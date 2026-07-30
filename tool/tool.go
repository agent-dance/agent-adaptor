package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	invopopjsonschema "github.com/invopop/jsonschema"
	santhoshjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maximumNameLength = 128
)

var validName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Handler is the ordinary implementation shape for a tool. Implementations
// may be called concurrently and should honor ctx cancellation.
type Handler[In, Out any] func(context.Context, In) (Out, error)

// Definition is an immutable, heterogeneous tool declaration. Values can only
// be constructed by Define; the unexported method seals the interface so that
// invalid third-party implementations cannot enter the runtime.
type Definition interface {
	// Descriptor returns the validated, deterministic tool declaration. The
	// returned schema bytes are detached from the Definition.
	Descriptor() (Descriptor, error)

	// Invoke is the provider-neutral runtime bridge. Applications normally do
	// not call it directly; Agent-owned tool runtimes use it after routing a
	// provider tool call to this Definition.
	Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error)

	definition()
}

// Option changes the semantic declaration produced by Define. The interface
// is sealed; use the option constructors in this package.
type Option interface {
	apply(*settings)
}

type optionFunc func(*settings)

func (f optionFunc) apply(s *settings) { f(s) }

type settings struct {
	title                string
	revision             string
	readOnly             bool
	destructive          bool
	idempotent           bool
	openWorld            bool
	inputSchemaJSON      json.RawMessage
	outputSchemaJSON     json.RawMessage
	inputSchemaOverride  bool
	outputSchemaOverride bool
}

// Title sets a short human-readable display title. The stable invocation key
// remains the name passed to Define.
func Title(title string) Option {
	return optionFunc(func(s *settings) { s.title = strings.TrimSpace(title) })
}

// ReadOnly declares that the tool does not modify its environment.
func ReadOnly() Option {
	return optionFunc(func(s *settings) { s.readOnly = true })
}

// Destructive declares that the tool may make destructive changes.
func Destructive() Option {
	return optionFunc(func(s *settings) { s.destructive = true })
}

// Idempotent declares that repeating a call with the same arguments has no
// additional effect.
func Idempotent() Option {
	return optionFunc(func(s *settings) { s.idempotent = true })
}

// OpenWorld declares that the tool may interact with entities outside the
// Agent's local environment.
func OpenWorld() Option {
	return optionFunc(func(s *settings) { s.openWorld = true })
}

// Revision sets a stable semantic implementation revision. Change it when
// handler behavior changes without a descriptor or schema change so Thread
// and persistent-process compatibility can detect the new capability.
func Revision(revision string) Option {
	return optionFunc(func(s *settings) { s.revision = strings.TrimSpace(revision) })
}

// InputSchemaJSON replaces schema inference for the input type with a standard
// JSON Schema document. The byte slice is snapshotted immediately.
func InputSchemaJSON(schema []byte) Option {
	snapshot := append(json.RawMessage(nil), schema...)
	return optionFunc(func(s *settings) {
		s.inputSchemaOverride = true
		s.inputSchemaJSON = append(json.RawMessage(nil), snapshot...)
	})
}

// OutputSchemaJSON replaces schema inference for the output type with a
// standard JSON Schema document. The byte slice is snapshotted immediately.
func OutputSchemaJSON(schema []byte) Option {
	snapshot := append(json.RawMessage(nil), schema...)
	return optionFunc(func(s *settings) {
		s.outputSchemaOverride = true
		s.outputSchemaJSON = append(json.RawMessage(nil), snapshot...)
	})
}

// Define declares a provider-neutral tool backed by a typed Go handler.
// Declaration errors are retained in the Definition and returned by
// Definition.Descriptor, allowing the common construction expression to stay
// compact while still failing deterministically before a provider is launched.
func Define[In, Out any](name, description string, handler Handler[In, Out], opts ...Option) Definition {
	var cfg settings
	for _, opt := range opts {
		if opt != nil {
			opt.apply(&cfg)
		}
	}
	return &typedDefinition[In, Out]{
		name:        name,
		description: description,
		handler:     handler,
		settings:    cfg,
	}
}

type typedDefinition[In, Out any] struct {
	name        string
	description string
	handler     Handler[In, Out]
	settings    settings
	once        sync.Once
	compiled    *compiledDefinition
	err         error
}

func (*typedDefinition[In, Out]) definition() {}

func (d *typedDefinition[In, Out]) Descriptor() (Descriptor, error) {
	compiled, err := d.resolve()
	if err != nil {
		return Descriptor{}, err
	}
	return cloneDescriptor(compiled.descriptor), nil
}

func (d *typedDefinition[In, Out]) Invoke(ctx context.Context, input json.RawMessage) (output json.RawMessage, err error) {
	defer func() {
		if recover() != nil {
			output = nil
			err = errors.New("tool invocation panicked")
		}
	}()
	compiled, err := d.resolve()
	if err != nil {
		return nil, err
	}
	return compiled.invoke(ctx, append(json.RawMessage(nil), input...))
}

func (d *typedDefinition[In, Out]) resolve() (*compiledDefinition, error) {
	if d == nil {
		return nil, invalidDefinition("", "definition", "is nil", nil)
	}
	d.once.Do(func() {
		defer func() {
			if recover() != nil {
				d.compiled = nil
				d.err = invalidDefinition(d.name, "schema", "derivation panicked", nil)
			}
		}()
		d.compiled, d.err = d.compile()
	})
	return d.compiled, d.err
}

func (d *typedDefinition[In, Out]) compile() (*compiledDefinition, error) {
	if d == nil {
		return nil, invalidDefinition("", "definition", "is nil", nil)
	}
	name := strings.TrimSpace(d.name)
	if name == "" {
		return nil, invalidDefinition("", "name", "is required", nil)
	}
	if len(name) > maximumNameLength {
		return nil, invalidDefinition(name, "name", "must not exceed 128 bytes", nil)
	}
	if !validName.MatchString(name) {
		return nil, invalidDefinition(name, "name", "must contain only ASCII letters, digits, underscore, hyphen, or dot", nil)
	}
	description := strings.TrimSpace(d.description)
	if description == "" {
		return nil, invalidDefinition(name, "description", "is required", nil)
	}
	if d.handler == nil {
		return nil, invalidDefinition(name, "handler", "is nil", nil)
	}
	if d.settings.readOnly && d.settings.destructive {
		return nil, invalidDefinition(name, "annotations", "ReadOnly and Destructive are mutually exclusive", nil)
	}

	inputType := reflect.TypeFor[In]()
	if err := validateInputType(inputType); err != nil {
		return nil, invalidDefinition(name, "input type", err.Error(), err)
	}
	if err := validateJSONType(reflect.TypeFor[Out](), nil); err != nil {
		return nil, invalidDefinition(name, "output type", err.Error(), err)
	}

	inputSchema, inputValidator, err := schemaForTypeOrOverride(inputType, d.settings.inputSchemaJSON, d.settings.inputSchemaOverride, true)
	if err != nil {
		return nil, invalidDefinition(name, "input schema", err.Error(), err)
	}
	outputSchema, outputValidator, err := schemaForTypeOrOverride(reflect.TypeFor[Out](), d.settings.outputSchemaJSON, d.settings.outputSchemaOverride, false)
	if err != nil {
		return nil, invalidDefinition(name, "output schema", err.Error(), err)
	}

	descriptor := Descriptor{
		Name:             name,
		Title:            d.settings.title,
		Description:      description,
		Revision:         d.settings.revision,
		InputSchemaJSON:  inputSchema,
		OutputSchemaJSON: outputSchema,
		Annotations: Annotations{
			ReadOnly:    optionalTrue(d.settings.readOnly),
			Destructive: optionalTrue(d.settings.destructive),
			Idempotent:  optionalTrue(d.settings.idempotent),
			OpenWorld:   optionalTrue(d.settings.openWorld),
		},
	}

	return &compiledDefinition{
		descriptor:      descriptor,
		inputValidator:  inputValidator,
		outputValidator: outputValidator,
		invoke: func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%w: context is nil", ErrInvalidInput)
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(bytes.TrimSpace(raw)) == 0 {
				raw = json.RawMessage(`{}`)
			}
			normalized, err := decodeAndValidateJSON(raw, inputValidator)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
			}
			var in In
			if err := json.Unmarshal(normalized, &in); err != nil {
				return nil, fmt.Errorf("%w: decode Go input: %v", ErrInvalidInput, err)
			}
			out, err := d.handler(ctx, in)
			if err != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return nil, contextErr
				}
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			rawOut, err := json.Marshal(out)
			if err != nil {
				return nil, fmt.Errorf("%w: encode Go output: %v", ErrInvalidOutput, err)
			}
			normalizedOut, err := decodeAndValidateJSON(rawOut, outputValidator)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
			}
			return normalizedOut, nil
		},
	}, nil
}

func invalidDefinition(name, field, reason string, cause error) error {
	message := ""
	if name != "" {
		message += "tool " + name + ": "
	}
	if field != "" {
		message += field
	}
	if reason != "" {
		if message != "" {
			message += " "
		}
		message += reason
	}
	return &definitionError{detail: message, cause: cause}
}

type definitionError struct {
	detail string
	cause  error
}

func (e *definitionError) Error() string {
	if e == nil || e.detail == "" {
		return ErrInvalidDefinition.Error()
	}
	return ErrInvalidDefinition.Error() + ": " + e.detail
}

func (e *definitionError) Unwrap() error {
	if e == nil || e.cause == nil {
		return ErrInvalidDefinition
	}
	return errors.Join(ErrInvalidDefinition, e.cause)
}

func validateInputType(t reflect.Type) error {
	if err := validateJSONType(t, nil); err != nil {
		return err
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		return nil
	}
	if t.Kind() == reflect.Map {
		if t.Key().Kind() == reflect.String {
			return nil
		}
	}
	return fmt.Errorf("must decode from a JSON object, got %s", t)
}

func validateJSONType(t reflect.Type, stack map[reflect.Type]bool) error {
	if t == nil {
		return errors.New("nil Go type")
	}
	pointers := make(map[reflect.Type]bool)
	for t.Kind() == reflect.Pointer {
		if pointers[t] {
			return fmt.Errorf("unsupported self-referential pointer type %s", t)
		}
		pointers[t] = true
		t = t.Elem()
	}
	composite := t.Kind() == reflect.Map || t.Kind() == reflect.Array || t.Kind() == reflect.Slice || t.Kind() == reflect.Struct
	if composite {
		if stack == nil {
			stack = make(map[reflect.Type]bool)
		}
		if stack[t] {
			return nil
		}
		stack[t] = true
		defer delete(stack, t)
	}
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.UnsafePointer, reflect.Complex64, reflect.Complex128, reflect.Uintptr:
		return fmt.Errorf("unsupported Go type %s", t)
	case reflect.Map:
		key := t.Key()
		keyPointers := make(map[reflect.Type]bool)
		for key.Kind() == reflect.Pointer {
			if keyPointers[key] {
				return fmt.Errorf("unsupported self-referential map key type %s", t.Key())
			}
			keyPointers[key] = true
			key = key.Elem()
		}
		if key.Kind() != reflect.String {
			return fmt.Errorf("unsupported map key type %s", t.Key())
		}
		return validateJSONType(t.Elem(), stack)
	case reflect.Array, reflect.Slice:
		return validateJSONType(t.Elem(), stack)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" || field.Tag.Get("json") == "-" {
				continue
			}
			if err := validateJSONType(field.Type, stack); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaForTypeOrOverride(t reflect.Type, override json.RawMessage, hasOverride, requireObject bool) (json.RawMessage, *santhoshjsonschema.Schema, error) {
	var raw []byte
	if hasOverride {
		if len(bytes.TrimSpace(override)) == 0 {
			return nil, nil, errors.New("JSON is required")
		}
		raw = override
	} else {
		reflector := &invopopjsonschema.Reflector{
			Anonymous:                 true,
			ExpandedStruct:            !schemaTypeHasCycle(t, nil, nil),
			AllowAdditionalProperties: false,
		}
		encoded, err := json.Marshal(reflector.ReflectFromType(t))
		if err != nil {
			return nil, nil, fmt.Errorf("derive from Go type: %w", err)
		}
		raw = encoded
	}
	normalized, decoded, err := normalizeJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	document, ok := decoded.(map[string]any)
	if requireObject && !ok {
		return nil, nil, errors.New("must be a JSON object")
	}
	if err := validateLocalReferences(decoded); err != nil {
		return nil, nil, err
	}
	if requireObject {
		changed, err := normalizeInputSchemaRoot(document, hasOverride)
		if err != nil {
			return nil, nil, err
		}
		if changed {
			encoded, err := json.Marshal(document)
			if err != nil {
				return nil, nil, fmt.Errorf("normalize input JSON Schema: %w", err)
			}
			normalized, decoded, err = normalizeJSON(encoded)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	compiler := santhoshjsonschema.NewCompiler()
	compiler.DefaultDraft(santhoshjsonschema.Draft2020)
	if err := compiler.AddResource("tool-schema.json", decoded); err != nil {
		return nil, nil, fmt.Errorf("load JSON Schema: %w", err)
	}
	compiled, err := compiler.Compile("tool-schema.json")
	if err != nil {
		return nil, nil, fmt.Errorf("compile JSON Schema: %w", err)
	}
	return normalized, compiled, nil
}

func normalizeInputSchemaRoot(schema map[string]any, explicitOverride bool) (bool, error) {
	typeValue, exists := schema["type"]
	if !exists {
		if explicitOverride {
			return false, errors.New(`top-level "type" must be "object"`)
		}
		schema["type"] = "object"
		return true, nil
	}
	typeName, ok := typeValue.(string)
	if !ok || typeName != "object" {
		return false, errors.New(`top-level "type" must be "object"`)
	}
	return false, nil
}

func validateLocalReferences(value any) error {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			if err := validateLocalReferences(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range current {
			if key == "$schema" {
				metaschema, ok := item.(string)
				if !ok {
					return errors.New("$schema must be a string")
				}
				if metaschema != "https://json-schema.org/draft/2020-12/schema" &&
					metaschema != "http://json-schema.org/draft/2020-12/schema" {
					return fmt.Errorf("external $schema %q is not supported", metaschema)
				}
			}
			if key == "$ref" || key == "$dynamicRef" || key == "$recursiveRef" {
				ref, ok := item.(string)
				if !ok {
					return fmt.Errorf("%s must be a string", key)
				}
				if !strings.HasPrefix(ref, "#") {
					return fmt.Errorf("external %s %q is not supported", key, ref)
				}
			}
			if err := validateLocalReferences(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaTypeHasCycle(t reflect.Type, visiting, visited map[reflect.Type]bool) bool {
	pointers := make(map[reflect.Type]bool)
	for t.Kind() == reflect.Pointer {
		if pointers[t] {
			return true
		}
		pointers[t] = true
		t = t.Elem()
	}
	if visiting == nil {
		visiting = make(map[reflect.Type]bool)
		visited = make(map[reflect.Type]bool)
	}
	if visiting[t] {
		return true
	}
	if visited[t] {
		return false
	}
	visiting[t] = true
	defer func() {
		delete(visiting, t)
		visited[t] = true
	}()
	switch t.Kind() {
	case reflect.Map:
		return schemaTypeHasCycle(t.Elem(), visiting, visited)
	case reflect.Array, reflect.Slice:
		return schemaTypeHasCycle(t.Elem(), visiting, visited)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath == "" && field.Tag.Get("json") != "-" && schemaTypeHasCycle(field.Type, visiting, visited) {
				return true
			}
		}
	}
	return false
}

func normalizeJSON(raw []byte) (json.RawMessage, any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, nil, fmt.Errorf("parse JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, nil, errors.New("multiple JSON values")
		}
		return nil, nil, fmt.Errorf("parse JSON: %w", err)
	}
	var buffer bytes.Buffer
	if err := writeCanonicalJSON(&buffer, decoded); err != nil {
		return nil, nil, err
	}
	return json.RawMessage(buffer.Bytes()), decoded, nil
}

func decodeAndValidateJSON(raw []byte, schema *santhoshjsonschema.Schema) (json.RawMessage, error) {
	normalized, decoded, err := normalizeJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(decoded); err != nil {
		return nil, fmt.Errorf("validate JSON Schema: %w", err)
	}
	return normalized, nil
}

func writeCanonicalJSON(buffer *bytes.Buffer, value any) error {
	switch current := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if current {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		encoded, _ := json.Marshal(current)
		buffer.Write(encoded)
	case json.Number:
		buffer.WriteString(current.String())
	case float64:
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		buffer.Write(encoded)
	case []any:
		buffer.WriteByte('[')
		for index, item := range current {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonicalJSON(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			encoded, _ := json.Marshal(key)
			buffer.Write(encoded)
			buffer.WriteByte(':')
			if err := writeCanonicalJSON(buffer, current[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return fmt.Errorf("canonicalize JSON: unsupported value %T", value)
	}
	return nil
}

func optionalTrue(set bool) *bool {
	if !set {
		return nil
	}
	value := true
	return &value
}

// Annotations are optional provider-neutral behavioral hints. A nil field is
// unspecified and must not be projected as an explicit false value. Hints are
// not access control and do not replace the Agent's approval policy.
type Annotations struct {
	ReadOnly    *bool
	Destructive *bool
	Idempotent  *bool
	OpenWorld   *bool
}

// Descriptor is the deterministic, transport-independent description of one
// validated tool. Schema slices returned by Definition.Descriptor are detached
// copies and may be safely modified by the caller.
type Descriptor struct {
	Name             string
	Title            string
	Description      string
	Revision         string
	InputSchemaJSON  json.RawMessage
	OutputSchemaJSON json.RawMessage
	Annotations      Annotations
}

type compiledDefinition struct {
	descriptor      Descriptor
	inputValidator  *santhoshjsonschema.Schema
	outputValidator *santhoshjsonschema.Schema
	invoke          func(context.Context, json.RawMessage) (json.RawMessage, error)
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.InputSchemaJSON = append(json.RawMessage(nil), descriptor.InputSchemaJSON...)
	descriptor.OutputSchemaJSON = append(json.RawMessage(nil), descriptor.OutputSchemaJSON...)
	descriptor.Annotations.ReadOnly = cloneBool(descriptor.Annotations.ReadOnly)
	descriptor.Annotations.Destructive = cloneBool(descriptor.Annotations.Destructive)
	descriptor.Annotations.Idempotent = cloneBool(descriptor.Annotations.Idempotent)
	descriptor.Annotations.OpenWorld = cloneBool(descriptor.Annotations.OpenWorld)
	return descriptor
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
