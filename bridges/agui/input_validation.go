package agui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	schema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Tool declares a frontend tool. The bridge diagnoses and ignores declarations.
type Tool struct {
	// Name identifies the tool to the model.
	Name string `json:"name"`
	// Description explains the tool's purpose.
	Description string `json:"description"`
	// Parameters preserves the optional, opaque parameter schema.
	Parameters json.RawMessage `json:"parameters,omitempty"`
	// Metadata carries application-defined tool information; decoded numbers use json.Number.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Context is ambient information appended to the prompt in input order.
type Context struct {
	// Description identifies the context's purpose.
	Description string `json:"description"`
	// Value is the context supplied to the model.
	Value string `json:"value"`
}

// FunctionCall retains arguments exactly as the protocol string.
type FunctionCall struct {
	// Name identifies the function being called.
	Name string `json:"name"`
	// Arguments preserves the arguments without requiring valid JSON within them.
	Arguments string `json:"arguments"`
}

// ToolCall is an assistant's historical function invocation.
type ToolCall struct {
	// ID correlates the call with its tool result.
	ID string `json:"id"`
	// Type is the protocol discriminator, currently "function".
	Type string `json:"type"`
	// Function describes the invocation.
	Function FunctionCall `json:"function"`
	// EncryptedValue preserves an opaque provider artefact.
	EncryptedValue string `json:"encryptedValue,omitempty"`
	// Metadata carries application-defined call information; decoded numbers use json.Number.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ResumeEntry answers an interrupt; non-empty resume is unsupported.
type ResumeEntry struct {
	// InterruptID identifies the interrupt being answered.
	InterruptID string `json:"interruptId"`
	// Status is "resolved" or "cancelled".
	Status string `json:"status"`
	// Payload preserves the opaque answer value.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Metadata carries application-defined response information; decoded numbers use json.Number.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Diagnostic describes an input member stripped or degraded during projection.
type Diagnostic struct {
	// Code is the stable, machine-readable warning code.
	Code string `json:"code"`
	// Message describes the degradation for a person.
	Message string `json:"message"`
	// Path is the original input's JSON Pointer; empty identifies the root.
	Path string `json:"path"`
}

// InputError is a stable transport validation failure, before any run starts.
type InputError struct {
	// Code is the stable, machine-readable failure code.
	Code string `json:"code"`
	// Message describes the failure and any available remedy.
	Message string `json:"message"`
	// Path is a JSON Pointer to the invalid value; empty identifies the root.
	Path string `json:"path"`
	// Status is the HTTP error status; zero selects the handler's default 400.
	Status int `json:"-"`
}

// Error formats the validation failure for logs and error chains.
func (e *InputError) Error() string {
	return e.Code + ": " + e.Message + " (" + e.Path + ")"
}

func inputError(code, path, msg string) error {
	return &InputError{Code: code, Path: path, Message: msg, Status: 400}
}

//go:embed testdata/ag-ui-schema-1.0.json
var protocolSchema []byte

var inputSchema, schemaDefs = compileInputSchema()

var versionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)$`)

func compileInputSchema() (*schema.Schema, map[string]any) {
	var doc map[string]any
	if err := json.Unmarshal(protocolSchema, &doc); err != nil {
		panic(err)
	}
	defs := doc["$defs"].(map[string]any)
	// Relax only identity presence for legacy requests. Versioned requests
	// restore that requirement after schema validation in validateInputVersion.
	defs["RunAgentInput"].(map[string]any)["required"] = []any{"messages"}

	doc["$ref"] = "#/$defs/RunAgentInput"
	compiler := schema.NewCompiler()
	if err := compiler.AddResource("agui.json", doc); err != nil {
		panic(err)
	}
	compiled, err := compiler.Compile("agui.json")
	if err != nil {
		panic(err)
	}
	return compiled, defs
}

func decodeInput(body []byte, in *RunAgentInput) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || !json.Valid(body) {
		return inputError("agui.invalid_json", "", "expected one JSON object")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return inputError("agui.invalid_input", "", "expected an object")
	}

	in.UnknownMembers = map[string]json.RawMessage{}
	in.partDiagnostics = []Diagnostic{}
	stripUnknown(value, schemaDefs["RunAgentInput"].(map[string]any), "", in)
	if err := inputSchema.Validate(value); err != nil {
		return inputError("agui.invalid_input", validationPath(err), "known member violates AG-UI 1.0 schema")
	}

	cleaned, _ := json.Marshal(value)
	type plain RunAgentInput
	var decoded plain
	cleanDecoder := json.NewDecoder(bytes.NewReader(cleaned))
	cleanDecoder.UseNumber() // Opaque metadata must not round or overflow JSON numbers.
	if err := cleanDecoder.Decode(&decoded); err != nil {
		return inputError("agui.invalid_input", "", err.Error())
	}
	decoded.UnknownMembers = in.UnknownMembers
	decoded.Diagnostics = in.Diagnostics
	decoded.partDiagnostics = in.partDiagnostics
	*in = RunAgentInput(decoded)

	// RawMessage must preserve the original forwarded value, including whitespace.
	var original map[string]json.RawMessage
	_ = json.Unmarshal(body, &original)
	in.ForwardedProps = original["forwardedProps"]
	_, versioned := object["protocolVersion"]
	return validateInputVersion(in, versioned)
}

func validationPath(err error) string {
	var invalid *schema.ValidationError
	if !errors.As(err, &invalid) {
		return ""
	}
	// Prefer a deepest location, with deterministic first-branch ties.
	path, depth := "", -1
	var visit func(*schema.ValidationError)
	visit = func(current *schema.ValidationError) {
		if len(current.InstanceLocation) > depth {
			depth = len(current.InstanceLocation)
			path = ""
			for _, part := range current.InstanceLocation {
				path = pointerChild(path, part)
			}
		}
		for _, cause := range current.Causes {
			visit(cause)
		}
	}
	visit(invalid)
	return path
}

func validateInputVersion(in *RunAgentInput, versioned bool) error {
	if !versioned {
		if in.ThreadID == "" || in.RunID == "" {
			in.warn("agui.legacy_identity", "", "legacy input omitted public identity")
		}
		return nil
	}
	match := versionPattern.FindStringSubmatch(in.ProtocolVersion)
	if match == nil || strings.TrimLeft(match[1], "0") != "1" {
		return inputError("agui.unsupported_version", "/protocolVersion", "expected supported MAJOR.MINOR (1.x)")
	}
	if in.ThreadID == "" || in.RunID == "" {
		return inputError("agui.identity_required", "", "versioned input requires threadId and runId")
	}
	if strings.TrimLeft(match[2], "0") != "" {
		in.warn("agui.newer_minor", "/protocolVersion", "serving newer 1.x input as 1.0")
	}
	return nil
}

func (in *RunAgentInput) warn(code, path, msg string) {
	in.Diagnostics = append(in.Diagnostics, Diagnostic{Code: code, Path: path, Message: msg})
}

func pointerChild(path, key string) string {
	return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

// stripUnknown follows only schema-defined shapes. Opaque payloads and open
// metadata objects have no declared properties and are deliberately left alone.
// Returning the cleaned value lets array parents remove unsupported parts while
// diagnostics continue to refer to indices in the original input.
func stripUnknown(value any, definition map[string]any, path string, in *RunAgentInput) any {
	if ref, ok := definition["$ref"].(string); ok {
		if target, ok := schemaDefsLookup(ref); ok {
			return stripUnknown(value, target, path, in)
		}
		return value
	}
	if choices, ok := definition["oneOf"].([]any); ok {
		if variant := schemaVariant(value, choices); variant != nil {
			return stripUnknown(value, variant, path, in)
		}
		return value
	}
	if array, ok := value.([]any); ok {
		return stripUnknownArray(array, definition, path, in)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	properties := collectProperties(definition)
	if len(properties) == 0 {
		return value
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		memberPath := pointerChild(path, key)
		field, known := properties[key]
		if !known {
			raw, _ := json.Marshal(object[key])
			in.UnknownMembers[memberPath] = raw
			in.warn("agui.unknown_member", memberPath, "unrecognised member stripped")
			delete(object, key)
		} else if child, ok := field.(map[string]any); ok {
			object[key] = stripUnknown(object[key], child, memberPath, in)
		}
	}
	return value
}

func schemaVariant(value any, choices []any) map[string]any {
	for _, choice := range choices {
		candidate := choice.(map[string]any)
		if ref, ok := candidate["$ref"].(string); ok {
			candidate, _ = schemaDefsLookup(ref)
		}
		if object, ok := value.(map[string]any); ok {
			properties := collectProperties(candidate)
			for _, key := range []string{"role", "type", "kind"} {
				if field, ok := properties[key].(map[string]any); ok {
					if discriminator, ok := field["const"]; ok && object[key] == discriminator {
						return candidate
					}
				}
			}
		} else if candidate["type"] == "array" {
			if _, ok := value.([]any); ok {
				return candidate
			}
		}
	}
	return nil
}

func stripUnknownArray(array []any, definition map[string]any, path string, in *RunAgentInput) any {
	item, ok := definition["items"].(map[string]any)
	if !ok {
		return array
	}
	cleaned := make([]any, 0, len(array))
	for index, value := range array {
		partPath := pointerChild(path, strconv.Itoa(index))
		if item["$ref"] == "#/$defs/ContentPart" {
			if part, ok := value.(map[string]any); ok {
				if kind, ok := part["type"].(string); ok && kind != "text" {
					in.partDiagnostics = append(in.partDiagnostics, Diagnostic{
						Code: "agui.content_part_skipped", Path: partPath,
						Message: "non-text content part skipped",
					})
					switch kind {
					case "image", "audio", "video", "document":
						// Known media still needs schema validation, even though
						// its model-facing projection is unsupported.
					default:
						raw, _ := json.Marshal(part)
						in.UnknownMembers[partPath] = raw
						continue
					}
				}
			}
		}
		cleaned = append(cleaned, stripUnknown(value, item, partPath, in))
	}
	return cleaned
}

func schemaDefsLookup(ref string) (map[string]any, bool) {
	value, ok := schemaDefs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
	return value, ok
}

func collectProperties(definition map[string]any) map[string]any {
	properties := map[string]any{}
	if declared, ok := definition["properties"].(map[string]any); ok {
		for key, value := range declared {
			properties[key] = value
		}
	}
	if all, ok := definition["allOf"].([]any); ok {
		for _, entry := range all {
			child := entry.(map[string]any)
			if ref, ok := child["$ref"].(string); ok {
				child, _ = schemaDefsLookup(ref)
			}
			for key, value := range collectProperties(child) {
				// A variant's literal discriminator takes precedence over
				// the base message's unconstrained role/type property.
				if _, exists := properties[key]; !exists {
					properties[key] = value
				}
			}
		}
	}
	return properties
}

// InputOptions controls the provider-neutral text projection.
type InputOptions struct {
	// DisableContext strips ambient context instead of appending it to the prompt.
	DisableContext bool
	// HistoryFallback rehydrates prior user/assistant text and tool-call summaries
	// when no usable checkpoint exists. The default rejects such history.
	HistoryFallback bool
}

// InputState describes the host's per-request conversation continuity check.
type InputState struct {
	// CheckpointUsable means the host can safely resume the prior conversation.
	// Leave false when checkpoint availability or compatibility is unknown.
	CheckpointUsable bool
}

// PrepareInput selects only the trailing user text. The host/session layer
// verifies history and supplies checkpoint availability through state.
func PrepareInput(in *RunAgentInput, opts InputOptions, state InputState) (string, []Diagnostic, error) {
	prompt, err := executableText(in)
	if err != nil {
		return "", nil, err
	}
	count := len(in.Messages)

	warnings := append([]Diagnostic(nil), in.Diagnostics...)
	warn := func(code, path, message string) {
		warnings = append(warnings, Diagnostic{Code: code, Path: path, Message: message})
	}
	rehydrate := count > 1 && !state.CheckpointUsable && opts.HistoryFallback
	if count > 1 && !state.CheckpointUsable && !opts.HistoryFallback {
		return "", nil, &InputError{
			Code: "agui.history_unrecoverable", Path: "/messages", Status: 409,
			Message: "prior history has no usable checkpoint; configure a ThreadStore " +
				"and a resumable Thread, or enable InputOptions.HistoryFallback for text rehydration",
		}
	}
	warnings = append(warnings, inputPartDiagnostics(in, rehydrate)...)
	if rehydrate {
		prompt = rehydrateHistory(in.Messages[:count-1]) + "\n\n" + prompt
		warn("agui.history_rehydrated", "/messages", "prior text and tool call summaries rehydrated")
	}
	if len(in.Tools) > 0 {
		warn("agui.tools_unsupported", "/tools", "frontend tools stripped")
	}
	if !emptyState(in.State) {
		warn("agui.state_unsupported", "/state", "shared state stripped")
	}
	if len(in.Context) > 0 {
		if opts.DisableContext {
			warn("agui.context_disabled", "/context", "context stripped by host option")
		} else {
			raw, _ := json.Marshal(in.Context)
			prompt += "\n\n<agent-adaptor-context>\n" + string(raw) + "\n</agent-adaptor-context>"
		}
	}
	return prompt, warnings, nil
}

func rehydrateHistory(messages []Message) string {
	history := []map[string]any{}
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		entry := map[string]any{"role": message.Role, "text": contentAsString(message.Content)}
		if len(message.ToolCalls) > 0 {
			entry["toolCalls"] = message.ToolCalls
		}
		history = append(history, entry)
	}
	raw, _ := json.Marshal(history)
	return "<agent-adaptor-history>\n" + string(raw) + "\n</agent-adaptor-history>"
}

func inputPartDiagnostics(in *RunAgentInput, rehydrate bool) []Diagnostic {
	var warnings []Diagnostic
	for index, message := range in.Messages {
		if index != len(in.Messages)-1 && (!rehydrate || (message.Role != "user" && message.Role != "assistant")) {
			continue
		}
		prefix := fmt.Sprintf("/messages/%d/content/", index)
		if in.partDiagnostics != nil {
			// Decoded inputs retain original indices after unknown parts were
			// removed. Re-scanning their cleaned content would shift the paths.
			for _, diagnostic := range in.partDiagnostics {
				if strings.HasPrefix(diagnostic.Path, prefix) {
					warnings = append(warnings, diagnostic)
				}
			}
			continue
		}
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			continue
		}
		for partIndex, part := range parts {
			if part.Type != "text" {
				warnings = append(warnings, Diagnostic{
					Code: "agui.content_part_skipped", Path: prefix + strconv.Itoa(partIndex),
					Message: "non-text content part skipped",
				})
			}
		}
	}
	return warnings
}

func emptyState(raw json.RawMessage) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		return len(value) == 0
	case []any:
		return len(value) == 0
	case string:
		return value == ""
	default:
		return false
	}
}

// executableText checks execution policy before host callbacks or checkpoint I/O.
func executableText(in *RunAgentInput) (string, error) {
	if in == nil {
		return "", inputError("agui.invalid_input", "", "nil input")
	}
	if len(in.Resume) > 0 {
		return "", inputError("agui.resume_unsupported", "/resume", "interrupt resume is unsupported")
	}
	count := len(in.Messages)
	if count == 0 || in.Messages[count-1].Role != "user" {
		return "", inputError("agui.trailing_user_required", "/messages",
			"the trailing message must be user; frontend tool results are unsupported")
	}
	seen := make(map[string]bool, count)
	for i, message := range in.Messages {
		if seen[message.ID] {
			return "", &InputError{Code: "agui.duplicate_message", Status: 409,
				Path: fmt.Sprintf("/messages/%d/id", i), Message: "message ID already appears in this request"}
		}
		seen[message.ID] = true
	}
	prompt := contentAsString(in.Messages[count-1].Content)
	if strings.TrimSpace(prompt) == "" {
		return "", inputError("agui.no_executable_text", "/messages", "trailing user has no executable text")
	}

	return prompt, nil
}
