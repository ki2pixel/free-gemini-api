package gemini

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// ToolCall represents an OpenAI-compatible function tool call
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // always "function"
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // stringified JSON object
}

// ArrangedOutput contains the parsed, cleaned, and structured result of Gemini's response
type ArrangedOutput struct {
	CleanText        string     `json:"clean_text"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	IsToolCall       bool       `json:"is_tool_call"`
	FinishReason     string     `json:"finish_reason"` // "tool_calls" or "stop"
	// UntaggedMutation reports that a declared mutating tool call was emitted
	// as raw JSON outside a <tool_call> envelope. It was removed from CleanText
	// and never promoted: the turn must be retried, never executed.
	UntaggedMutation bool `json:"untagged_mutation,omitempty"`
	// Repairs lists the repair kinds applied while arranging the raw output
	// ("disclaimer", "control_chars", "brace_envelope"). The API layer turns
	// them into Prometheus counters.
	Repairs []string `json:"repairs,omitempty"`
}

const (
	maxToolDescriptionRunes     = 180
	maxPropertyDescriptionRunes = 120
	maxCompactSchemaDepth       = 6
	// maxStrictSignatureRunes bounds the signature appended to a tool
	// description. The signature restates parameter names the model must not
	// invent; it is capped so a wide schema cannot inflate the catalog that
	// TestFormatToolsInstruction_CompactOutput requires to stay compact.
	maxStrictSignatureRunes = 220
	// strictParametersMarker makes the injection idempotent: a description
	// that already carries the signature is never rewritten.
	strictParametersMarker = "STRICT PARAMETERS:"
)

// orderedJSONField and orderedJSONObject preserve a deliberate key order
// (notably `path` first) that encoding/json would otherwise sort
// alphabetically.
type orderedJSONField struct {
	key   string
	value any
}

type orderedJSONObject []orderedJSONField

func (o orderedJSONObject) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, field := range o {
		if i > 0 {
			sb.WriteByte(',')
		}
		key, err := json.Marshal(field.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.value)
		if err != nil {
			return nil, err
		}
		sb.Write(key)
		sb.WriteByte(':')
		sb.Write(value)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

// FormatToolsInstruction creates a clear, structured system prompt injecting
// tools into Gemini. Schemas are compacted to their actionable fields
// (name/description/required/properties) to cut the prompt footprint and to
// keep required keys — `path` first — at the model's focus point.
func FormatToolsInstruction(tools []map[string]any) string {
	if len(tools) == 0 {
		return ""
	}

	toolsJSON, err := json.Marshal(compactToolDefinitions(tools))
	if err != nil {
		toolsJSON, err = json.Marshal(tools)
		if err != nil {
			toolsJSON = []byte("[]")
		}
	}

	var sb strings.Builder
	sb.WriteString("[AVAILABLE TOOLS]\n")
	sb.WriteString("You have active tools in this session that you can invoke:\n")
	sb.WriteString("```json\n")
	sb.Write(toolsJSON)
	sb.WriteString("\n```\n\n")
	sb.WriteString("[TOOL CALL INSTRUCTIONS]\n")
	sb.WriteString("You are an autonomous AI coding agent operating in a live development environment with real-time tool execution.\n")
	sb.WriteString("1. Before calling tools or formulating your response, you can think step-by-step inside `<thought>...</thought>` tags. Your thoughts are streamed live to the user interface.\n")
	sb.WriteString("2. When you need to inspect files, run shell/terminal commands, search code, or apply changes, you MUST invoke the appropriate tools using `<tool_call>` XML blocks in this exact format:\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "<tool_name>", "arguments": {<json_arguments>}}` + "\n")
	sb.WriteString("</tool_call>\n")
	sb.WriteString("3. Read-Parallel / Write-Serial cadence: batch 2 to 4 read-only tool calls (read, glob, grep, list) in parallel when inspecting; serialize mutations strictly by emitting at most one file-writing tool call (write, edit, or a shell command that changes files) per turn so each change can be verified before the next. After that single mutation, stop the turn immediately and wait for the host's tool result before issuing any further call, including read-only verification calls.\n")
	sb.WriteString("4. Metadata-First & Strict Parameters: place `path` first in every file tool argument object, before `content`, `old_string`, `new_string`, or `diff`, and keep large payloads last. Ensure the \"arguments\" object strictly follows the schema parameters. The `⚠️ STRICT PARAMETERS` line of each tool is the authoritative list of names: NEVER guess parameter names from prior training data and never reuse the conventions of a different tool. Array arguments must contain the declared item type, and when the schema and an example disagree, the schema wins. When passing multi-line file content or code snippets in string arguments, escape all newlines as \\n and escape all internal double quotes as \\\" so the JSON remains strictly valid.\n")
	sb.WriteString("5. The host environment executes each `<tool_call>` directly and returns the output to you. Every tool listed in [AVAILABLE TOOLS] remains active and executable for the entire session.\n")
	sb.WriteString("6. Invoke tools exclusively through `<tool_call>` XML blocks; the [AVAILABLE TOOLS] catalog is internal context, not response content.\n")
	sb.WriteString("7. Continuous and active tool access: Every tool listed in [AVAILABLE TOOLS] is permanently active and available on every turn. Continue invoking tools turn after turn until all requested file modifications, builds, and verifications are fully completed.\n")
	sb.WriteString("8. When asked to create or update a file (such as AGENTS.md), invoke the write/edit tool directly with a `<tool_call>` block; the host executes it and returns the result.\n")
	sb.WriteString("9. Exact-anchor rule (Poka-Yoke): when using `edit`, `oldString` (or `old_string`) MUST be a non-empty exact verbatim substring copied from a recent `read` output. Never invent a section heading, never wrap the anchor in a markdown link unless the file itself contains that exact markdown link, and never mix conversational prose or prompt text into the arguments. If the anchor is not known byte-for-byte, call `read` again first. If the host reports that the anchor was not found or that `oldString` is empty, re-read the file and replace the anchor with a fresh exact substring; never repeat the same failing anchor.\n")
	sb.WriteString("10. Evidence-based completion (Counter-S7): a tool result that reports an error is not a success. Explicitly acknowledge every failed tool call, fix it, and verify the result before continuing. Never claim that a file was updated, or that the task is 100% complete, unless the corresponding tool result confirms the mutation succeeded. When errors remain, list them and continue working instead of writing a success report.\n")
	sb.WriteString("11. Completion gate (anti-pseudo-report): Do not emit a final completion report or summary if any planned modification or verification step is still pending. Execute all tool calls until all files are modified. A prose report is not a substitute for the remaining write/edit/tool calls.\n\n")
	sb.WriteString("[TOOL CALL EXAMPLE]\n")
	sb.WriteString("User: Peux-tu inspecter main.rs et vérifier les tests ?\n")
	sb.WriteString("Assistant:\n")
	sb.WriteString("<thought>\n")
	sb.WriteString("Je vais d'abord inspecter le fichier main.rs pour comprendre le code existant, puis lancer la suite de tests avec cargo.\n")
	sb.WriteString("</thought>\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "read", "arguments": {"path": "main.rs"}}` + "\n")
	sb.WriteString("</tool_call>\n")
	sb.WriteString("<tool_call>\n")
	sb.WriteString(`{"name": "shell", "arguments": {"command": "cargo test"}}` + "\n")
	sb.WriteString("</tool_call>\n")

	return sb.String()
}

// compactToolDefinitions projects OpenAI tool declarations onto the fields the
// model actually needs in order to produce a valid call.
func compactToolDefinitions(tools []map[string]any) []orderedJSONObject {
	out := make([]orderedJSONObject, 0, len(tools))
	for _, tool := range tools {
		fn := toolFunctionDefinition(tool)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		obj := orderedJSONObject{{key: "name", value: name}}
		description, _ := fn["description"].(string)
		description = truncateRunes(strings.TrimSpace(description), maxToolDescriptionRunes)
		params, ok := fn["parameters"].(map[string]any)
		if !ok || len(params) == 0 {
			if input, okInput := fn["inputSchema"].(map[string]any); okInput {
				params = input
			}
		}
		var compactedParams orderedJSONObject
		if len(params) > 0 {
			compactedParams = compactParametersSchema(params)
		}
		// The signature is derived from the compacted schema, never from the
		// raw one: compactParametersSchema drops undeclared `required` entries
		// and synthesizes `items`, so a raw-schema signature would advertise
		// parameters the emitted schema does not contain — the exact
		// hallucination this hint is meant to prevent.
		if signature := formatStrictParameterSignature(compactedParams); signature != "" {
			description = injectStrictParameterSignature(description, signature)
		}
		if description != "" {
			obj = append(obj, orderedJSONField{key: "description", value: description})
		}
		if len(compactedParams) > 0 {
			obj = append(obj, orderedJSONField{key: "parameters", value: compactedParams})
		}
		out = append(out, obj)
	}
	return out
}

// formatStrictParameterSignature restates the compacted parameters as
// "name (type, REQUIRED)" so the model copies real names instead of guessing
// them from prior training data. It returns "" when there is nothing to state.
func formatStrictParameterSignature(params orderedJSONObject) string {
	props := orderedJSONObjectField(params, "properties")
	if props == nil {
		return ""
	}
	propertyObject, ok := props.(orderedJSONObject)
	if !ok || len(propertyObject) == 0 {
		return ""
	}
	required := make(map[string]struct{})
	if list, ok := orderedJSONObjectField(params, "required").([]string); ok {
		for _, key := range list {
			required[key] = struct{}{}
		}
	}
	parts := make([]string, 0, len(propertyObject))
	for _, property := range propertyObject {
		hint := parameterTypeHint(property.value)
		if hint == "" {
			hint = property.key
		} else {
			hint = property.key + " " + hint
		}
		if _, isRequired := required[property.key]; isRequired {
			hint += ", REQUIRED"
		}
		parts = append(parts, hint)
	}
	signature := strings.Join(parts, "; ")
	if signature == "" {
		return ""
	}
	return truncateRunes(signature, maxStrictSignatureRunes)
}

// parameterTypeHint renders the declared type of one compacted parameter,
// including enum members so literal values are never invented either.
func parameterTypeHint(value any) string {
	schema, ok := value.(orderedJSONObject)
	if !ok {
		return ""
	}
	typeName, _ := orderedJSONObjectField(schema, "type").(string)
	typeName = strings.TrimSpace(typeName)
	if members, ok := orderedJSONObjectField(schema, "enum").([]any); ok && len(members) > 0 {
		quoted := make([]string, 0, len(members))
		for _, member := range members {
			if literal, ok := member.(string); ok {
				quoted = append(quoted, literal)
			}
		}
		if len(quoted) > 0 {
			enum := "enum[" + strings.Join(quoted, "|") + "]"
			if typeName == "" {
				return enum
			}
			return typeName + " " + enum
		}
	}
	if typeName == "" {
		if _, hasProperties := orderedJSONObjectField(schema, "properties").(orderedJSONObject); hasProperties {
			return "object"
		}
		return ""
	}
	if isArraySchemaType(typeName) {
		if items, ok := orderedJSONObjectField(schema, "items").(orderedJSONObject); ok {
			if itemType, ok := orderedJSONObjectField(items, "type").(string); ok && strings.TrimSpace(itemType) != "" {
				return "array of " + strings.TrimSpace(itemType)
			}
		}
	}
	return typeName
}

// injectStrictParameterSignature appends the signature to a tool description.
// It is idempotent: a description already carrying the marker is returned
// unchanged, so rebuilding the prompt never accumulates signatures.
func injectStrictParameterSignature(description, signature string) string {
	if signature == "" || strings.Contains(description, strictParametersMarker) {
		return description
	}
	if description == "" {
		return "⚠️ " + strictParametersMarker + " " + signature
	}
	return description + "\n\n⚠️ " + strictParametersMarker + " " + signature
}

func orderedJSONObjectField(obj orderedJSONObject, key string) any {
	for _, field := range obj {
		if field.key == key {
			return field.value
		}
	}
	return nil
}

func compactParametersSchema(params map[string]any) orderedJSONObject {
	obj := orderedJSONObject{}
	typ, _ := params["type"].(string)
	if typ == "" {
		typ = "object"
	}
	obj = append(obj, orderedJSONField{key: "type", value: typ})

	props, hasProps := params["properties"].(map[string]any)
	required := stringList(params["required"])
	if hasProps && len(props) > 0 {
		// A required key that is not declared in properties makes the upstream
		// Protobuf validator reject the whole request
		// ("parameters.required[X]: property is not defined").
		required = filterRequiredAgainstProperties(required, props)
	}
	if len(required) > 0 {
		obj = append(obj, orderedJSONField{key: "required", value: pathFirst(required)})
	}
	if hasProps && len(props) > 0 {
		obj = append(obj, orderedJSONField{key: "properties", value: compactProperties(props, required)})
	}
	if additional, ok := params["additionalProperties"]; ok {
		obj = append(obj, orderedJSONField{key: "additionalProperties", value: additional})
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if value, ok := params[key]; ok {
			obj = append(obj, orderedJSONField{key: key, value: compactSchemaList(value, 0)})
		}
	}
	// Reference-based parameter schemas need their definitions to stay
	// resolvable in the prompt.
	for _, key := range []string{"$ref", "$defs", "definitions"} {
		if value, ok := params[key]; ok {
			obj = append(obj, orderedJSONField{key: key, value: value})
		}
	}
	return obj
}

func compactProperties(props map[string]any, required []string) orderedJSONObject {
	keys := orderedPropertyKeys(props, required)
	out := make(orderedJSONObject, 0, len(keys))
	for _, key := range keys {
		out = append(out, orderedJSONField{key: key, value: compactPropertySchema(props[key], 0)})
	}
	return out
}

// filterRequiredAgainstProperties drops every required entry that has no
// matching declaration in properties. Gemini otherwise rejects the whole
// request with "parameters.required[X]: property is not defined".
func filterRequiredAgainstProperties(required []string, props map[string]any) []string {
	if len(required) == 0 || len(props) == 0 {
		return nil
	}
	out := make([]string, 0, len(required))
	for _, name := range required {
		if _, ok := props[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// isArraySchemaType reports whether a JSON Schema type is "array", regardless
// of casing. The proxy emits lower-case types; the check stays defensive.
func isArraySchemaType(value any) bool {
	typ, ok := value.(string)
	return ok && strings.EqualFold(strings.TrimSpace(typ), "array")
}

// defaultArrayItemsSchema is the fallback items schema injected into an array
// declared without one. Gemini's Protobuf validator rejects
// "parameters.properties[X].items: missing field" otherwise.
func defaultArrayItemsSchema() orderedJSONObject {
	return orderedJSONObject{{key: "type", value: "string"}}
}

// orderedPropertyKeys puts `path` first, then the remaining required keys in
// declaration order, then every other property alphabetically.
func orderedPropertyKeys(props map[string]any, required []string) []string {
	seen := make(map[string]struct{}, len(props))
	keys := make([]string, 0, len(props))
	add := func(key string) {
		if _, ok := props[key]; !ok {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	add("path")
	for _, key := range required {
		add(key)
	}
	rest := make([]string, 0, len(props))
	for key := range props {
		if _, ok := seen[key]; !ok {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func pathFirst(required []string) []string {
	out := make([]string, 0, len(required))
	for _, key := range required {
		if key == "path" {
			out = append(out, key)
			break
		}
	}
	for _, key := range required {
		if key != "path" {
			out = append(out, key)
		}
	}
	return out
}

func stringList(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, item := range values {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

var compactSchemaFields = []string{
	"type", "description", "enum", "const", "default", "format", "items",
	"properties", "required", "additionalProperties", "anyOf", "oneOf",
	"allOf", "minimum", "maximum", "minLength", "maxLength", "pattern",
	"minItems", "maxItems", "uniqueItems",
}

func compactPropertySchema(value any, depth int) any {
	schema, ok := value.(map[string]any)
	if !ok {
		return value
	}
	// Preserve reference-based schemas verbatim: rewriting a $ref target
	// without its $defs would break validation.
	if _, ok := schema["$ref"]; ok {
		return schema
	}
	if _, ok := schema["$defs"]; ok {
		return schema
	}
	if _, ok := schema["definitions"]; ok {
		return schema
	}
	if depth > maxCompactSchemaDepth {
		obj := orderedJSONObject{}
		if typ, ok := schema["type"]; ok {
			obj = append(obj, orderedJSONField{key: "type", value: typ})
			if isArraySchemaType(typ) {
				obj = append(obj, orderedJSONField{key: "items", value: defaultArrayItemsSchema()})
			}
		}
		return obj
	}

	// Filter required against the node's own properties before either field is
	// emitted, so an undefined requirement can never reach the upstream
	// Protobuf validator. Like the root schema, an object that declares no
	// properties keeps its required list untouched.
	props, hasProps := schema["properties"].(map[string]any)
	required := stringList(schema["required"])
	if hasProps && len(props) > 0 {
		required = filterRequiredAgainstProperties(required, props)
	}

	obj := orderedJSONObject{}
	for _, key := range compactSchemaFields {
		fieldValue, exists := schema[key]
		if !exists {
			continue
		}
		switch key {
		case "description":
			desc, ok := fieldValue.(string)
			if !ok {
				continue
			}
			if desc = truncateRunes(strings.TrimSpace(desc), maxPropertyDescriptionRunes); desc != "" {
				obj = append(obj, orderedJSONField{key: key, value: desc})
			}
		case "items":
			compacted := compactPropertySchema(fieldValue, depth+1)
			if isEmptyItemsSchema(compacted) {
				// Absent or empty nodes are rebuilt below for array schemas.
				continue
			}
			obj = append(obj, orderedJSONField{key: key, value: compacted})
		case "properties":
			if !hasProps {
				continue
			}
			obj = append(obj, orderedJSONField{key: key, value: compactProperties(props, required)})
		case "required":
			if len(required) > 0 {
				obj = append(obj, orderedJSONField{key: key, value: pathFirst(required)})
			}
		case "anyOf", "oneOf", "allOf":
			obj = append(obj, orderedJSONField{key: key, value: compactSchemaList(fieldValue, depth+1)})
		default:
			obj = append(obj, orderedJSONField{key: key, value: fieldValue})
		}
	}
	// Synthesize a minimal items schema after cleaning: Gemini's Protobuf
	// validator rejects "parameters.properties[X].items: missing field".
	if isArraySchemaType(schema["type"]) && !hasOrderedJSONField(obj, "items") {
		obj = append(obj, orderedJSONField{key: "items", value: defaultArrayItemsSchema()})
	}
	if len(obj) == 0 {
		return schema
	}
	return obj
}

func hasOrderedJSONField(obj orderedJSONObject, key string) bool {
	for _, field := range obj {
		if field.key == key {
			return true
		}
	}
	return false
}

func isEmptyItemsSchema(value any) bool {
	switch items := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(items) == 0
	case orderedJSONObject:
		return len(items) == 0
	default:
		return false
	}
}

func compactSchemaList(value any, depth int) any {
	items, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = compactPropertySchema(item, depth)
	}
	return out
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

// Thought extraction is tag-based rather than regex-based: the upstream model
// frequently switches directly to a <tool_call> (or the stream is cut off)
// without ever emitting the closing tag. A regex requiring a matched pair
// leaks the whole thought into the assistant-visible content.
var (
	thoughtOpenTags  = []string{"<thought>", "<thinking>"}
	thoughtCloseTags = []string{"</thought>", "</thinking>"}
	thoughtTagRegex  = regexp.MustCompile(`</?(?:thought|thinking)>`)

	jsonCodeBlock = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{[\\s\\S]*?\\})\\s*```")
	// Gemini web auto-linkifies bare URLs in its rendered stream, producing
	// "[https://x](https://x)" instead of the plain URL. Inside tool-call
	// arguments that extra markdown syntax corrupts the JSON payload.
	autoLinkRegex = regexp.MustCompile(`\[(https?://[^\]]+)\]\((https?://[^)]+)\)`)

	heuristicNameRegex    = regexp.MustCompile(`"(?:name|tool|function)"\s*:\s*"([^"]+)"`)
	heuristicPathRegex    = regexp.MustCompile(`"(?:path|file|filename)"\s*:\s*"([^"]+)"`)
	heuristicPatternRegex = regexp.MustCompile(`"(?:pattern|query)"\s*:\s*"([^"]+)"`)
	heuristicArgEndRegex  = regexp.MustCompile(`"(?:\s*,\s*"[a-zA-Z0-9_-]+"\s*:|\s*\}\s*\}|\s*\}\s*$)`)
	// A payload whose first key is a mutation-specific field is a file-writing
	// call even when the tool name appears later (or is truncated away).
	mutationKeyFirstRegex = regexp.MustCompile(`^\s*\{\s*"(?:content|old_string|new_string|diff|patch)"\s*:`)
	// Used only as a conservative fallback when the tool name cannot be
	// determined reliably from a truncated payload.
	mutationKeyAnywhereRegex = regexp.MustCompile(`"(?:content|old_string|new_string|diff|patch)"\s*:`)
)

// upstreamDisclaimerPatterns matches the canned refusals emitted by the
// upstream web backend. Apostrophes are matched natively as either ASCII ('),
// or typographic (’), so the byte offsets returned against the original string
// stay exact even in French text containing many U+2019 characters.
var upstreamDisclaimerPatterns = func() []*regexp.Regexp {
	disclaimers := []string{
		"i'm just a language model",
		"i am just a language model",
		"i'm a language model",
		"i am a language model",
		"can't help with that",
		"cannot help with that",
		"i can't assist",
		"i cannot assist",
		"i'm unable to assist",
		"i am unable to assist",
		"i'm unable to help",
		"i am unable to help",
		"as an ai language model",
		"as a large language model",
		"i apologize, but i cannot",
		"i apologize, but i can't",
		"i'm sorry, but i cannot",
		"i'm sorry, but i can't",
	}
	patterns := make([]*regexp.Regexp, 0, len(disclaimers))
	for _, d := range disclaimers {
		quoted := regexp.QuoteMeta(d)
		quoted = strings.ReplaceAll(quoted, "'", "['’]")
		patterns = append(patterns, regexp.MustCompile("(?i)"+quoted))
	}
	return patterns
}()

// isMutatingTool reports whether a tool name can write to the filesystem or
// execute a state-changing command. Mid-value truncations of these tools must
// never be auto-repaired: a syntactically valid but incomplete payload would
// overwrite a file (or run a partial command) with truncated content.
func isMutatingTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write", "edit", "shell", "patch", "multiedit", "apply_patch":
		return true
	default:
		return false
	}
}

// IsMutatingTool reports whether name designates a tool that can change
// filesystem or process state. It is the exported counterpart of
// isMutatingTool for API-layer bookkeeping (doom-loop tracking).
func IsMutatingTool(name string) bool {
	return isMutatingTool(name)
}

// isMutatingToolPayload infers whether a raw (possibly truncated) tool-call
// payload targets a mutating tool. The tool name is normally emitted first,
// but a content-first payload is also classified as a mutation.
func isMutatingToolPayload(payload string) bool {
	payload = trimToolCallFence(payload)
	if mutationKeyFirstRegex.MatchString(payload) {
		return true
	}
	if m := heuristicNameRegex.FindStringSubmatch(payload); len(m) >= 2 {
		name := strings.ToLower(strings.TrimSpace(m[1]))
		if isMutatingTool(name) {
			return true
		}
		// The name was found but may itself be text inside a truncated
		// content string. If an arguments object carries mutation-specific
		// keys, stay conservative and treat the payload as a mutation.
		if (strings.Contains(payload, `"arguments"`) || strings.Contains(payload, `"parameters"`)) &&
			mutationKeyAnywhereRegex.MatchString(payload) {
			return true
		}
		return false
	}
	// No name could be recovered from the truncated payload: fall back to the
	// presence of mutation-specific keys.
	return mutationKeyAnywhereRegex.MatchString(payload)
}

// trimToolCallFence removes an optional markdown code fence around a tool-call
// payload before analysis or parsing.
func trimToolCallFence(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// shouldRejectMidValueRepair reports whether repairing the payload would turn
// a truncated mutation into a plausible but incomplete file write.
func shouldRejectMidValueRepair(payload string) bool {
	_, isMidValue := autoCloseJSONBraces(payload)
	return isMidValue && isMutatingToolPayload(payload)
}

// deLinkifyAutoLinks rewrites Gemini's auto-linkified anchors back to the bare
// URL when the label and href are identical (the auto-link artifact shape).
// A genuine markdown link whose label differs from its href is left intact.
func deLinkifyAutoLinks(s string) string {
	return autoLinkRegex.ReplaceAllStringFunc(s, func(m string) string {
		sub := autoLinkRegex.FindStringSubmatch(m)
		if len(sub) == 3 && sub[1] == sub[2] {
			return sub[1]
		}
		return m
	})
}

// ArrangeGeminiOutput analyzes, cleans, parses, and structures raw Gemini
// output without schema validation. It is kept for backward compatibility.
func ArrangeGeminiOutput(raw string) *ArrangedOutput {
	return ArrangeGeminiOutputWithTools(raw, nil)
}

// ArrangeGeminiOutputWithTools analyzes, cleans, parses, and strictly validates
// raw Gemini output against the request's declared tool schemas. Calls that do
// not conform are dropped so they can never reach the host.
func ArrangeGeminiOutputWithTools(raw string, tools []map[string]any) *ArrangedOutput {
	var catalog *CompiledToolCatalog
	if len(tools) > 0 {
		catalog, _ = DefaultToolSchemaRegistry.CompileTools(tools)
	}

	res := &ArrangedOutput{
		FinishReason: "stop",
	}
	if start, _ := findUpstreamDisclaimerOffset(raw); start != -1 {
		res.Repairs = append(res.Repairs, "disclaimer")
	}
	if (strings.Contains(raw, "<tool_call>") || strings.Contains(raw, `"arguments"`)) &&
		normalizeJSONStringLiterals(raw) != raw {
		res.Repairs = append(res.Repairs, "control_chars")
	}

	text := raw

	// 1. Extract and separate Thinking / Reasoning blocks (<thought> or
	// <thinking>). The extractor also handles unclosed thoughts (a switch to a
	// tool call or a truncated stream), nested/orphan tags, and guarantees that
	// no thought text or tag can leak into the assistant-visible content.
	text, res.ReasoningContent = extractThoughtsAndContent(raw)

	text = strings.TrimSpace(text)

	// Gemini's web stream auto-linkifies bare URLs ("[https://x](https://x)").
	// Undo that so tool-call arguments keep the plain URL.
	text = deLinkifyAutoLinks(text)

	// 2. Extract Tool Calls from <tool_call> tags (supporting standard </tool_call>, markdown fences ```, or unclosed blocks)
	if strings.Contains(text, "<tool_call>") {
		taggedCalls, cleanText := extractTaggedToolCalls(text, catalog)
		text = cleanText
		if len(taggedCalls) > 0 {
			res.ToolCalls = taggedCalls
			res.IsToolCall = true
			res.FinishReason = "tool_calls"
			// A valid tagged call does not excuse a second, untagged payload
			// emitted in the same turn. Quarantine it so no raw tool JSON can
			// survive in CleanText; the API layer retries the whole turn.
			if catalog != nil {
				sanitized, hasMutation, promoted := quarantineUntaggedToolJSON(cleanText, catalog)
				res.UntaggedMutation = hasMutation
				res.ToolCalls = append(res.ToolCalls, promoted...)
				cleanText = sanitized
			}
			res.CleanText = sanitizeGeminiContentWithRepairs(res, cleanText)
			noteBraceEnvelopeRepair(res, raw)
			return res
		}
	}

	// 3. Untagged JSON is never promoted for a mutating tool. The catalog-aware
	// quarantine removes every declared-tool object from the visible text and
	// promotes only complete, schema-valid, read-only calls.
	if catalog != nil {
		sanitized, hasMutation, promoted := quarantineUntaggedToolJSON(text, catalog)
		res.UntaggedMutation = hasMutation
		if len(promoted) > 0 {
			res.ToolCalls = promoted
			res.IsToolCall = true
			res.FinishReason = "tool_calls"
		}
		res.CleanText = sanitizeGeminiContentWithRepairs(res, sanitized)
		return res
	}

	// 4. Fallback without a declared catalog: preserve the historical
	// best-effort promotion of a complete JSON call.
	if strings.Contains(text, `"name"`) {
		// Try code block first
		if cbMatches := jsonCodeBlock.FindAllStringSubmatch(text, -1); len(cbMatches) > 0 {
			for _, m := range cbMatches {
				if len(m) > 1 {
					payload := strings.TrimSpace(m[1])
					tc, ok := parseSingleToolCall(payload)
					if ok && acceptsToolCall(catalog, tc) {
						res.ToolCalls = append(res.ToolCalls, tc)
					}
				}
			}
		}
		// If still none, try entire text as JSON
		if len(res.ToolCalls) == 0 {
			tc, ok := parseSingleToolCall(text)
			if ok && acceptsToolCall(catalog, tc) {
				res.ToolCalls = append(res.ToolCalls, tc)
			}
		}
		// If still none, scan for the first balanced JSON object that parses as
		// a tool call. Models sometimes emit overlapping/concatenated objects
		// (e.g. a truncated attempt immediately followed by the full one), and
		// sometimes put the argument fields at the top level ({"name": "x",
		// "code": "..."}) instead of nesting them under "arguments".
		if len(res.ToolCalls) == 0 {
			if tc, ok := findFirstJSONToolCall(text, catalog); ok {
				res.ToolCalls = append(res.ToolCalls, tc)
			}
		}

		if len(res.ToolCalls) > 0 {
			res.IsToolCall = true
			res.FinishReason = "tool_calls"
			res.CleanText = ""
			noteBraceEnvelopeRepair(res, raw)
			return res
		}
	}

	// 5. Clean up any raw internal artifacts or trailing disclaimers
	res.CleanText = sanitizeGeminiContentWithRepairs(res, text)
	return res
}

// noteBraceEnvelopeRepair records that a tool call was recovered from an
// unclosed <tool_call> envelope (for example when the upstream stream stopped
// before the closing tag). It is purely observational and does not alter the
// arranged result.
func noteBraceEnvelopeRepair(res *ArrangedOutput, raw string) {
	if res == nil || !res.IsToolCall {
		return
	}
	if strings.Contains(raw, "<tool_call>") && !strings.Contains(raw, "</tool_call>") {
		res.Repairs = append(res.Repairs, "brace_envelope")
	}
}

// extractTaggedToolCalls extracts tool calls from <tool_call> blocks even if the model
// closed them with markdown fences (```) or omitted the closing tag entirely.
// When catalog is non-nil, calls that violate the declared schema are removed
// from both the result and the cleaned text.
func extractTaggedToolCalls(text string, catalog *CompiledToolCatalog) ([]ToolCall, string) {
	const tag = "<tool_call>"
	tagLen := len(tag)
	var tcs []ToolCall
	type span struct {
		start, end int
	}
	var spans []span

	pos := 0
	for pos < len(text) {
		idx := strings.Index(text[pos:], tag)
		if idx == -1 {
			break
		}
		absIdx := pos + idx

		braceOffset := strings.IndexByte(text[absIdx+tagLen:], '{')
		if braceOffset == -1 {
			pos = absIdx + tagLen
			continue
		}
		braceIdx := absIdx + tagLen + braceOffset

		objStr, ok := balancedJSONObjectAt(text, braceIdx)
		if ok {
			endObjIdx := braceIdx + len(objStr)
			tc, parsed := parseSingleToolCall(objStr)
			if !parsed {
				// If parsing objStr failed (e.g. premature brace cutoff), try candidate between <tool_call> and </tool_call>
				if closeIdx := strings.Index(text[absIdx+tagLen:], "</tool_call>"); closeIdx != -1 {
					candidate := strings.TrimSpace(text[absIdx+tagLen : absIdx+tagLen+closeIdx])
					if tcCand, parsedCand := parseSingleToolCall(candidate); parsedCand {
						tc = tcCand
						parsed = true
						endObjIdx = absIdx + tagLen + closeIdx
					} else if tcHeur, okHeur := extractHeuristicToolCall(candidate); okHeur {
						tc = tcHeur
						parsed = true
						endObjIdx = absIdx + tagLen + closeIdx
					}
				} else if tcHeur, okHeur := extractHeuristicToolCall(objStr); okHeur {
					tc = tcHeur
					parsed = true
				}
			}

			if parsed {
				if acceptsToolCall(catalog, tc) {
					tcs = append(tcs, tc)
				}
				endBlock := endObjIdx
				rest := text[endObjIdx:]
				trimmed := strings.TrimLeft(rest, " \t\r\n")
				wsLen := len(rest) - len(trimmed)
				if strings.HasPrefix(trimmed, "</tool_call>") {
					endBlock = endObjIdx + wsLen + len("</tool_call>")
				} else if strings.HasPrefix(trimmed, "```") {
					endBlock = endObjIdx + wsLen + len("```")
				}
				// Also swallow any immediate trailing upstream refusal/disclaimer if present
				if dStart, dEnd := findUpstreamDisclaimerOffset(text[endBlock:]); dStart == 0 {
					endBlock += dEnd
				}
				spans = append(spans, span{start: absIdx, end: endBlock})
				pos = endBlock
				continue
			}
		}

		// If balancedJSONObjectAt failed OR parseSingleToolCall failed on objStr:
		// Recover from an unclosed or truncated tool call (e.g. cut off by upstream disclaimer or EOF).
		recovered := false
		limit := len(text)
		if nextTagIdx := strings.Index(text[absIdx+tagLen:], tag); nextTagIdx != -1 {
			limit = absIdx + tagLen + nextTagIdx
		}

		// 1. Check if an explicit </tool_call> tag exists before limit
		if closeIdx := strings.Index(text[absIdx+tagLen:limit], "</tool_call>"); closeIdx != -1 {
			candLimit := absIdx + tagLen + closeIdx
			candidate := strings.TrimSpace(text[absIdx+tagLen : candLimit])
			if tc, parsed := parseSingleToolCall(candidate); parsed {
				if acceptsToolCall(catalog, tc) {
					tcs = append(tcs, tc)
				}
				endBlock := candLimit + len("</tool_call>")
				spans = append(spans, span{start: absIdx, end: endBlock})
				pos = endBlock
				recovered = true
			} else if !shouldRejectMidValueRepair(candidate) {
				fixed, _ := autoCloseJSONBraces(candidate)
				if tc, parsed := parseSingleToolCall(fixed); parsed {
					if acceptsToolCall(catalog, tc) {
						tcs = append(tcs, tc)
					}
					endBlock := candLimit + len("</tool_call>")
					spans = append(spans, span{start: absIdx, end: endBlock})
					pos = endBlock
					recovered = true
				} else if tcHeur, okHeur := extractHeuristicToolCall(candidate); okHeur {
					if acceptsToolCall(catalog, tcHeur) {
						tcs = append(tcs, tcHeur)
					}
					endBlock := candLimit + len("</tool_call>")
					spans = append(spans, span{start: absIdx, end: endBlock})
					pos = endBlock
					recovered = true
				}
			}
		}

		// 2. If no explicit </tool_call> tag, check if an upstream disclaimer or EOF truncated the call
		if !recovered {
			searchRegion := text[braceIdx:limit]
			dStart, dEnd := findUpstreamDisclaimerOffset(searchRegion)
			var candidate string
			endBlock := limit
			if dStart != -1 {
				candidate = strings.TrimSpace(searchRegion[:dStart])
				endBlock = braceIdx + dEnd
			} else {
				candidate = strings.TrimSpace(searchRegion)
			}

			if !shouldRejectMidValueRepair(candidate) {
				fixed, _ := autoCloseJSONBraces(candidate)
				if tc, parsed := parseSingleToolCall(fixed); parsed {
					if acceptsToolCall(catalog, tc) {
						tcs = append(tcs, tc)
					}
					spans = append(spans, span{start: absIdx, end: endBlock})
					pos = endBlock
					recovered = true
				} else if tcHeur, okHeur := extractHeuristicToolCall(candidate); okHeur {
					if acceptsToolCall(catalog, tcHeur) {
						tcs = append(tcs, tcHeur)
					}
					spans = append(spans, span{start: absIdx, end: endBlock})
					pos = endBlock
					recovered = true
				}
			}
		}

		if recovered {
			continue
		}

		pos = braceIdx + 1
	}

	if len(spans) == 0 {
		return nil, text
	}

	var sb strings.Builder
	lastEnd := 0
	for _, sp := range spans {
		if sp.start > lastEnd {
			sb.WriteString(text[lastEnd:sp.start])
		}
		lastEnd = sp.end
	}
	if lastEnd < len(text) {
		sb.WriteString(text[lastEnd:])
	}

	return tcs, strings.TrimSpace(sb.String())
}

// acceptsToolCall reports whether tc conforms to the request's compiled tool
// catalog. A nil catalog means validation is disabled.
func acceptsToolCall(catalog *CompiledToolCatalog, tc ToolCall) bool {
	if catalog == nil {
		return true
	}
	ok, _ := catalog.ValidateCall(tc.Function.Name, tc.Function.Arguments)
	return ok
}

// ThrottleMutatingToolCalls enforces the Write-Serial cadence: every read-only
// call preceding the first mutation is preserved, exactly one mutating call is
// allowed, and the batch ends immediately after that mutation. Trailing calls
// are dropped so the host must execute the mutation and return its result
// before the model can request anything else (including a verification read).
func ThrottleMutatingToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return calls
	}
	out := make([]ToolCall, 0, len(calls))
	for _, tc := range calls {
		out = append(out, tc)
		if isMutatingTool(tc.Function.Name) {
			break
		}
	}
	return out
}

func parseSingleToolCall(payload string) (ToolCall, bool) {
	payload = trimToolCallFence(payload)
	// Undo Gemini's auto-linkified URLs so URL arguments stay intact.
	payload = deLinkifyAutoLinks(payload)

	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		// Normalize literal newlines/control characters and unescaped quotes inside JSON strings
		normalized := normalizeJSONStringLiterals(payload)
		if err2 := json.Unmarshal([]byte(normalized), &obj); err2 != nil {
			return ToolCall{}, false
		}
	}

	// Extract tool name
	name := ""
	if n, ok := obj["name"].(string); ok && n != "" {
		name = n
	} else if n, ok := obj["tool"].(string); ok && n != "" {
		name = n
	} else if n, ok := obj["function"].(string); ok && n != "" {
		name = n
	}

	if name == "" {
		return ToolCall{}, false
	}

	// Extract arguments. An explicitly supplied but malformed argument payload
	// is a hard parse failure: silently coercing it to "{}" would let a tool
	// call with missing required fields reach the host (e.g. a write without
	// its path or content).
	argsStr := ""
	argsInvalid := false
	if argsObj, exists := obj["arguments"]; exists {
		switch v := argsObj.(type) {
		case string:
			argsStr = v
		case nil:
			argsStr = ""
		default:
			bytes, err := json.Marshal(v)
			if err != nil {
				argsInvalid = true
			} else {
				argsStr = string(bytes)
			}
		}
	} else if paramsObj, exists := obj["parameters"]; exists {
		switch v := paramsObj.(type) {
		case string:
			argsStr = v
		case nil:
			argsStr = ""
		default:
			bytes, err := json.Marshal(v)
			if err != nil {
				argsInvalid = true
			} else {
				argsStr = string(bytes)
			}
		}
	}

	if argsInvalid {
		return ToolCall{}, false
	}

	// Fallback: models sometimes put the argument fields at the top level,
	// either instead of an "arguments"/"parameters" object
	// ({"name": "execute", "code": "..."}) or alongside an empty one.
	// Collect the remaining non-meta keys as the arguments object.
	if argsStr == "" || argsStr == "{}" {
		rest := map[string]any{}
		for k, v := range obj {
			switch k {
			case "name", "tool", "function", "id", "type", "arguments", "parameters":
				continue
			}
			rest[k] = v
		}
		if len(rest) > 0 {
			if bytes, err := json.Marshal(rest); err == nil {
				argsStr = string(bytes)
			}
		}
	}

	if argsStr == "" {
		argsStr = "{}"
	}

	// Ensure argsStr is valid JSON. Unlike the previous permissive fallback,
	// an invalid payload is rejected rather than replaced by "{}".
	var dummy any
	if err := json.Unmarshal([]byte(argsStr), &dummy); err != nil {
		return ToolCall{}, false
	}
	if dummy == nil {
		argsStr = "{}"
	}

	return ToolCall{
		ID:   "call_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: argsStr,
		},
	}, true
}

// normalizeJSONStringLiterals escapes unescaped control characters (ASCII 10, 13, 9)
// inside JSON string literals and escapes unescaped double quotes that appear inside strings.
func normalizeJSONStringLiterals(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + 64)
	inStr := false
	escaped := false
	n := len(s)
	for i := 0; i < n; i++ {
		c := s[i]
		if inStr {
			if escaped {
				escaped = false
				sb.WriteByte(c)
				continue
			}
			if c == '\\' {
				escaped = true
				sb.WriteByte(c)
				continue
			}
			if c == '"' {
				// Check whether this quote closes the string or is an unescaped inner quote.
				// In valid JSON, a closing quote must be followed by optional whitespace and then
				// one of: ',', '}', ']', ':', or EOF.
				j := i + 1
				for j < n && (s[j] == ' ' || s[j] == '\t' || s[j] == '\r' || s[j] == '\n') {
					j++
				}
				if j < n && s[j] != ',' && s[j] != '}' && s[j] != ']' && s[j] != ':' {
					// Unescaped quote inside string: escape it.
					sb.WriteString(`\"`)
					continue
				}
				inStr = false
				sb.WriteByte(c)
				continue
			}
			if c == '\n' {
				sb.WriteString(`\n`)
				continue
			}
			if c == '\r' {
				sb.WriteString(`\r`)
				continue
			}
			if c == '\t' {
				sb.WriteString(`\t`)
				continue
			}
			sb.WriteByte(c)
			continue
		}

		if c == '"' {
			inStr = true
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

func extractHeuristicToolCall(payload string) (ToolCall, bool) {
	nameMatch := heuristicNameRegex.FindStringSubmatch(payload)
	if len(nameMatch) < 2 {
		return ToolCall{}, false
	}
	name := strings.TrimSpace(nameMatch[1])
	if name == "" {
		return ToolCall{}, false
	}

	args := make(map[string]any)

	// Extract path if present
	if pathMatch := heuristicPathRegex.FindStringSubmatch(payload); len(pathMatch) >= 2 {
		args["path"] = pathMatch[1]
	}

	// Extract pattern / query if present
	if patMatch := heuristicPatternRegex.FindStringSubmatch(payload); len(patMatch) >= 2 {
		args["pattern"] = patMatch[1]
	}

	// Extract command if present
	for _, key := range []string{`"command"`, `"cmd"`} {
		if idx := strings.Index(payload, key); idx != -1 {
			colonIdx := strings.IndexByte(payload[idx:], ':')
			if colonIdx != -1 {
				valStart := idx + colonIdx + 1
				quoteStart := strings.IndexByte(payload[valStart:], '"')
				if quoteStart != -1 {
					cStart := valStart + quoteStart + 1
					rest := payload[cStart:]
					if endMatch := heuristicArgEndRegex.FindStringIndex(rest); len(endMatch) >= 2 {
						args["command"] = rest[:endMatch[0]]
						break
					}
				}
			}
		}
	}

	// Extract content / code / text if present
	for _, key := range []string{`"content"`, `"code"`, `"text"`, `"diff"`} {
		if idx := strings.Index(payload, key); idx != -1 {
			colonIdx := strings.IndexByte(payload[idx:], ':')
			if colonIdx != -1 {
				valStart := idx + colonIdx + 1
				quoteStart := strings.IndexByte(payload[valStart:], '"')
				if quoteStart != -1 {
					cStart := valStart + quoteStart + 1
					rest := payload[cStart:]
					if endMatch := heuristicArgEndRegex.FindStringIndex(rest); len(endMatch) >= 2 {
						args["content"] = rest[:endMatch[0]]
						break
					}
				}
			}
		}
	}

	argsBytes, err := json.Marshal(args)
	if err != nil {
		return ToolCall{}, false
	}

	return ToolCall{
		ID:   "call_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: string(argsBytes),
		},
	}, true
}

// findFirstJSONToolCall scans text for balanced {...} objects and returns the
// first that parses as a schema-valid tool call. Handles overlapping or
// concatenated JSON emissions (e.g. a truncated attempt immediately followed by
// the full one).
func findFirstJSONToolCall(text string, catalog *CompiledToolCatalog) (ToolCall, bool) {
	candidates := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		if candidates >= 50 {
			break
		}
		candidates++
		objStr, ok := balancedJSONObjectAt(text, i)
		if !ok {
			continue
		}
		if tc, ok := parseSingleToolCall(objStr); ok && acceptsToolCall(catalog, tc) {
			return tc, true
		}
	}

	// Fallback for truncated raw JSON cut off by disclaimer or EOF
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		sub := text[i:]
		if !strings.Contains(sub, `"name"`) && !strings.Contains(sub, `"tool"`) && !strings.Contains(sub, `"function"`) {
			continue
		}
		cand := sub
		if dStart, _ := findUpstreamDisclaimerOffset(sub); dStart != -1 {
			cand = sub[:dStart]
		}
		if shouldRejectMidValueRepair(cand) {
			continue
		}
		fixed, _ := autoCloseJSONBraces(cand)
		if tc, ok := parseSingleToolCall(fixed); ok && acceptsToolCall(catalog, tc) {
			return tc, true
		}
	}

	return ToolCall{}, false
}

// QuarantineUntaggedToolJSON removes every declared-tool JSON object emitted
// outside a <tool_call> envelope from cleanText. Mutating tools and invalid or
// truncated payloads are never promoted: they are stripped and reported via
// hasUntaggedMutation so the API layer retries the turn. Only a complete,
// schema-valid, read-only call is promoted, and only the first one is returned.
func QuarantineUntaggedToolJSON(cleanText string, catalog *CompiledToolCatalog) (sanitizedText string, hasUntaggedMutation bool, promotedCall *ToolCall) {
	sanitized, mutation, promoted := quarantineUntaggedToolJSON(cleanText, catalog)
	if len(promoted) == 0 {
		return sanitized, mutation, nil
	}
	call := promoted[0]
	return sanitized, mutation, &call
}

// quarantineUntaggedToolJSON is the multi-call form used by the arranger.
func quarantineUntaggedToolJSON(text string, catalog *CompiledToolCatalog) (string, bool, []ToolCall) {
	if catalog == nil || strings.TrimSpace(text) == "" {
		return text, false, nil
	}
	type span struct{ start, end int }
	var spans []span
	var promoted []ToolCall
	hasMutation := false
	candidates := 0

	for i := 0; i < len(text); {
		if text[i] != '{' {
			i++
			continue
		}
		if candidates >= 50 {
			break
		}
		candidates++

		if obj, ok := balancedJSONObjectAt(text, i); ok {
			// Inspect the whole object: a content-first emission can place
			// "name" after several hundred bytes of file content.
			if !strings.Contains(obj, `"name"`) &&
				!strings.Contains(obj, `"tool"`) &&
				!strings.Contains(obj, `"function"`) {
				i += len(obj)
				continue
			}
			name := untaggedToolName(obj)
			if name == "" {
				i += len(obj)
				continue
			}
			if _, declared := catalog.declared[name]; !declared {
				// Undeclared tool-shaped JSON is never executable output; keep
				// it out of the visible answer.
				start, end := expandJSONFenceSpan(text, i, i+len(obj))
				spans = append(spans, span{start, end})
				i += len(obj)
				continue
			}
			if isMutatingTool(name) {
				start, end := expandJSONFenceSpan(text, i, i+len(obj))
				spans = append(spans, span{start, end})
				hasMutation = true
				i += len(obj)
				continue
			}
			call, parsed := parseUntaggedToolCall(obj)
			if parsed && acceptsToolCall(catalog, call) {
				promoted = append(promoted, call)
			} else {
				// A declared read-only call with invalid arguments is still a
				// broken payload; quarantine and retry.
				hasMutation = true
			}
			start, end := expandJSONFenceSpan(text, i, i+len(obj))
			spans = append(spans, span{start, end})
			i += len(obj)
			continue
		}

		// Unbalanced/truncated object. A recovered declared name is enough to
		// classify it; a payload with mutation keys but no recoverable name is
		// treated as a mutation too. The whole tail is removed because a
		// truncated JSON string can absorb any following prose.
		windowEnd := i + 512
		if windowEnd > len(text) {
			windowEnd = len(text)
		}
		window := text[i:windowEnd]
		name := untaggedToolName(window)
		declared := false
		if name != "" {
			_, declared = catalog.declared[name]
		}
		if name != "" && !declared {
			spans = append(spans, span{i, len(text)})
			break
		}
		if name == "" && !isMutatingToolPayload(text[i:]) {
			i++
			continue
		}
		spans = append(spans, span{i, len(text)})
		hasMutation = true
		break
	}

	if len(spans) == 0 {
		return text, false, nil
	}

	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var sb strings.Builder
	last := 0
	for _, sp := range spans {
		if sp.start < last {
			if sp.end > last {
				last = sp.end
			}
			continue
		}
		sb.WriteString(text[last:sp.start])
		last = sp.end
	}
	if last < len(text) {
		sb.WriteString(text[last:])
	}
	return strings.TrimSpace(sb.String()), hasMutation, promoted
}

var (
	untaggedToolNameRegex       = regexp.MustCompile(`"(?:name|tool)"\s*:\s*"([^"\\\n]{1,128})"`)
	untaggedFunctionNameRegex   = regexp.MustCompile(`"function"\s*:\s*\{[^{}]{0,512}?"name"\s*:\s*"([^"\\\n]{1,128})"`)
	untaggedFunctionStringRegex = regexp.MustCompile(`"function"\s*:\s*"([^"\\\n]{1,128})"`)
)

func untaggedToolName(payload string) string {
	if match := untaggedToolNameRegex.FindStringSubmatch(payload); len(match) >= 2 {
		return strings.TrimSpace(match[1])
	}
	if match := untaggedFunctionNameRegex.FindStringSubmatch(payload); len(match) >= 2 {
		return strings.TrimSpace(match[1])
	}
	if match := untaggedFunctionStringRegex.FindStringSubmatch(payload); len(match) >= 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

// parseUntaggedToolCall accepts both the flat shape
// ({"name":"read","arguments":{...}}) and the OpenAI envelope
// ({"function":{"name":"read","arguments":{...}}}).
func parseUntaggedToolCall(obj string) (ToolCall, bool) {
	if call, ok := parseSingleToolCall(obj); ok {
		return call, true
	}
	var envelope struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if err := json.Unmarshal([]byte(obj), &envelope); err != nil || envelope.Function.Name == "" {
		return ToolCall{}, false
	}
	args := "{}"
	if len(envelope.Function.Arguments) > 0 {
		args = string(envelope.Function.Arguments)
	}
	return ToolCall{
		ID:   "call_" + strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		Type: "function",
		Function: ToolCallFunction{
			Name:      envelope.Function.Name,
			Arguments: args,
		},
	}, true
}

// expandJSONFenceSpan widens a JSON object span to the surrounding Markdown
// fence when the fence contains nothing else, so quarantine does not leave an
// empty ```json block in the visible answer.
func expandJSONFenceSpan(text string, start, end int) (int, int) {
	open := strings.LastIndex(text[:start], "```")
	if open == -1 {
		return start, end
	}
	lineEnd := strings.IndexByte(text[open:], '\n')
	if lineEnd == -1 {
		return start, end
	}
	bodyStart := open + lineEnd + 1
	if strings.TrimSpace(text[bodyStart:start]) != "" {
		return start, end
	}
	closeRel := strings.Index(text[end:], "```")
	if closeRel == -1 {
		return start, end
	}
	closeStart := end + closeRel
	if strings.TrimSpace(text[end:closeStart]) != "" {
		return start, end
	}
	newEnd := closeStart + 3
	if newEnd < len(text) && text[newEnd] == '\n' {
		newEnd++
	}
	return open, newEnd
}

// findUpstreamDisclaimerOffset returns the start and end offsets of any upstream
// refusal/disclaimer encountered in s, or -1, -1 if none found.
//
// The regexes run directly on the original bytes: replacing typographic
// apostrophes (U+2019, 3 bytes) with ASCII apostrophes (1 byte) before
// indexing would shift every offset inside French text and slice into valid
// JSON when the caller later does s[bestIdx:].
func findUpstreamDisclaimerOffset(s string) (int, int) {
	bestIdx := -1
	for _, re := range upstreamDisclaimerPatterns {
		loc := re.FindStringIndex(s)
		if loc == nil {
			continue
		}
		if bestIdx == -1 || loc[0] < bestIdx {
			bestIdx = loc[0]
		}
	}

	if bestIdx == -1 {
		return -1, -1
	}

	nextTag := strings.Index(s[bestIdx:], "<tool_call>")
	if nextTag != -1 {
		return bestIdx, bestIdx + nextTag
	}
	return bestIdx, len(s)
}

// autoCloseJSONBraces attempts to close unclosed JSON structures (strings, objects, arrays)
// caused by stream truncation or upstream disclaimer cutoff.
//
// The second return value reports whether the cutoff happened mid-value:
// inside a JSON string (inStr) or immediately after a dangling ':' or ','.
// Such a repair may be syntactically valid but semantically incomplete, so
// callers must not apply it to mutating tools (write/edit/shell).
func autoCloseJSONBraces(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return s, false
	}

	var stack []byte
	inStr := false
	escaped := false
	n := len(s)

	for i := 0; i < n; i++ {
		c := s[i]
		if inStr {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}

		if c == '"' {
			inStr = true
			continue
		}
		switch c {
		case '{', '[':
			stack = append(stack, c)
		case '}':
			if len(stack) > 0 && stack[len(stack)-1] == '{' {
				stack = stack[:len(stack)-1]
			}
		case ']':
			if len(stack) > 0 && stack[len(stack)-1] == '[' {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// A cutoff inside a string, or right after a pending key/value separator,
	// means at least one value is incomplete even if the repaired text parses.
	isMidValue := inStr
	trimmedInput := strings.TrimRight(s, " \t\r\n")
	if strings.HasSuffix(trimmedInput, ":") || strings.HasSuffix(trimmedInput, ",") {
		isMidValue = true
	}

	var closer strings.Builder
	if inStr {
		if escaped && len(s) > 0 && s[len(s)-1] == '\\' {
			s = s[:len(s)-1]
		}
		closer.WriteByte('"')
	}

	trimmed := s + closer.String()
	closer.Reset()

	t := strings.TrimRight(trimmed, " \t\r\n")
	if strings.HasSuffix(t, ":") {
		closer.WriteString(`""`)
	} else if strings.HasSuffix(t, ",") {
		t = strings.TrimRight(t[:len(t)-1], " \t\r\n")
		trimmed = t
	}

	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			closer.WriteByte('}')
		case '[':
			closer.WriteByte(']')
		}
	}

	return trimmed + closer.String(), isMidValue
}

// HasTruncatedMutationToolCall reports whether raw contains at least one
// file-mutating tool call that was cut off mid-value. The API layer uses this
// to retry the turn instead of silently returning a stop response, while the
// arranger refuses to repair such calls so no partial write can be executed.
func HasTruncatedMutationToolCall(raw string) bool {
	if hasTruncatedMutationTagged(raw) {
		return true
	}
	return hasTruncatedMutationRawJSON(raw)
}

func hasTruncatedMutationTagged(raw string) bool {
	const tag = "<tool_call>"
	pos := 0
	for pos < len(raw) {
		idx := strings.Index(raw[pos:], tag)
		if idx == -1 {
			return false
		}
		absIdx := pos + idx
		braceOffset := strings.IndexByte(raw[absIdx+len(tag):], '{')
		if braceOffset == -1 {
			pos = absIdx + len(tag)
			continue
		}
		braceIdx := absIdx + len(tag) + braceOffset

		// A complete, syntactically valid object at braceIdx is never a
		// truncation, even when its string values contain tag-like text
		// (`<tool_call>`, `</tool_call>`) or a disclaimer phrase. Check this
		// before searching for closing/next tags: those searches can match a
		// tag inside the current JSON string and produce a false truncation.
		if objStr, ok := balancedJSONObjectAt(raw, braceIdx); ok && json.Valid([]byte(objStr)) {
			pos = braceIdx + len(objStr)
			continue
		}

		limit := len(raw)
		if closeIdx := strings.Index(raw[braceIdx:], "</tool_call>"); closeIdx != -1 {
			limit = braceIdx + closeIdx
		}
		// Look for a following call only after the current object's opening
		// brace. Searching before it can match a `<tool_call>` mention inside
		// the preamble/reasoning and produce limit < braceIdx (slice panic).
		if nextTagIdx := strings.Index(raw[braceIdx:], tag); nextTagIdx != -1 {
			nextAbs := braceIdx + nextTagIdx
			if nextAbs < limit {
				limit = nextAbs
			}
		}
		if limit < braceIdx {
			limit = braceIdx
		}

		candidate := strings.TrimSpace(raw[braceIdx:limit])
		// A complete, syntactically valid object is not a truncation, even if
		// it is followed by a stray comma or fence before the closing tag, or
		// if its content legitimately quotes a disclaimer phrase.
		if objStr, ok := balancedJSONObjectAt(candidate, 0); ok && json.Valid([]byte(objStr)) {
			pos = limit
			continue
		}
		if dStart, _ := findUpstreamDisclaimerOffset(candidate); dStart != -1 {
			candidate = strings.TrimSpace(candidate[:dStart])
		}
		if shouldRejectMidValueRepair(candidate) {
			return true
		}

		if limit <= braceIdx {
			pos = braceIdx + 1
		} else {
			pos = limit
		}
	}
	return false
}

// hasTruncatedMutationRawJSON covers models that emit a bare JSON tool call
// without <tool_call> tags (the same fallback shape handled by
// findFirstJSONToolCall).
func hasTruncatedMutationRawJSON(raw string) bool {
	candidates := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] != '{' {
			continue
		}
		if candidates >= 50 {
			break
		}
		candidates++
		sub := raw[i:]
		if !strings.Contains(sub, `"name"`) && !strings.Contains(sub, `"tool"`) && !strings.Contains(sub, `"function"`) {
			continue
		}
		// A complete object at this position is not a truncation, even when
		// its content quotes a disclaimer phrase.
		if objStr, ok := balancedJSONObjectAt(sub, 0); ok && json.Valid([]byte(objStr)) {
			continue
		}
		cand := sub
		if dStart, _ := findUpstreamDisclaimerOffset(sub); dStart != -1 {
			cand = sub[:dStart]
		}
		if shouldRejectMidValueRepair(cand) {
			return true
		}
	}
	return false
}

// balancedJSONObjectAt returns the substring starting at s[start] ('{') up to
// and including its balanced closing '}', honouring JSON string escapes.
func balancedJSONObjectAt(s string, start int) (string, bool) {
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

var (
	codeFenceWithQueryRegex = regexp.MustCompile(`(?m)^` + "```" + `([a-zA-Z0-9_-]+)\?[^\n]*$`)
	multiNewlineRegex       = regexp.MustCompile(`\n{3,}`)
	toolsClosedRegex        = regexp.MustCompile(`(?s)<tools>[\s\S]*?</tools>`)
	toolsUnclosedRegex      = regexp.MustCompile(`(?s)<tools>[\s\S]*`)
	schemaEchoRegex         = regexp.MustCompile(`(?s)(?:^|\n)\s*\[\s*\{\s*"(?:function|type)"\s*:\s*[\s\S]*`)
	toolResultOutputRegex   = regexp.MustCompile(`(?s)<output>[\s\S]*?</output>`)
	subagentTagRegex        = regexp.MustCompile(`(?s)<subagent\b[^>]*/>|<subagent\b[^>]*>[\s\S]*?</subagent>`)
	upstreamDisclaimerRegex = regexp.MustCompile(`(?i)(?:^|\n|\.\s*)\s*(?:I'm just a language model|I am just a language model|I'm a language model|I am a language model|can't help with that|cannot help with that|I can't assist with that|I cannot assist with that|I'm unable to assist with that|I am unable to assist with that|I'm unable to help with that|I am unable to help with that|As an AI language model|As a large language model|I apologize, but I (?:can't|cannot)|I'm sorry, but I (?:can't|cannot))[\s\S]*`)

	scriptOpenTagRegex      = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	scriptCloseTagRegex     = regexp.MustCompile(`(?i)</script\s*>`)
	toolCallOpenTagRegex    = regexp.MustCompile(`(?i)<tool_call\b[^>]*>`)
	toolCallCloseTagRegex   = regexp.MustCompile(`(?i)</tool_call\s*>`)
	toolsOpenTagRegex       = regexp.MustCompile(`(?i)<tools\b[^>]*>`)
	toolsCloseTagRegex      = regexp.MustCompile(`(?i)</tools\s*>`)
	whitespaceOnlyLineRegex = regexp.MustCompile(`(?m)^[ \t]+$`)
)

// sanitizeGeminiContentWithRepairs cleans visible model text and records
// whether repeated orphan closing tags (an autoregressive surface-tag leak)
// were removed.
func sanitizeGeminiContentWithRepairs(res *ArrangedOutput, text string) string {
	cleaned, removedOrphanTags := stripRepeatedOrphanClosingTags(text)
	if removedOrphanTags && res != nil {
		res.Repairs = append(res.Repairs, "orphan_tags")
	}
	return sanitizeGeminiContent(cleaned)
}

// stripRepeatedOrphanClosingTags removes repeated closing tags that have no
// matching opening tag. Gemini occasionally degenerates into a loop of
// `</script> </tool_call>` (or similar) tokens on long generations. A single
// unmatched closing tag is left untouched so legitimate code/documentation
// snippets are preserved; two or more unmatched closes are treated as a leak.
func stripRepeatedOrphanClosingTags(text string) (string, bool) {
	cleaned := text
	removedAny := false
	pairs := []struct {
		open  *regexp.Regexp
		close *regexp.Regexp
	}{
		{scriptOpenTagRegex, scriptCloseTagRegex},
		{toolCallOpenTagRegex, toolCallCloseTagRegex},
		{toolsOpenTagRegex, toolsCloseTagRegex},
	}
	for _, pair := range pairs {
		opens := len(pair.open.FindAllStringIndex(cleaned, -1))
		closes := len(pair.close.FindAllStringIndex(cleaned, -1))
		if closes < 2 || closes <= opens {
			continue
		}
		keep := opens
		cleaned = pair.close.ReplaceAllStringFunc(cleaned, func(match string) string {
			if keep > 0 {
				keep--
				return match
			}
			removedAny = true
			return ""
		})
	}
	if !removedAny {
		return text, false
	}
	// A leaked tag sequence often occupies its own line. Remove the
	// whitespace-only lines and excess blank lines left behind.
	cleaned = whitespaceOnlyLineRegex.ReplaceAllString(cleaned, "")
	cleaned = multiNewlineRegex.ReplaceAllString(cleaned, "\n\n")
	return cleaned, true
}

func sanitizeGeminiContent(text string) string {
	cleaned := text
	// Remove null bytes
	cleaned = strings.ReplaceAll(cleaned, "\x00", "")
	// Clean Google Python Sandbox internal query strings: ```python?code_reference&code_event_index=1 -> ```python
	cleaned = codeFenceWithQueryRegex.ReplaceAllString(cleaned, "```$1")
	// Clean prompt leakage, tool declarations, schema echoes, and simulated XML tags
	cleaned = toolsClosedRegex.ReplaceAllString(cleaned, "")
	cleaned = toolsUnclosedRegex.ReplaceAllString(cleaned, "")
	cleaned = schemaEchoRegex.ReplaceAllString(cleaned, "")
	cleaned = toolResultOutputRegex.ReplaceAllString(cleaned, "")
	cleaned = subagentTagRegex.ReplaceAllString(cleaned, "")
	// Clean upstream disclaimers / refusals
	cleaned = upstreamDisclaimerRegex.ReplaceAllString(cleaned, "")
	// A thought tag must never be visible to the host, even on an edge path
	// where extraction did not see a matching opening tag.
	cleaned = thoughtTagRegex.ReplaceAllString(cleaned, "")
	// Normalize excessive newlines
	cleaned = multiNewlineRegex.ReplaceAllString(cleaned, "\n\n")
	cleaned = strings.TrimSpace(cleaned)
	return cleaned
}

// earliestThoughtTag returns the index and value of the earliest tag from tags
// found in s, or (-1, "") when none is present.
func earliestThoughtTag(s string, tags []string) (int, string) {
	bestIdx := -1
	bestTag := ""
	for _, tag := range tags {
		idx := strings.Index(s, tag)
		if idx == -1 {
			continue
		}
		if bestIdx == -1 || idx < bestIdx {
			bestIdx = idx
			bestTag = tag
		}
	}
	return bestIdx, bestTag
}

// trimPartialThoughtClose removes a trailing partial closing tag (for example
// "</" or "</th") from an unclosed thought that was cut off at end of stream.
func trimPartialThoughtClose(s string) string {
	for _, closeTag := range thoughtCloseTags {
		// Start at the longest partial tag (without the final '>') and stop
		// before the bare "<", which may legitimately end a thought.
		for i := len(closeTag) - 1; i > 1; i-- {
			partial := closeTag[:i]
			if strings.HasSuffix(s, partial) {
				return s[:len(s)-len(partial)]
			}
		}
	}
	return s
}

// extractThoughtsAndContent separates Gemini's private reasoning from the
// assistant-visible content. It is the single source of truth used by the
// arranger and by the live-stream accessors.
//
// A thought block ends at:
//   - its matching </thought> or </thinking> closing tag, when present. A
//     closed thought is wholly private, even when its reasoning quotes a
//     literal <tool_call> tag;
//   - otherwise, a <tool_call> tag, which the model emits directly when it
//     switches to an action without closing its thought (the tool call is
//     preserved verbatim in cleanedContent);
//   - otherwise, end of stream, in which case any trailing partial closing
//     tag is removed.
//
// Nested/orphan opening tags are stripped from the reasoning text. The returned
// cleanedContent never contains a thought tag or text from an unclosed thought.
func extractThoughtsAndContent(raw string) (cleanedContent string, reasoningContent string) {
	if !strings.Contains(raw, "<thought>") && !strings.Contains(raw, "<thinking>") {
		return thoughtTagRegex.ReplaceAllString(raw, ""), ""
	}

	var cleaned strings.Builder
	var thoughts []string
	remaining := raw
	for {
		start, openTag := earliestThoughtTag(remaining, thoughtOpenTags)
		if start == -1 {
			cleaned.WriteString(remaining)
			break
		}
		cleaned.WriteString(remaining[:start])

		// Walk the inside of this thought. An explicit closing tag has
		// priority: the block is reasoning up to that tag, even when it quotes
		// a literal <tool_call>. Nested opening tags increase the depth and are
		// stripped from the reasoning text.
		pos := start + len(openTag)
		var segment string
		if closeIdx, _ := earliestThoughtTag(remaining[pos:], thoughtCloseTags); closeIdx != -1 {
			depth := 1
			scan := pos
			end := len(remaining)
			closingTag := ""
			for {
				nestedIdx, nestedTag := earliestThoughtTag(remaining[scan:], thoughtOpenTags)
				if nestedIdx != -1 {
					nestedIdx += scan
				}
				closeAt, closeTag := earliestThoughtTag(remaining[scan:], thoughtCloseTags)
				if closeAt != -1 {
					closeAt += scan
				}
				if nestedIdx == -1 && closeAt == -1 {
					break
				}
				if nestedIdx != -1 && (closeAt == -1 || nestedIdx < closeAt) {
					depth++
					scan = nestedIdx + len(nestedTag)
					continue
				}
				end = closeAt
				scan = closeAt + len(closeTag)
				depth--
				if depth == 0 {
					closingTag = closeTag
					break
				}
			}
			segment = remaining[pos:end]
			pos = end
			if depth == 0 {
				pos += len(closingTag)
			}
		} else {
			// Unclosed thought: it ends at a <tool_call> switch (the call is
			// preserved verbatim in cleanedContent) or at end of stream.
			end := len(remaining)
			if toolIdx := strings.Index(remaining[pos:], "<tool_call>"); toolIdx != -1 {
				end = pos + toolIdx
			}
			segment = trimPartialThoughtClose(remaining[pos:end])
			pos = end
		}
		segment = strings.TrimSpace(thoughtTagRegex.ReplaceAllString(segment, ""))
		if segment != "" {
			thoughts = append(thoughts, segment)
		}
		if pos > len(remaining) {
			pos = len(remaining)
		}
		remaining = remaining[pos:]
	}

	// Belt-and-braces: a stray tag must never reach the host as visible text.
	cleanedContent = thoughtTagRegex.ReplaceAllString(cleaned.String(), "")
	return cleanedContent, strings.Join(thoughts, "\n\n")
}

// ExtractAvailableThoughts extracts currently generated thoughts from raw stream text,
// including partially generated/unclosed <thought> or <thinking> blocks.
func ExtractAvailableThoughts(raw string) string {
	_, reasoning := extractThoughtsAndContent(raw)
	return reasoning
}

// ExtractAvailableContent extracts content outside thoughts from raw stream text.
// Unlike a matched-pair regex, it also stops an unclosed thought at a
// <tool_call> switch so the call itself remains available to the parser.
func ExtractAvailableContent(raw string) string {
	cleaned, _ := extractThoughtsAndContent(raw)
	return cleaned
}
