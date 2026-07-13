package agentadaptor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"

	invopopjsonschema "github.com/invopop/jsonschema"
	santhoshjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// StructuredOutputOption customizes a JSON Schema structured-output request.
type StructuredOutputOption func(*OutputSchema)

// SchemaOption customizes JSONSchemaFor. The SDK owns these options so the
// public API does not expose the selected schema generator implementation.
type SchemaOption func(*schemaOptions)

type schemaOptions struct {
	inlineRefs                bool
	allowAdditionalProperties bool
	requireExplicitTags       bool
	goCommentsBase            string
	goCommentsPath            string
}

// NativeStrictOutput requires provider-native JSON Schema enforcement.
func NativeStrictOutput() StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.Mode = StructuredOutputNativeStrict
	}
}

// PreferNativeOutput uses native JSON Schema enforcement when the adapter can
// honor it, otherwise the explicit prompt+validation fallback when supported.
func PreferNativeOutput() StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.Mode = StructuredOutputPreferNative
	}
}

// PromptValidateOutput requests explicit prompt instructions plus SDK-side
// exact-JSON validation. It is weaker than provider-native enforcement.
func PromptValidateOutput() StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.Mode = StructuredOutputPromptValidate
	}
}

// StructuredOutputName sets a provider-facing schema name when supported.
func StructuredOutputName(name string) StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.Name = strings.TrimSpace(name)
	}
}

// StructuredOutputDescription sets a provider-facing schema description when
// supported.
func StructuredOutputDescription(desc string) StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.Description = strings.TrimSpace(desc)
	}
}

// ReturnInvalidStructuredOutput returns invalid prompt-validated JSON as
// StructuredOutput.Valid=false instead of marking the run as failed.
func ReturnInvalidStructuredOutput() StructuredOutputOption {
	return func(schema *OutputSchema) {
		schema.OnInvalid = StructuredOutputReturnInvalid
	}
}

// SchemaInlineReferences asks the generator to inline referenced definitions.
// This is useful for CLIs whose accepted schema subset rejects $defs/$ref.
func SchemaInlineReferences() SchemaOption {
	return func(opts *schemaOptions) {
		opts.inlineRefs = true
	}
}

// SchemaAllowAdditionalProperties relaxes the default strict-object behavior.
func SchemaAllowAdditionalProperties() SchemaOption {
	return func(opts *schemaOptions) {
		opts.allowAdditionalProperties = true
	}
}

// SchemaRequireExplicitTags makes only fields tagged jsonschema:"required"
// required in the generated schema.
func SchemaRequireExplicitTags() SchemaOption {
	return func(opts *schemaOptions) {
		opts.requireExplicitTags = true
	}
}

// SchemaUseGoComments adds Go comments from path under base as schema
// descriptions when the generator can resolve them.
func SchemaUseGoComments(base, path string) SchemaOption {
	return func(opts *schemaOptions) {
		opts.goCommentsBase = strings.TrimSpace(base)
		opts.goCommentsPath = strings.TrimSpace(path)
	}
}

// WithOutputSchema attaches a fully specified structured-output request to one
// Run or Start invocation.
func WithOutputSchema(schema OutputSchema) RunOption {
	return func(ro *runOptions) {
		copySchema := cloneOutputSchema(&schema)
		ro.outputSchema = copySchema
	}
}

// WithJSONSchemaOutput attaches a raw JSON Schema document to one Run or Start
// invocation. Defaults are native_strict mode and fail_run invalid policy.
func WithJSONSchemaOutput(schemaJSON []byte, opts ...StructuredOutputOption) RunOption {
	schema := defaultJSONOutputSchema(schemaJSON, opts...)
	return WithOutputSchema(schema)
}

// WithJSONSchemaOutputFile reads a JSON Schema document from path and attaches
// it to one Run or Start invocation.
func WithJSONSchemaOutputFile(path string, opts ...StructuredOutputOption) RunOption {
	raw, err := os.ReadFile(path)
	if err != nil {
		return func(ro *runOptions) {
			ro.outputSchemaErr = &InvalidOutputSchemaError{Reason: "read schema file", Cause: err}
		}
	}
	return WithJSONSchemaOutput(raw, opts...)
}

// JSONSchemaFor derives a JSON Schema document from Go type T.
func JSONSchemaFor[T any](opts ...SchemaOption) (json.RawMessage, error) {
	var cfg schemaOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	t := reflect.TypeFor[T]()
	if err := validateSchemaType(t, nil); err != nil {
		return nil, err
	}
	recursive := schemaTypeHasCycle(t, nil, nil)
	if cfg.inlineRefs && recursive {
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("cannot inline recursive Go type %s", t)}
	}

	reflector := &invopopjsonschema.Reflector{
		Anonymous:                  true,
		ExpandedStruct:             !recursive,
		DoNotReference:             cfg.inlineRefs,
		AllowAdditionalProperties:  cfg.allowAdditionalProperties,
		RequiredFromJSONSchemaTags: cfg.requireExplicitTags,
	}
	if cfg.goCommentsBase != "" || cfg.goCommentsPath != "" {
		if cfg.goCommentsBase == "" || cfg.goCommentsPath == "" {
			return nil, &InvalidOutputSchemaError{Reason: "SchemaUseGoComments requires both base and path"}
		}
		if err := reflector.AddGoComments(cfg.goCommentsBase, cfg.goCommentsPath); err != nil {
			return nil, &InvalidOutputSchemaError{Reason: "load Go comments", Cause: err}
		}
	}

	raw, err := json.Marshal(reflector.ReflectFromType(t))
	if err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "marshal generated schema", Cause: err}
	}
	return normalizeJSON(raw)
}

// WithJSONSchemaOutputFor derives a schema from Go type T and attaches it to
// one Run or Start invocation.
func WithJSONSchemaOutputFor[T any](opts ...StructuredOutputOption) RunOption {
	raw, err := JSONSchemaFor[T]()
	if err != nil {
		return func(ro *runOptions) {
			ro.outputSchemaErr = err
		}
	}
	return WithJSONSchemaOutput(raw, opts...)
}

// DecodeStructuredOutput decodes and type-checks the final structured output.
func DecodeStructuredOutput[T any](res RunResult) (T, error) {
	var out T
	if res.StructuredOutput == nil {
		return out, errors.New("agentadaptor: structured output missing")
	}
	if !res.StructuredOutput.Valid {
		return out, fmt.Errorf("agentadaptor: structured output invalid: %s", strings.Join(res.StructuredOutput.ValidationErrors, "; "))
	}
	if len(res.StructuredOutput.RawJSON) == 0 {
		return out, errors.New("agentadaptor: structured output RawJSON is empty")
	}
	if err := json.Unmarshal(res.StructuredOutput.RawJSON, &out); err != nil {
		return out, err
	}
	return out, nil
}

// RunStructured runs r and decodes the final structured output into T.
func RunStructured[T any](ctx context.Context, r Runner, prompt string, opts ...RunOption) (T, RunResult, error) {
	var out T
	runOpts := append([]RunOption{WithJSONSchemaOutputFor[T]()}, opts...)
	res, err := r.Run(ctx, prompt, runOpts...)
	if err != nil {
		return out, res, err
	}
	out, err = DecodeStructuredOutput[T](res)
	return out, res, err
}

func defaultJSONOutputSchema(schemaJSON []byte, opts ...StructuredOutputOption) OutputSchema {
	schema := OutputSchema{
		Format:     OutputFormatJSONSchema,
		Mode:       StructuredOutputNativeStrict,
		SchemaJSON: append(json.RawMessage(nil), schemaJSON...),
		OnInvalid:  StructuredOutputFailRun,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&schema)
		}
	}
	return schema
}

func normalizeOutputSchema(schema *OutputSchema) (*OutputSchema, error) {
	if schema == nil {
		return nil, nil
	}
	out := cloneOutputSchema(schema)
	if out.Format == "" {
		out.Format = OutputFormatJSONSchema
	}
	if out.Mode == "" {
		out.Mode = StructuredOutputNativeStrict
	}
	if out.OnInvalid == "" {
		out.OnInvalid = StructuredOutputFailRun
	}
	if out.Format != OutputFormatJSONSchema {
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported output format %q", out.Format)}
	}
	switch out.Mode {
	case StructuredOutputNativeStrict, StructuredOutputPreferNative, StructuredOutputPromptValidate:
	default:
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported structured output mode %q", out.Mode)}
	}
	switch out.OnInvalid {
	case StructuredOutputFailRun, StructuredOutputReturnInvalid:
	default:
		return nil, &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported invalid policy %q", out.OnInvalid)}
	}
	if len(bytes.TrimSpace(out.SchemaJSON)) == 0 {
		return nil, &InvalidOutputSchemaError{Reason: "schema JSON is required"}
	}
	normalized, err := normalizeJSON(out.SchemaJSON)
	if err != nil {
		return nil, err
	}
	if _, err := compileJSONSchema(normalized); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "compile JSON Schema", Cause: err}
	}
	out.SchemaJSON = normalized
	return out, nil
}

func resolveStructuredOutputSource(desc DriverDescriptor, schema *OutputSchema, streaming bool, policy RunPolicy) (StructuredOutputSource, error) {
	if schema == nil {
		return "", nil
	}
	caps := desc.StructuredOutput
	hitlAsk := policy.HumanDecision.Permission == HumanDecisionAsk ||
		policy.HumanDecision.PlanReview == HumanDecisionAsk ||
		policy.HumanDecision.Question == QuestionAsk

	nativeOK := caps.JSONSchemaNative && caps.WorksWithRun && caps.WorksWithStart
	if nativeOK && streaming && !caps.WorksWithStreaming {
		nativeOK = false
	}
	if nativeOK && hitlAsk && !caps.WorksWithHITL {
		nativeOK = false
	}

	promptOK := caps.JSONSchemaPromptValidate && caps.WorksWithRun && caps.WorksWithStart
	if promptOK && streaming && !caps.WorksWithStreaming {
		promptOK = false
	}
	if promptOK && hitlAsk && !caps.WorksWithHITL {
		promptOK = false
	}

	switch schema.Mode {
	case StructuredOutputNativeStrict:
		if nativeOK {
			return StructuredOutputSourceNative, nil
		}
		return "", &StructuredOutputUnsupportedError{Adapter: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, streaming, hitlAsk, true)}
	case StructuredOutputPromptValidate:
		if promptOK {
			return StructuredOutputSourcePromptValidate, nil
		}
		return "", &StructuredOutputUnsupportedError{Adapter: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, streaming, hitlAsk, false)}
	case StructuredOutputPreferNative:
		if nativeOK {
			return StructuredOutputSourceNative, nil
		}
		if promptOK {
			return StructuredOutputSourcePromptValidate, nil
		}
		return "", &StructuredOutputUnsupportedError{Adapter: desc.Type, Mode: schema.Mode, Reason: structuredCapabilityReason(caps, streaming, hitlAsk, false)}
	default:
		return "", &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported structured output mode %q", schema.Mode)}
	}
}

func structuredCapabilityReason(caps StructuredOutputCapability, streaming, hitlAsk, native bool) string {
	switch {
	case native && !caps.JSONSchemaNative:
		return "native JSON Schema output is not supported"
	case !native && !caps.JSONSchemaPromptValidate:
		return "prompt-validation JSON Schema output is not supported"
	case !caps.WorksWithRun || !caps.WorksWithStart:
		return "structured output is not supported for Run/Start"
	case streaming && !caps.WorksWithStreaming:
		return "structured output is not supported with streaming"
	case hitlAsk && !caps.WorksWithHITL:
		return "structured output is not supported with HITL Ask modes"
	case caps.Notes != "":
		return caps.Notes
	default:
		return "unsupported structured output capability combination"
	}
}

func validateStructuredOutput(schema *OutputSchema, source StructuredOutputSource, raw []byte) *StructuredOutput {
	out := &StructuredOutput{
		Format:     schema.Format,
		Mode:       schema.Mode,
		Source:     source,
		SchemaHash: schemaHash(schema),
	}
	normalized, value, errs := validateJSONValue(schema.SchemaJSON, raw)
	out.RawJSON = normalized
	out.Value = value
	out.Valid = len(errs) == 0
	out.ValidationErrors = errs
	return out
}

func cloneOutputSchema(schema *OutputSchema) *OutputSchema {
	if schema == nil {
		return nil
	}
	out := *schema
	out.SchemaJSON = append(json.RawMessage(nil), schema.SchemaJSON...)
	return &out
}

func cloneStructuredOutput(value *StructuredOutput) *StructuredOutput {
	if value == nil {
		return nil
	}
	out := *value
	out.RawJSON = append(json.RawMessage(nil), value.RawJSON...)
	out.ValidationErrors = cloneStrings(value.ValidationErrors)
	return &out
}

func schemaHash(schema *OutputSchema) string {
	if schema == nil {
		return ""
	}
	return stableHash("output_schema", schema.Format, schema.SchemaJSON)
}

func normalizeJSON(raw []byte) (json.RawMessage, error) {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "parse JSON", Cause: err}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, &InvalidOutputSchemaError{Reason: "multiple JSON values"}
		}
		return nil, &InvalidOutputSchemaError{Reason: "parse JSON", Cause: err}
	}
	var buf bytes.Buffer
	if err := writeCanonicalJSON(&buf, decoded); err != nil {
		return nil, &InvalidOutputSchemaError{Reason: "canonicalize JSON", Cause: err}
	}
	return json.RawMessage(buf.Bytes()), nil
}

func compileJSONSchema(schemaJSON []byte) (*santhoshjsonschema.Schema, error) {
	doc, err := decodeJSONValue(schemaJSON)
	if err != nil {
		return nil, err
	}
	compiler := santhoshjsonschema.NewCompiler()
	compiler.DefaultDraft(santhoshjsonschema.Draft2020)
	if err := compiler.AddResource("schema.json", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("schema.json")
}

func validateJSONValue(schemaJSON []byte, raw []byte) (json.RawMessage, any, []string) {
	normalized, err := normalizeJSON(raw)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	value, err := decodeJSONValue(normalized)
	if err != nil {
		return normalized, nil, []string{err.Error()}
	}
	schema, err := compileJSONSchema(schemaJSON)
	if err != nil {
		return normalized, value, []string{err.Error()}
	}
	if err := schema.Validate(value); err != nil {
		return normalized, value, flattenValidationErrors(err)
	}
	return normalized, value, nil
}

func decodeJSONValue(raw []byte) (any, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, &InvalidOutputSchemaError{Reason: "multiple JSON values"}
		}
		return nil, err
	}
	return value, nil
}

func flattenValidationErrors(err error) []string {
	if err == nil {
		return nil
	}
	var validationErr *santhoshjsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return []string{err.Error()}
	}
	var out []string
	var walk func(*santhoshjsonschema.ValidationError)
	walk = func(e *santhoshjsonschema.ValidationError) {
		if e == nil {
			return
		}
		if len(e.Causes) == 0 {
			out = append(out, e.Error())
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(validationErr)
	if len(out) == 0 {
		out = append(out, err.Error())
	}
	return out
}

func validateSchemaType(t reflect.Type, stack map[reflect.Type]bool) error {
	if t == nil {
		return &InvalidOutputSchemaError{Reason: "nil Go type"}
	}
	pointers := map[reflect.Type]bool{}
	for t.Kind() == reflect.Pointer {
		if pointers[t] {
			return &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported self-referential pointer type %s", t)}
		}
		pointers[t] = true
		t = t.Elem()
	}
	composite := t.Kind() == reflect.Map || t.Kind() == reflect.Array || t.Kind() == reflect.Slice || t.Kind() == reflect.Struct
	if composite {
		if stack == nil {
			stack = map[reflect.Type]bool{}
		}
		if stack[t] {
			return nil
		}
		stack[t] = true
		defer delete(stack, t)
	}
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128, reflect.Uintptr:
		return &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported Go type %s", t)}
	case reflect.Map:
		key := t.Key()
		keyPointers := map[reflect.Type]bool{}
		for key.Kind() == reflect.Pointer {
			if keyPointers[key] {
				return &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported self-referential map key type %s", t.Key())}
			}
			keyPointers[key] = true
			key = key.Elem()
		}
		if key.Kind() != reflect.String {
			return &InvalidOutputSchemaError{Reason: fmt.Sprintf("unsupported map key type %s", t.Key())}
		}
		return validateSchemaType(t.Elem(), stack)
	case reflect.Array, reflect.Slice:
		return validateSchemaType(t.Elem(), stack)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := validateSchemaType(field.Type, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaTypeHasCycle(t reflect.Type, active, visited map[reflect.Type]bool) bool {
	if t == nil {
		return false
	}
	pointers := map[reflect.Type]bool{}
	for t.Kind() == reflect.Pointer {
		if pointers[t] {
			return true
		}
		pointers[t] = true
		t = t.Elem()
	}
	composite := t.Kind() == reflect.Map || t.Kind() == reflect.Array || t.Kind() == reflect.Slice || t.Kind() == reflect.Struct
	if !composite {
		return false
	}
	if active == nil {
		active = map[reflect.Type]bool{}
	}
	if visited == nil {
		visited = map[reflect.Type]bool{}
	}
	if active[t] {
		return true
	}
	if visited[t] {
		return false
	}
	active[t] = true
	defer delete(active, t)
	switch t.Kind() {
	case reflect.Map, reflect.Array, reflect.Slice:
		if schemaTypeHasCycle(t.Elem(), active, visited) {
			return true
		}
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if schemaTypeHasCycle(field.Type, active, visited) {
				return true
			}
		}
	}
	visited[t] = true
	return false
}

func structuredOutputPromptInstruction(schema *OutputSchema) string {
	if schema == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Return only a single JSON value as the final assistant response. ")
	b.WriteString("Do not include Markdown fences, prose, comments, or any text outside the JSON value. ")
	b.WriteString("The JSON value must validate against this JSON Schema")
	if schema.Name != "" {
		b.WriteString(" named ")
		b.WriteString(schema.Name)
	}
	if schema.Description != "" {
		b.WriteString(". Description: ")
		b.WriteString(schema.Description)
	}
	b.WriteString(". Schema: ")
	b.WriteString(string(schema.SchemaJSON))
	return b.String()
}
