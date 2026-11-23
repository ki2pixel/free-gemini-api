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
	if argsMap, ok := args.(map[string]any); ok {
		if valid, detail := validateToolArgumentSemantics(name, argsMap); !valid {
			return false, detail
		}
	}
	return true, ""
}

// validateToolArgumentSemantics enforces argument invariants that JSON Schema
// cannot express portably: a path must not be blank, and an edit must carry a
// non-empty anchor. An empty `oldString` is especially dangerous because it
// passes a `type: string` schema while the host cannot locate any text to
// replace (and tools may interpret it as a whole-file overwrite).
func validateToolArgumentSemantics(name string, args map[string]any) (bool, string) {
	for _, key := range []string{"path", "file_path", "filePath", "filepath"} {
		if value, ok := args[key]; ok {
			text, isString := value.(string)
			if !isString || strings.TrimSpace(text) == "" {
				return false, fmt.Sprintf("'%s' must not be empty", key)
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit", "multiedit", "patch", "apply_patch", "replace":
		for _, key := range []string{"oldString", "old_string", "oldText", "old_text"} {
			value, ok := args[key]
			if !ok {
				continue
			}
			text, isString := value.(string)
			if !isString || strings.TrimSpace(text) == "" {
				return false, fmt.Sprintf("'%s' must not be empty; copy the exact text to replace, or use write to create/overwrite the file", key)
			}
		}
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

// explicitToolCalls extracts only the calls the model explicitly framed as
// tool invocations: <tool_call> envelopes or Markdown JSON fences. It never
// scans arbitrary prose for balanced JSON objects, because a final answer may
// legitimately quote {"name":"read"} while explaining the work it performed.
func explicitToolCalls(raw string) []ToolCall {
	// Thinking blocks are reasoning, not executable output: the arranger strips
	// them before parsing, so a plan that quotes a JSON call inside <thought>
	// must not be validated as if it had been emitted to the host.
	frame := thoughtRegex.ReplaceAllString(raw, "")
	var calls []ToolCall
	if strings.Contains(frame, "<tool_call>") {
		// A nil catalog keeps schema-invalid calls in the result so the caller
		// can report the precise violation instead of silently dropping it.
		tagged, _ := extractTaggedToolCalls(frame, nil)
		calls = append(calls, tagged...)
	}
	for _, match := range jsonCodeBlock.FindAllStringSubmatch(frame, -1) {
		if len(match) < 2 {
			continue
		}
		if tc, ok := parseSingleToolCall(strings.TrimSpace(match[1])); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// HasInvalidSchemaToolCall reports the first explicitly framed tool call that
// violates the declared schemas. It is used by the API layer to trigger an
// abstract-error retry before any call reaches the host.
//
// Only calls explicitly framed by the model (<tool_call> tags or a Markdown
// JSON fence) are validated. Final prose often mentions tool names and may
// quote JSON fragments; the catalog-aware arranger keeps those as text instead
// of promoting them to calls, so a legitimate synthesis is not rejected with a
// spurious "missing required property" error.
func HasInvalidSchemaToolCall(raw string, tools []map[string]any) (bool, string) {
	if len(tools) == 0 {
		return false, ""
	}
	catalog, _ := DefaultToolSchemaRegistry.CompileTools(tools)
	if catalog == nil {
		return false, ""
	}
	arranged := ArrangeGeminiOutputWithTools(raw, tools)
	calls := explicitToolCalls(raw)
	if len(calls) == 0 {
		// A call without an explicit envelope is considered only when the
		// catalog-aware arranger accepted it; arbitrary prose JSON and dropped
		// schema violations were already discarded by that pass.
		calls = arranged.ToolCalls
	}
	for _, tc := range calls {
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
