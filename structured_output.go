package agentadaptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	invopopjsonschema "github.com/invopop/jsonschema"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// The engine-side halves of structured output (request normalization,
// capability resolution, local validation) live in internal/engine
// (structured.go there); this file keeps the host-facing constructors and
// the generic schema derivation / decode API.

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
		copySchema := engine.CloneOutputSchema(&schema)
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
	return engine.NormalizeJSON(raw)
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
