package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-dance/agent-adaptor/tool"
)

type searchInput struct {
	Query string `json:"query" jsonschema:"required,minLength=1"`
	Limit int    `json:"limit,omitempty" jsonschema:"minimum=1"`
}

type searchOutput struct {
	Files []string `json:"files" jsonschema:"required"`
}

type treeInput struct {
	Value    string     `json:"value" jsonschema:"required"`
	Children *treeInput `json:"children,omitempty"`
}

type panickingInput struct{}

func (*panickingInput) UnmarshalJSON([]byte) error {
	panic("input secret")
}

type panickingOutput struct{}

func (panickingOutput) MarshalJSON() ([]byte, error) {
	panic("output secret")
}

func TestDefineInfersSchemasAndPreservesSemanticHints(t *testing.T) {
	definition := tool.Define(
		" search_repo ",
		" Search repository files. ",
		func(context.Context, searchInput) (searchOutput, error) {
			return searchOutput{}, nil
		},
		tool.Title(" Repository search "),
		tool.ReadOnly(),
		tool.Idempotent(),
		tool.OpenWorld(),
		tool.Revision(" search/v2 "),
	)

	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatalf("Descriptor() error = %v", err)
	}
	if descriptor.Name != "search_repo" || descriptor.Title != "Repository search" || descriptor.Description != "Search repository files." || descriptor.Revision != "search/v2" {
		t.Fatalf("descriptor identity = %#v", descriptor)
	}
	if descriptor.Annotations.ReadOnly == nil || !*descriptor.Annotations.ReadOnly || descriptor.Annotations.Idempotent == nil || !*descriptor.Annotations.Idempotent || descriptor.Annotations.OpenWorld == nil || !*descriptor.Annotations.OpenWorld {
		t.Fatalf("annotations = %#v, want selected hints explicitly true", descriptor.Annotations)
	}
	if descriptor.Annotations.Destructive != nil {
		t.Fatalf("Destructive = %#v, want unspecified", descriptor.Annotations.Destructive)
	}
	for name, raw := range map[string]json.RawMessage{"input": descriptor.InputSchemaJSON, "output": descriptor.OutputSchemaJSON} {
		if !json.Valid(raw) {
			t.Fatalf("%s schema is not JSON: %s", name, raw)
		}
	}
	if !strings.Contains(string(descriptor.InputSchemaJSON), `"query"`) || !strings.Contains(string(descriptor.OutputSchemaJSON), `"files"`) {
		t.Fatalf("inferred schemas = input %s, output %s", descriptor.InputSchemaJSON, descriptor.OutputSchemaJSON)
	}
	if _, err := definition.Invoke(context.Background(), json.RawMessage(`{"query":""}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("jsonschema tag validation error = %v, want ErrInvalidInput", err)
	}
}

func TestUnselectedAnnotationsRemainAbsent(t *testing.T) {
	definition := tool.Define("noop", "Do nothing.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(descriptor.Annotations, tool.Annotations{}) {
		t.Fatalf("Annotations = %#v, want all hints unspecified", descriptor.Annotations)
	}
}

func TestAnnotationsCanExplicitlyOverrideMCPTrueDefaults(t *testing.T) {
	definition := tool.Define("local_update", "Update local state.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, nil
	}, tool.NonDestructive(), tool.ClosedWorld(), tool.Revision("v1"))
	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Annotations.Destructive == nil || *descriptor.Annotations.Destructive {
		t.Fatalf("Destructive = %#v, want explicit false", descriptor.Annotations.Destructive)
	}
	if descriptor.Annotations.OpenWorld == nil || *descriptor.Annotations.OpenWorld {
		t.Fatalf("OpenWorld = %#v, want explicit false", descriptor.Annotations.OpenWorld)
	}
	if descriptor.Annotations.ReadOnly != nil || descriptor.Annotations.Idempotent != nil {
		t.Fatalf("unselected annotations = %#v, want unspecified", descriptor.Annotations)
	}
}

func TestDescriptorIsDeterministicAndDetached(t *testing.T) {
	left := tool.Define("lookup", "Lookup.", func(context.Context, searchInput) (searchOutput, error) {
		return searchOutput{Files: []string{"left"}}, nil
	}, tool.ReadOnly(), tool.Revision("v1"))
	right := tool.Define("lookup", "Lookup.", func(context.Context, searchInput) (searchOutput, error) {
		return searchOutput{Files: []string{"right"}}, nil
	}, tool.ReadOnly(), tool.Revision("v1"))

	leftDescriptor, err := left.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	rightDescriptor, err := right.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftDescriptor, rightDescriptor) {
		t.Fatalf("equivalent declarations differ by handler identity:\nleft=%#v\nright=%#v", leftDescriptor, rightDescriptor)
	}

	leftDescriptor.InputSchemaJSON[0] = '!'
	*leftDescriptor.Annotations.ReadOnly = false
	again, err := left.Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(again.InputSchemaJSON) || again.Annotations.ReadOnly == nil || !*again.Annotations.ReadOnly {
		t.Fatalf("caller mutation leaked into Definition: %#v", again)
	}
}

func TestRecursiveGoTypesUseStableLocalSchemaReferences(t *testing.T) {
	definition := tool.Define("walk_tree", "Walk a recursive tree.", func(_ context.Context, in treeInput) (treeInput, error) {
		return in, nil
	})
	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatalf("Descriptor() error = %v", err)
	}
	if !strings.Contains(string(descriptor.InputSchemaJSON), `"$ref":"#/$defs/`) || !strings.Contains(string(descriptor.InputSchemaJSON), `"type":"object"`) || !strings.Contains(string(descriptor.OutputSchemaJSON), `"$ref":"#/$defs/`) {
		t.Fatalf("recursive schemas do not use local definitions: input=%s output=%s", descriptor.InputSchemaJSON, descriptor.OutputSchemaJSON)
	}
	result, err := definition.Invoke(context.Background(), json.RawMessage(`{"value":"root","children":{"value":"leaf"}}`))
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got, want := string(result), `{"children":{"value":"leaf"},"value":"root"}`; got != want {
		t.Fatalf("Invoke() = %s, want %s", got, want)
	}
}

func TestRawSchemaOverridesAreSnapshottedCanonicalAndEnforced(t *testing.T) {
	inputSchema := []byte(`{
		"required":["query"],
		"properties":{"query":{"minLength":2,"type":"string"}},
		"additionalProperties":false,
		"type":"object"
	}`)
	outputSchema := []byte(`{"type":"object","required":["files"],"properties":{"files":{"minItems":1,"type":"array","items":{"type":"string"}}}}`)
	definition := tool.Define(
		"strict_lookup",
		"Strict lookup.",
		func(_ context.Context, in searchInput) (searchOutput, error) {
			return searchOutput{Files: []string{in.Query}}, nil
		},
		tool.InputSchemaJSON(inputSchema),
		tool.OutputSchemaJSON(outputSchema),
	)
	for index := range inputSchema {
		inputSchema[index] = 'x'
	}
	for index := range outputSchema {
		outputSchema[index] = 'x'
	}

	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatalf("Descriptor() error = %v", err)
	}
	if got, want := string(descriptor.InputSchemaJSON), `{"additionalProperties":false,"properties":{"query":{"minLength":2,"type":"string"}},"required":["query"],"type":"object"}`; got != want {
		t.Fatalf("canonical input schema = %s, want %s", got, want)
	}
	result, err := definition.Invoke(context.Background(), json.RawMessage(`{"query":"go"}`))
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got, want := string(result), `{"files":["go"]}`; got != want {
		t.Fatalf("Invoke() = %s, want %s", got, want)
	}
	if _, err := definition.Invoke(context.Background(), json.RawMessage(`{"query":"x"}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("short input error = %v, want ErrInvalidInput", err)
	}
	if _, err := definition.Invoke(context.Background(), json.RawMessage(`{"query":"go","extra":true}`)); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("extra input error = %v, want ErrInvalidInput", err)
	}
}

func TestOutputSchemaAcceptsStandardBooleanSchema(t *testing.T) {
	definition := tool.Define(
		"boolean_output_schema",
		"Use an unrestricted output schema.",
		func(context.Context, struct{}) (string, error) { return "ok", nil },
		tool.OutputSchemaJSON([]byte(`true`)),
	)
	descriptor, err := definition.Descriptor()
	if err != nil {
		t.Fatalf("Descriptor() error = %v", err)
	}
	if got, want := string(descriptor.OutputSchemaJSON), "true"; got != want {
		t.Fatalf("OutputSchemaJSON = %s, want %s", got, want)
	}
	result, err := definition.Invoke(context.Background(), nil)
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	if got, want := string(result), `"ok"`; got != want {
		t.Fatalf("Invoke() = %s, want %s", got, want)
	}
}

func TestDefinitionValidation(t *testing.T) {
	validHandler := func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }
	var nilHandler tool.Handler[struct{}, struct{}]
	tests := []struct {
		name       string
		definition tool.Definition
		contains   string
	}{
		{name: "empty name", definition: tool.Define(" ", "Description.", validHandler), contains: "name is required"},
		{name: "invalid name", definition: tool.Define("not valid", "Description.", validHandler), contains: "ASCII letters"},
		{name: "long name", definition: tool.Define(strings.Repeat("a", 129), "Description.", validHandler), contains: "128 bytes"},
		{name: "empty description", definition: tool.Define("valid", " ", validHandler), contains: "description is required"},
		{name: "nil handler", definition: tool.Define("valid", "Description.", nilHandler), contains: "handler is nil"},
		{name: "conflicting hints", definition: tool.Define("valid", "Description.", validHandler, tool.ReadOnly(), tool.Destructive()), contains: "mutually exclusive"},
		{name: "scalar input", definition: tool.Define("valid", "Description.", func(context.Context, string) (struct{}, error) { return struct{}{}, nil }), contains: "JSON object"},
		{name: "unsupported output", definition: tool.Define("valid", "Description.", func(context.Context, struct{}) (chan int, error) { return nil, nil }), contains: "unsupported Go type"},
		{name: "empty input schema", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON(nil)), contains: "JSON is required"},
		{name: "malformed output schema", definition: tool.Define("valid", "Description.", validHandler, tool.OutputSchemaJSON([]byte(`{"type":`))), contains: "parse JSON"},
		{name: "boolean schema", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`true`))), contains: "JSON object"},
		{name: "missing input object type", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"properties":{}}`))), contains: `top-level "type"`},
		{name: "scalar input schema", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"type":"string"}`))), contains: `top-level "type"`},
		{name: "external reference", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"$ref":"https://example.com/schema"}`))), contains: "external $ref"},
		{name: "external dynamic reference", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"type":"object","$dynamicRef":"https://example.com/schema"}`))), contains: "external $dynamicRef"},
		{name: "external recursive reference", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"type":"object","$recursiveRef":"https://example.com/schema"}`))), contains: "external $recursiveRef"},
		{name: "invalid schema keyword", definition: tool.Define("valid", "Description.", validHandler, tool.InputSchemaJSON([]byte(`{"type":"object","properties":{"value":{"type":"invalid"}}}`))), contains: "compile JSON Schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.definition.Descriptor()
			if !errors.Is(err, tool.ErrInvalidDefinition) || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Descriptor() error = %v, want ErrInvalidDefinition containing %q", err, test.contains)
			}
			if _, invokeErr := test.definition.Invoke(context.Background(), json.RawMessage(`{}`)); !errors.Is(invokeErr, tool.ErrInvalidDefinition) {
				t.Fatalf("Invoke() error = %v, want cached ErrInvalidDefinition", invokeErr)
			}
		})
	}
}

func TestInvokePreservesHandlerErrorsCancellationAndRejection(t *testing.T) {
	handlerErr := errors.New("database unavailable")
	failed := tool.Define("failed", "Fail.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, handlerErr
	})
	if _, err := failed.Invoke(context.Background(), nil); !errors.Is(err, handlerErr) {
		t.Fatalf("handler error = %v, want original identity", err)
	}

	rejectedErr := tool.Reject("not_found", "No matching record.")
	rejected := tool.Define("rejected", "Reject.", func(context.Context, struct{}) (struct{}, error) {
		return struct{}{}, rejectedErr
	})
	_, err := rejected.Invoke(context.Background(), nil)
	if err != rejectedErr {
		t.Fatalf("rejection error = %v, want exact handler error", err)
	}
	code, message, ok := tool.AsRejection(err)
	if !ok {
		t.Fatalf("rejection type = %T", err)
	}
	if code != "not_found" || message != "No matching record." {
		t.Fatalf("rejection = (%q, %q)", code, message)
	}

	var calls atomic.Int64
	canceled := tool.Define("canceled", "Observe cancellation.", func(context.Context, struct{}) (struct{}, error) {
		calls.Add(1)
		return struct{}{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := canceled.Invoke(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("handler calls = %d, want 0", calls.Load())
	}
	if _, err := canceled.Invoke(nil, nil); !errors.Is(err, tool.ErrInvalidInput) {
		t.Fatalf("nil context error = %v, want ErrInvalidInput", err)
	}
}

func TestInvokeRecoversPanicAndRejectsInvalidOutput(t *testing.T) {
	panics := tool.Define("panic", "Panic.", func(context.Context, struct{}) (struct{}, error) {
		panic("secret detail")
	})
	if _, err := panics.Invoke(context.Background(), nil); err == nil || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("panic error = %v, want recovered and sanitized", err)
	} else {
		if _, _, ok := tool.AsRejection(err); ok {
			t.Fatalf("panic classified as safe rejection: %v", err)
		}
	}

	invalidOutput := tool.Define(
		"invalid_output",
		"Return invalid output.",
		func(context.Context, struct{}) (searchOutput, error) { return searchOutput{}, nil },
		tool.OutputSchemaJSON([]byte(`{"type":"object","required":["files"],"properties":{"files":{"type":"array","minItems":1}}}`)),
	)
	if _, err := invalidOutput.Invoke(context.Background(), nil); !errors.Is(err, tool.ErrInvalidOutput) {
		t.Fatalf("invalid output error = %v, want ErrInvalidOutput", err)
	}
}

func TestInvokeRecoversJSONCodecPanics(t *testing.T) {
	panickingDecoder := tool.Define("panic_decode", "Panic while decoding.", func(context.Context, panickingInput) (struct{}, error) {
		return struct{}{}, nil
	})
	assertInternalPanic := func(name string, definition tool.Definition) {
		t.Helper()
		_, err := definition.Invoke(context.Background(), json.RawMessage(`{}`))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s error = %v, want recovered and sanitized", name, err)
		}
		if _, _, ok := tool.AsRejection(err); ok {
			t.Fatalf("%s panic classified as safe rejection: %v", name, err)
		}
	}
	assertInternalPanic("decoder", panickingDecoder)

	panickingEncoder := tool.Define("panic_encode", "Panic while encoding.", func(context.Context, struct{}) (panickingOutput, error) {
		return panickingOutput{}, nil
	})
	assertInternalPanic("encoder", panickingEncoder)
}

func TestSchemaMetaschemaCannotLoadExternalResources(t *testing.T) {
	definition := tool.Define(
		"no_external_schema",
		"Reject an external metaschema loader.",
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil },
		tool.InputSchemaJSON([]byte(`{"$schema":"file:///sensitive/local/schema.json","type":"object"}`)),
	)
	if _, err := definition.Descriptor(); !errors.Is(err, tool.ErrInvalidDefinition) || !strings.Contains(err.Error(), "external $schema") {
		t.Fatalf("Descriptor error = %v, want rejected external $schema", err)
	}
}

func TestDefinitionIsConcurrencySafe(t *testing.T) {
	var calls atomic.Int64
	definition := tool.Define("echo", "Echo.", func(_ context.Context, in searchInput) (searchOutput, error) {
		calls.Add(1)
		return searchOutput{Files: []string{in.Query}}, nil
	})

	const workers = 64
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers*2)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := definition.Descriptor(); err != nil {
				errorsFound <- err
			}
			if _, err := definition.Invoke(context.Background(), json.RawMessage(`{"query":"same"}`)); err != nil {
				errorsFound <- err
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent operation error = %v", err)
	}
	if calls.Load() != workers {
		t.Fatalf("handler calls = %d, want %d", calls.Load(), workers)
	}
}

func TestRejectNormalizesSafeFields(t *testing.T) {
	tests := []struct {
		code, message string
		wantCode      string
		wantMessage   string
	}{
		{code: " retry_later ", message: " Try again. ", wantCode: "retry_later", wantMessage: "Try again."},
		{code: "", message: "", wantCode: "rejected", wantMessage: "tool request rejected"},
		{code: "unsafe code\n", message: "Explain.", wantCode: "rejected", wantMessage: "Explain."},
		{code: strings.Repeat("x", 65), message: strings.Repeat("y", 4097), wantCode: "rejected", wantMessage: "tool request rejected"},
	}
	for _, test := range tests {
		rejectionErr := tool.Reject(test.code, test.message)
		if code, message, ok := tool.AsRejection(errors.Join(errors.New("wrapped"), rejectionErr)); !ok {
			t.Fatalf("Reject(%q, %q) type = %T", test.code, test.message, rejectionErr)
		} else if code != test.wantCode || message != test.wantMessage {
			t.Fatalf("Reject(%q, %q) = (%q, %q), want (%q, %q)", test.code, test.message, code, message, test.wantCode, test.wantMessage)
		}
	}
}
