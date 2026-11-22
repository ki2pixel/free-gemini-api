package gemini

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

const defaultToolSchemaCacheSize = 64

// CompiledToolCatalog holds the JSON Schemas compiled from one request's
// OpenAI tool declarations. It is immutable once built and therefore safe for
// concurrent use.
type CompiledToolCatalog struct {
	schemas    map[string]*jsonschema.Schema
	declared   map[string]struct{}
	compileErr error
}

// ValidateCall checks that name was declared in the request and that argsJSON
// conforms to the tool's parameter schema. It returns (true, "") on success or
// (false, detail) with a concise, model-actionable explanation.
func (c *CompiledToolCatalog) ValidateCall(name, argsJSON string) (bool, string) {
	if c == nil {
		return true, ""
	}
	if _, ok := c.declared[name]; !ok {
		return false, fmt.Sprintf("tool '%s' is not declared in the request", name)
	}
	schema := c.schemas[name]
	if schema == nil {
		// The tool is declared but carries no usable parameter schema. Do not
		// block calls that cannot be validated.
		return true, ""
	}
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}
	args, err := jsonschema.UnmarshalJSON(strings.NewReader(argsJSON))
	if err != nil {
		return false, fmt.Sprintf("invalid JSON arguments: %v", err)
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := schema.Validate(args); err != nil {
		return false, describeSchemaViolation(err)
	}
	return true, ""
}

// ToolSchemaRegistry caches compiled tool catalogs keyed by a SHA-256 hash of
// the request's tool block. Compilation is comparatively expensive, while
// agent harnesses replay the same tool declarations on every turn.
type ToolSchemaRegistry struct {
	mu         sync.RWMutex
	maxEntries int
	order      *list.List
	entries    map[string]*list.Element
}

type toolSchemaCacheEntry struct {
	key     string
	catalog *CompiledToolCatalog
}

// DefaultToolSchemaRegistry is the process-wide cache used by the arranger and
// the API retry path.
var DefaultToolSchemaRegistry = NewToolSchemaRegistry()

// NewToolSchemaRegistry creates an empty LRU-backed registry.
func NewToolSchemaRegistry() *ToolSchemaRegistry {
	return &ToolSchemaRegistry{
		maxEntries: defaultToolSchemaCacheSize,
		order:      list.New(),
		entries:    make(map[string]*list.Element),
	}
}

// CompileTools returns the compiled catalog for tools, reusing a cached entry
// when the exact tool block was seen before. An error is returned when one or
// more schemas could not be compiled; the catalog remains usable and treats
// those tools as declared but unvalidated.
func (r *ToolSchemaRegistry) CompileTools(tools []map[string]any) (*CompiledToolCatalog, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	key, err := toolSchemaCacheKey(tools)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if el, ok := r.entries[key]; ok {
		r.order.MoveToFront(el)
		catalog := el.Value.(*toolSchemaCacheEntry).catalog
		r.mu.Unlock()
		return catalog, catalog.compileErr
	}
	r.mu.Unlock()

	catalog, compileErr := compileToolsUncached(tools, key)

	r.mu.Lock()
	defer r.mu.Unlock()
	if el, ok := r.entries[key]; ok {
		r.order.MoveToFront(el)
		cached := el.Value.(*toolSchemaCacheEntry).catalog
		return cached, cached.compileErr
	}
	el := r.order.PushFront(&toolSchemaCacheEntry{key: key, catalog: catalog})
	r.entries[key] = el
	for r.order.Len() > r.maxEntries {
		oldest := r.order.Back()
		if oldest == nil {
			break
		}
		r.order.Remove(oldest)
		delete(r.entries, oldest.Value.(*toolSchemaCacheEntry).key)
	}
	return catalog, compileErr
}

func toolSchemaCacheKey(tools []map[string]any) (string, error) {
	raw, err := json.Marshal(tools)
	if err != nil {
		return "", fmt.Errorf("marshal tool declarations: %w", err)
	}
	sum := sha256.Sum256(raw)
	return "v1:" + hex.EncodeToString(sum[:]), nil
}

func compileToolsUncached(tools []map[string]any, key string) (*CompiledToolCatalog, error) {
	catalog := &CompiledToolCatalog{
		schemas:  make(map[string]*jsonschema.Schema, len(tools)),
		declared: make(map[string]struct{}, len(tools)),
	}
	var failures []string
	for i, tool := range tools {
		fn := toolFunctionDefinition(tool)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		catalog.declared[name] = struct{}{}

		params, ok := fn["parameters"].(map[string]any)
		if !ok || len(params) == 0 {
			if input, okInput := fn["inputSchema"].(map[string]any); okInput {
				params = input
			}
		}
		if len(params) == 0 {
			continue
		}

		compiler := jsonschema.NewCompiler()
		url := fmt.Sprintf("https://free-gemini-api.local/tools/%s/%d.json", key[3:19], i)
		if err := compiler.AddResource(url, params); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		schema, err := compiler.Compile(url)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		catalog.schemas[name] = schema
	}
	if len(failures) > 0 {
		catalog.compileErr = fmt.Errorf("tool schema compilation failed: %s", strings.Join(failures, "; "))
	}
	return catalog, catalog.compileErr
}

// toolFunctionDefinition accepts both the OpenAI envelope
// ({"type":"function","function":{...}}) and an already-flattened definition.
func toolFunctionDefinition(tool map[string]any) map[string]any {
	if fn, ok := tool["function"].(map[string]any); ok {
		return fn
	}
	if _, ok := tool["name"].(string); ok {
		return tool
	}
	return nil
}

// HasInvalidSchemaToolCall parses raw model output without schema filtering and
// reports the first tool call that violates the declared schemas. It is used by
// the API layer to trigger an abstract-error retry before any call reaches the
// host.
func HasInvalidSchemaToolCall(raw string, tools []map[string]any) (bool, string) {
	if len(tools) == 0 {
		return false, ""
	}
	arranged := ArrangeGeminiOutput(raw)
	if !arranged.IsToolCall || len(arranged.ToolCalls) == 0 {
		return false, ""
	}
	catalog, _ := DefaultToolSchemaRegistry.CompileTools(tools)
	if catalog == nil {
		return false, ""
	}
	for _, tc := range arranged.ToolCalls {
		if ok, detail := catalog.ValidateCall(tc.Function.Name, tc.Function.Arguments); !ok {
			return true, fmt.Sprintf("tool '%s': %s", tc.Function.Name, detail)
		}
	}
	return false, ""
}

func describeSchemaViolation(err error) string {
	validationErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return err.Error()
	}
	leaf := leafValidationError(validationErr)
	field := strings.Join(leaf.InstanceLocation, ".")
	switch k := leaf.ErrorKind.(type) {
	case *kind.Required:
		switch len(k.Missing) {
		case 0:
			return "missing required property"
		case 1:
			return fmt.Sprintf("missing required property '%s'", k.Missing[0])
		default:
			quoted := make([]string, 0, len(k.Missing))
			for _, missing := range k.Missing {
				quoted = append(quoted, "'"+missing+"'")
			}
			return "missing required properties " + strings.Join(quoted, ", ")
		}
	case *kind.Type:
		want := strings.Join(k.Want, " or ")
		if field != "" {
			return fmt.Sprintf("type mismatch for '%s': got %s, want %s", field, k.Got, want)
		}
		return fmt.Sprintf("type mismatch: got %s, want %s", k.Got, want)
	default:
		detail := leaf.Error()
		if field != "" && !strings.Contains(detail, field) {
			return fmt.Sprintf("invalid value for '%s': %s", field, detail)
		}
		return detail
	}
}

// leafValidationError descends to the most specific cause so the retry
// directive names the offending property instead of a generic anyOf/oneOf
// wrapper.
func leafValidationError(err *jsonschema.ValidationError) *jsonschema.ValidationError {
	for len(err.Causes) > 0 {
		next := err.Causes[0]
		for _, cause := range err.Causes {
			if _, ok := cause.ErrorKind.(*kind.Required); ok {
				next = cause
				break
			}
		}
		err = next
	}
	return err
}
