package adaptor

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	invopopjsonschema "github.com/invopop/jsonschema"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// Structured output is configured with SchemaStrict, SchemaFlexible, or
// SchemaPromptOnly.
//
//	review, _, err := adaptor.RunAs[Review](ctx, reviewer, "review the diff")
//
//	stream := agent.Stream(ctx, p, adaptor.WithSchema[Review]())
//	...
//	res, err := stream.Result()
//	var review Review
//	err = res.Decode(&review)
//
// The Driver capability matrix determines which modes are available. An
// unsupported mode fails before Driver.Run starts with
// ErrStructuredOutputUnsupported.

// SchemaOption customizes WithSchema[T]: how the JSON Schema document is
// generated from the Go type, and how the structured-output request is
// shaped (mode, name, description, invalid policy).
type SchemaOption func(*schemaSettings)

type schemaSettings struct {
	gen    schemaGenSettings
	mutate []func(*driver.OutputSchema)
}

// schemaGenSettings contains the Go-to-JSON-Schema generation knobs.
type schemaGenSettings struct {
	inlineRefs                bool
	allowAdditionalProperties bool
	requireExplicitTags       bool
	goCommentsBase            string
	goCommentsPath            string
}

func (s *schemaSettings) request(fn func(*driver.OutputSchema)) {
	s.mutate = append(s.mutate, fn)
}

// SchemaStrict requires provider/CLI-native JSON Schema enforcement
// (native_strict — the default). Drivers that cannot enforce the schema
// natively fail the run before launch.
func SchemaStrict() SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.Mode = driver.StructuredOutputNativeStrict })
	}
}

// SchemaFlexible uses native enforcement when the driver can honor it and
// falls back to explicit prompt instructions plus SDK-side validation
// otherwise (prefer_native).
func SchemaFlexible() SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.Mode = driver.StructuredOutputPreferNative })
	}
}

// SchemaPromptOnly requests explicit prompt instructions plus SDK-side
// exact-JSON validation (prompt_validate). It is weaker than native
// enforcement but works with drivers that have no native schema support.
func SchemaPromptOnly() SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.Mode = driver.StructuredOutputPromptValidate })
	}
}

// SchemaName sets a provider-facing schema name when supported.
func SchemaName(name string) SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.Name = strings.TrimSpace(name) })
	}
}

// SchemaDescription sets a provider-facing schema description when
// supported.
func SchemaDescription(desc string) SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.Description = strings.TrimSpace(desc) })
	}
}

// SchemaReturnInvalid returns invalid structured output as
// StructuredOutput.Valid=false (readable via Result.Decode's error) instead
// of failing the run.
func SchemaReturnInvalid() SchemaOption {
	return func(s *schemaSettings) {
		s.request(func(schema *driver.OutputSchema) { schema.OnInvalid = driver.StructuredOutputReturnInvalid })
	}
}

// SchemaInlineReferences asks the generator to inline referenced
// definitions. Useful for CLIs whose accepted schema subset rejects
// $defs/$ref. Inlining a recursive Go type is an error.
func SchemaInlineReferences() SchemaOption {
	return func(s *schemaSettings) { s.gen.inlineRefs = true }
}

// SchemaAllowAdditionalProperties relaxes the default strict-object
// behavior of generated schemas.
func SchemaAllowAdditionalProperties() SchemaOption {
	return func(s *schemaSettings) { s.gen.allowAdditionalProperties = true }
}

// SchemaRequireExplicitTags makes only fields tagged jsonschema:"required"
// required in the generated schema.
func SchemaRequireExplicitTags() SchemaOption {
	return func(s *schemaSettings) { s.gen.requireExplicitTags = true }
}

// SchemaUseGoComments adds Go comments from path under base as schema
// descriptions when the generator can resolve them.
func SchemaUseGoComments(base, path string) SchemaOption {
	return func(s *schemaSettings) {
		s.gen.goCommentsBase = strings.TrimSpace(base)
		s.gen.goCommentsPath = strings.TrimSpace(path)
	}
}

// WithSchema requests structured output matching Go type T for this
// invocation. The JSON Schema document is derived from T at option
// construction time; a derivation failure fails the run before the driver
// launches (schema bugs are programmer errors, never silent degradation).
// Defaults: json_schema format, SchemaStrict mode, fail-run invalid policy.
//
// Call scope only — passing WithSchema to New is a compile error
// ("missing method ApplyNew"): a schema belongs to one question, not to
// the Agent.
func WithSchema[T any](opts ...SchemaOption) CallOption {
	cfg := collectSchemaSettings(opts)
	raw, err := jsonSchemaForType(reflect.TypeFor[T](), cfg.gen)
	return callOptionFunc(func(s *RunSettings) {
		if err != nil {
			s.SetOutputSchemaError(err)
			return
		}
		s.SetOutputSchema(cfg.buildSchema(raw))
	})
}

// WithSchemaJSON requests structured output matching a raw JSON Schema
// document — the escape hatch for schemas that do not originate from a Go
// type (contract files, registry-served schemas). Generation-side
// SchemaOptions (SchemaInlineReferences, ...) have no effect here; the
// request-side ones (SchemaStrict / SchemaFlexible / SchemaPromptOnly /
// SchemaName / SchemaDescription / SchemaReturnInvalid) apply as usual.
// Call scope only.
func WithSchemaJSON(schemaJSON []byte, opts ...SchemaOption) CallOption {
	cfg := collectSchemaSettings(opts)
	raw := append(json.RawMessage(nil), schemaJSON...)
	return callOptionFunc(func(s *RunSettings) {
		s.SetOutputSchema(cfg.buildSchema(raw))
	})
}

// RunAs runs one prompt and decodes the structured output into T. It
// accepts any Runner, so stateless Agents and stateful Threads work
// interchangeably:
//
//	triage, res, err := adaptor.RunAs[Triage](ctx, agent, prompt)
//
// RunAs prepends WithSchema[T]() to the call options; explicit options
// (including another WithSchema) apply after it and win. On a run error
// the zero T is returned together with Run's (*Result, error) contract; on
// success a decode failure surfaces as the returned error with the Result
// still available.
func RunAs[T any](ctx context.Context, r Runner, prompt string, opts ...CallOption) (T, *Result, error) {
	var out T
	callOpts := append([]CallOption{WithSchema[T]()}, opts...)
	res, err := r.Run(ctx, prompt, callOpts...)
	if err != nil {
		return out, res, err
	}
	if err := res.Decode(&out); err != nil {
		return out, res, err
	}
	return out, res, nil
}

func collectSchemaSettings(opts []SchemaOption) schemaSettings {
	var cfg schemaSettings
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// buildSchema assembles the driver-facing request with the documented
// defaults (json_schema / native_strict / fail_run) and applies option
// mutations in order.
func (s schemaSettings) buildSchema(raw json.RawMessage) driver.OutputSchema {
	schema := driver.OutputSchema{
		Format:     driver.OutputFormatJSONSchema,
		Mode:       driver.StructuredOutputNativeStrict,
		SchemaJSON: append(json.RawMessage(nil), raw...),
		OnInvalid:  driver.StructuredOutputFailRun,
	}
	for _, m := range s.mutate {
		m(&schema)
	}
	return schema
}

// jsonSchemaForType derives a JSON Schema document from a Go type. It performs
// type validation, rejects recursive types when refs are inlined, configures
// the reflector, and normalizes the generated JSON.
func jsonSchemaForType(t reflect.Type, cfg schemaGenSettings) (json.RawMessage, error) {
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
