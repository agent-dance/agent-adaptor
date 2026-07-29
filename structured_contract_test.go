package adaptor_test

// Structured-output contract for the v1 surface. The consumer declares only
// the desired schema. Core then selects provider-native enforcement when it is
// compatible and otherwise falls back to exact-JSON prompting plus local
// validation. Provider transport selection remains independent from whether
// the consumer called Run or Stream.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

// structuredFake mirrors the root structuredTestDriver on the driver SPI:
// it mints/echoes session checkpoints exactly like a resumable CLI and emits
// native structured output when core selected the native mechanism (or when
// forced, to probe unrequested-output suppression).
type structuredFake struct {
	mu       sync.Mutex
	requests []driver.Request

	caps   driver.StructuredOutputCapability
	output string
	// structuredRaw, when non-empty, diverges the emitted RawJSON from
	// Output so suppression tests can prove which surface Decode used.
	structuredRaw   string
	forceStructured bool

	sessionCounter int
}

var _ driver.Driver = (*structuredFake)(nil)
var _ driver.SessionConfigFingerprinter = (*structuredFake)(nil)
var _ driver.SessionCodecProvider = (*structuredFake)(nil)
var _ driver.StreamSupport = (*structuredFake)(nil)

func (d *structuredFake) Descriptor() driver.Descriptor {
	return driver.Descriptor{
		Type:             "structured-fake",
		DisplayName:      "Structured Fake",
		Sessions:         driver.SessionCapability{SupportsResume: true},
		StructuredOutput: d.caps,
	}
}

func (d *structuredFake) ValidateConfig(any) error { return nil }

func (d *structuredFake) SessionConfigFingerprint() (string, error) {
	return "structured-fake-config/v1", nil
}

func (d *structuredFake) SessionCodec() driver.SessionCodec { return fakeSessionCodec{} }

func (d *structuredFake) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true}
}

func (d *structuredFake) Run(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	var checkpoint *driver.Checkpoint
	if req.Session != nil {
		state := req.Session.State
		if state == nil || state.ResumeID == "" {
			d.sessionCounter++
			state = &driver.SessionState{
				ResumeID:  fmt.Sprintf("structured-session-%d", d.sessionCounter),
				DisplayID: fmt.Sprintf("Structured Session %d", d.sessionCounter),
			}
		}
		checkpoint = &driver.Checkpoint{State: state, Valid: true}
	}
	var structured *driver.StructuredOutput
	if d.forceStructured || (req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative) {
		raw := d.output
		if d.structuredRaw != "" {
			raw = d.structuredRaw
		}
		structured = &driver.StructuredOutput{
			Format:  driver.OutputFormatJSONSchema,
			Source:  driver.StructuredOutputSourceNative,
			RawJSON: []byte(raw),
			Valid:   true,
		}
	}
	output := d.output
	d.mu.Unlock()

	return driver.Response{
		Output:           output,
		RawStreams:       &driver.RawStreams{},
		Checkpoint:       checkpoint,
		StructuredOutput: structured,
	}, nil
}

func (d *structuredFake) request(t *testing.T, i int) driver.Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.requests) {
		t.Fatalf("structured fake saw %d request(s), want index %d", len(d.requests), i)
	}
	return d.requests[i]
}

func (d *structuredFake) lastRequest(t *testing.T) driver.Request {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.requests) == 0 {
		t.Fatal("structured fake saw no requests")
	}
	return d.requests[len(d.requests)-1]
}

func (d *structuredFake) runCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

// fullStructuredCaps advertises both provider transports so these fixtures
// exercise native streaming unless a focused test requests batch fallback.
func fullStructuredCaps() driver.StructuredOutputCapability {
	return driver.StructuredOutputCapability{
		JSONSchemaNative:         true,
		JSONSchemaPromptValidate: true,
		WorksWithRun:             true,
		WorksWithStreaming:       true,
		WorksWithHITL:            true,
	}
}

// The schema fixture types are byte-for-byte copies of the root baseline so
// the generated schemas stay comparable across the two surfaces.

type projectMetadata struct {
	ProjectName          string   `json:"project_name"`
	ProgrammingLanguages []string `json:"programming_languages,omitempty"`
}

type nestedProjectMetadata struct {
	ProjectName string          `json:"project_name"`
	Artifact    projectMetadata `json:"artifact"`
}

type recursiveProjectMetadata struct {
	ProjectName string                      `json:"project_name"`
	Children    []*recursiveProjectMetadata `json:"children"`
}

type recursiveList []recursiveList

type recursiveMap map[string]recursiveMap

type recursivePointer *recursivePointer

type recursivePointerMap map[recursivePointer]string

type largeSchemaNumber struct {
	ID int64 `json:"id" jsonschema:"minimum=9007199254740993"`
}

func TestWithSchemaGeneratesDeterministicSchema(t *testing.T) {
	ctx := context.Background()
	fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor","programming_languages":["go"]}`}
	agent := adaptor.New(fake)

	res, err := agent.Run(ctx, "extract", adaptor.WithSchema[projectMetadata]())
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if _, err := agent.Run(ctx, "extract again", adaptor.WithSchema[projectMetadata]()); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	firstReq := fake.request(t, 0)
	secondReq := fake.request(t, 1)
	first := firstReq.OutputSchema
	second := secondReq.OutputSchema
	if first == nil || second == nil {
		t.Fatalf("output schema missing: first=%v second=%v", first, second)
	}
	if string(first.SchemaJSON) != string(second.SchemaJSON) {
		t.Fatalf("expected deterministic schema bytes:\nfirst=%s\nsecond=%s", first.SchemaJSON, second.SchemaJSON)
	}
	if !strings.Contains(string(first.SchemaJSON), `"additionalProperties":false`) {
		t.Errorf("expected strict object schema, got %s", first.SchemaJSON)
	}
	if !strings.Contains(string(first.SchemaJSON), `"project_name"`) {
		t.Errorf("expected json tag name in schema, got %s", first.SchemaJSON)
	}
	if first.OnInvalid != driver.StructuredOutputFailRun {
		t.Errorf("default invalid policy = %q, want fail_run", first.OnInvalid)
	}
	if firstReq.StructuredOutputSource != driver.StructuredOutputSourceNative || secondReq.StructuredOutputSource != driver.StructuredOutputSourceNative {
		t.Errorf("resolved sources = %q / %q, want native", firstReq.StructuredOutputSource, secondReq.StructuredOutputSource)
	}

	var decoded projectMetadata
	if err := res.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ProjectName != "agent-adaptor" || len(decoded.ProgrammingLanguages) != 1 || decoded.ProgrammingLanguages[0] != "go" {
		t.Errorf("unexpected decoded value: %#v", decoded)
	}
}

func TestWithSchemaSupportsRecursiveTypes(t *testing.T) {
	fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"root","children":[]}`}
	agent := adaptor.New(fake)

	res, err := agent.Run(context.Background(), "extract tree", adaptor.WithSchema[recursiveProjectMetadata]())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	req := fake.lastRequest(t)
	if req.OutputSchema == nil || !strings.Contains(string(req.OutputSchema.SchemaJSON), `"$ref"`) {
		t.Fatalf("expected recursive schema references, got %#v", req.OutputSchema)
	}
	var decoded recursiveProjectMetadata
	if err := res.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ProjectName != "root" {
		t.Errorf("unexpected decoded value: %#v", decoded)
	}
}

func TestWithSchemaRejectsInliningRecursiveTypes(t *testing.T) {
	fake := &structuredFake{caps: fullStructuredCaps(), output: `{}`}
	agent := adaptor.New(fake)

	_, err := agent.Run(context.Background(), "extract", adaptor.WithSchema[recursiveProjectMetadata](adaptor.SchemaInlineReferences()))
	if !errors.Is(err, adaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "cannot inline recursive Go type") {
		t.Fatalf("error = %v, want recursive ErrInvalidOutputSchema", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestWithSchemaSupportsRecursiveCollections(t *testing.T) {
	tests := []struct {
		name   string
		output string
		run    func(agent *adaptor.Agent) (*adaptor.Result, error)
	}{
		{
			name:   "slice",
			output: `[]`,
			run: func(agent *adaptor.Agent) (*adaptor.Result, error) {
				return agent.Run(context.Background(), "extract list", adaptor.WithSchema[recursiveList]())
			},
		},
		{
			name:   "map",
			output: `{}`,
			run: func(agent *adaptor.Agent) (*adaptor.Result, error) {
				return agent.Run(context.Background(), "extract map", adaptor.WithSchema[recursiveMap]())
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &structuredFake{caps: fullStructuredCaps(), output: tc.output}
			if _, err := tc.run(adaptor.New(fake)); err != nil {
				t.Fatalf("run: %v", err)
			}
			req := fake.lastRequest(t)
			if req.OutputSchema == nil || !strings.Contains(string(req.OutputSchema.SchemaJSON), `"$ref"`) {
				t.Fatalf("schema = %#v", req.OutputSchema)
			}
		})
	}
}

func TestWithSchemaRejectsSelfReferentialPointer(t *testing.T) {
	fake := &structuredFake{caps: fullStructuredCaps()}
	_, err := adaptor.New(fake).Run(context.Background(), "extract", adaptor.WithSchema[recursivePointer]())
	if !errors.Is(err, adaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "self-referential pointer") {
		t.Fatalf("error = %v, want self-referential pointer ErrInvalidOutputSchema", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestWithSchemaRejectsSelfReferentialMapKeyPointer(t *testing.T) {
	fake := &structuredFake{caps: fullStructuredCaps()}
	_, err := adaptor.New(fake).Run(context.Background(), "extract", adaptor.WithSchema[recursivePointerMap]())
	if !errors.Is(err, adaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "self-referential map key") {
		t.Fatalf("error = %v, want self-referential map key ErrInvalidOutputSchema", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestWithSchemaRejectsUnsupportedTypes(t *testing.T) {
	type invalid struct {
		Done chan struct{} `json:"done"`
	}
	fake := &structuredFake{caps: fullStructuredCaps()}
	_, err := adaptor.New(fake).Run(context.Background(), "extract", adaptor.WithSchema[invalid]())
	if !errors.Is(err, adaptor.ErrInvalidOutputSchema) {
		t.Fatalf("expected ErrInvalidOutputSchema, got %v", err)
	}
	var typed *adaptor.InvalidOutputSchemaError
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want *InvalidOutputSchemaError in the chain", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestWithSchemaPreservesLargeConstraintNumbers(t *testing.T) {
	// SchemaReturnInvalid keeps the run alive regardless of how the local
	// validator treats the extreme constraint — the assertion is that the
	// generated document preserves the integer
	// verbatim on its way to the driver.
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false
	fake := &structuredFake{caps: caps, output: `{"id":9007199254740993}`}
	_, err := adaptor.New(fake).Run(context.Background(), "extract",
		adaptor.WithSchema[largeSchemaNumber](adaptor.SchemaReturnInvalid()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	req := fake.lastRequest(t)
	if req.OutputSchema == nil || !strings.Contains(string(req.OutputSchema.SchemaJSON), `"minimum":9007199254740993`) {
		t.Fatalf("large schema number was not preserved: %#v", req.OutputSchema)
	}
}

func TestStructuredOutputRejectsInvalidSchemaBeforeLaunch(t *testing.T) {
	fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	agent := adaptor.New(fake)

	_, err := agent.Run(context.Background(), "extract", adaptor.WithSchemaJSON([]byte(`{`)))
	if !errors.Is(err, adaptor.ErrInvalidOutputSchema) {
		t.Fatalf("expected ErrInvalidOutputSchema, got %v", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestStructuredOutputRejectsUnsupportedBeforeLaunch(t *testing.T) {
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false
	caps.JSONSchemaPromptValidate = false
	fake := &structuredFake{caps: caps, output: `{"project_name":"agent-adaptor"}`}
	agent := adaptor.New(fake)

	_, err := agent.Run(context.Background(), "extract", adaptor.WithSchema[projectMetadata]())
	if !errors.Is(err, adaptor.ErrStructuredOutputUnsupported) {
		t.Fatalf("expected ErrStructuredOutputUnsupported, got %v", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

func TestStructuredSchemaFallsBackToCompatibleProviderTransport(t *testing.T) {
	caps := fullStructuredCaps()
	caps.WorksWithStreaming = false
	fake := &structuredFake{caps: caps, output: `{"project_name":"agent-adaptor"}`}
	agent := adaptor.New(fake)

	res, err := agent.Stream(context.Background(), "extract", adaptor.WithSchema[projectMetadata]()).Result()
	if err != nil {
		t.Fatalf("Stream.Result: %v", err)
	}
	if res == nil {
		t.Fatal("missing Result")
	}
	if req := fake.lastRequest(t); req.Streaming {
		t.Fatalf("request selected provider streaming despite schema capability %+v", caps)
	}
}

func TestStructuredOutputFallsBackToPromptValidation(t *testing.T) {
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false
	fake := &structuredFake{caps: caps, output: `{"project_name":"agent-adaptor","programming_languages":["go"]}`}
	agent := adaptor.New(fake)

	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"},"programming_languages":{"type":"array","items":{"type":"string"}}},"required":["project_name"],"additionalProperties":false}`)
	opt := adaptor.WithSchemaJSON(schema, adaptor.SchemaName("project_metadata"))
	schema[0] = '[' // prove WithSchemaJSON made its own copy.

	res, err := agent.Run(context.Background(), "extract metadata", opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	req := fake.lastRequest(t)
	if !strings.Contains(req.Prompt, "Return only a single JSON value") || !strings.Contains(req.Prompt, "project_metadata") {
		t.Fatalf("prompt-validation instructions were not injected: %q", req.Prompt)
	}
	if !strings.HasSuffix(req.Prompt, "extract metadata") {
		t.Errorf("original prompt must trail the injected instructions, got %q", req.Prompt)
	}
	if req.OutputSchema == nil || len(req.OutputSchema.SchemaJSON) == 0 || req.OutputSchema.SchemaJSON[0] != '{' {
		t.Fatalf("expected deep-copied schema on the driver request, got %#v", req.OutputSchema)
	}
	if req.StructuredOutputSource != driver.StructuredOutputSourcePromptValidate {
		t.Fatalf("resolved source = %q, want automatic prompt-validation fallback", req.StructuredOutputSource)
	}
	// The fake emits no native structured output on the fallback path,
	// so a successful Decode proves the SDK validated the raw text itself.
	var decoded projectMetadata
	if err := res.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ProjectName != "agent-adaptor" {
		t.Errorf("unexpected decoded result: %#v", decoded)
	}
}

func TestStructuredOutputFallbackFailurePolicies(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"],"additionalProperties":false}`)
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false

	t.Run("default fail-run policy", func(t *testing.T) {
		// Code fences around the JSON: the fallback path demands an exact
		// JSON document, so this output is invalid and the default policy
		// fails the run as a policy violation.
		fake := &structuredFake{caps: caps, output: "```json\n{\"project_name\":\"agent-adaptor\"}\n```"}
		agent := adaptor.New(fake)

		_, err := agent.Run(context.Background(), "extract", adaptor.WithSchemaJSON(schema))
		if !errors.Is(err, adaptor.ErrPolicyViolation) {
			t.Fatalf("error = %v, want ErrPolicyViolation", err)
		}
		var re *adaptor.RunError
		if !errors.As(err, &re) {
			t.Fatalf("error = %v, want *RunError", err)
		}
		if re.Reason != adaptor.ReasonPolicyViolation {
			t.Errorf("reason = %q, want policy_violation", re.Reason)
		}
		if re.Result == nil {
			t.Fatal("RunError.Result missing — the completed-but-invalid run must stay auditable")
		}
		var decoded projectMetadata
		if decodeErr := re.Result.Decode(&decoded); decodeErr == nil || !strings.Contains(decodeErr.Error(), "structured output invalid") {
			t.Errorf("decode error = %v, want the invalid-structured-output contract", decodeErr)
		}
		if fake.runCount() != 1 {
			t.Errorf("driver ran %d time(s), want exactly one post-launch failure", fake.runCount())
		}
	})

	t.Run("SchemaReturnInvalid", func(t *testing.T) {
		// Type mismatch (42 for a string): invalid, but the run succeeds
		// and Decode reports the invalidity.
		fake := &structuredFake{caps: caps, output: `{"project_name":42}`}
		agent := adaptor.New(fake)

		res, err := agent.Run(context.Background(), "extract",
			adaptor.WithSchemaJSON(schema, adaptor.SchemaReturnInvalid()))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		var decoded projectMetadata
		if decodeErr := res.Decode(&decoded); decodeErr == nil || !strings.Contains(decodeErr.Error(), "structured output invalid") {
			t.Errorf("decode error = %v, want the invalid-structured-output contract", decodeErr)
		}
	})
}

func TestStructuredOutputIgnoredWithoutSchemaRequest(t *testing.T) {
	// The fake emits structured output nobody asked for, with RawJSON
	// deliberately different from Output. Decode must fall back to Text —
	// seeing "structured" here would mean unrequested output leaked.
	fake := &structuredFake{
		caps:            fullStructuredCaps(),
		output:          `{"from":"text"}`,
		structuredRaw:   `{"from":"structured"}`,
		forceStructured: true,
	}
	agent := adaptor.New(fake)

	res, err := agent.Run(context.Background(), "ordinary run")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var decoded map[string]string
	if err := res.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["from"] != "text" {
		t.Errorf("decoded %v — unrequested structured output leaked into the Result", decoded)
	}
}

func TestStructuredOutputFallbackPreservesLargeNumbers(t *testing.T) {
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false
	fake := &structuredFake{caps: caps, output: `{"id":9007199254740993}`}
	agent := adaptor.New(fake)
	schema := []byte(`{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"],"additionalProperties":false}`)

	res, err := agent.Run(context.Background(), "extract", adaptor.WithSchemaJSON(schema))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var decoded struct {
		ID json.Number `json:"id"`
	}
	if err := res.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ID.String() != "9007199254740993" {
		t.Errorf("large integer was not preserved: %s", decoded.ID)
	}
}

func TestRunAndStreamReturnEquivalentStructuredOutput(t *testing.T) {
	ctx := context.Background()
	caps := fullStructuredCaps()
	caps.JSONSchemaNative = false
	fake := &structuredFake{caps: caps, output: `{"project_name":"agent-adaptor"}`}
	agent := adaptor.New(fake)
	opt := adaptor.WithSchema[projectMetadata]()

	runRes, err := agent.Run(ctx, "extract", opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	stream := agent.Stream(ctx, "extract", opt)
	for range stream.Events() {
		// drain
	}
	streamRes, err := stream.Result()
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}

	var fromRun, fromStream projectMetadata
	if err := runRes.Decode(&fromRun); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if err := streamRes.Decode(&fromStream); err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if !reflect.DeepEqual(fromRun, fromStream) {
		t.Errorf("Run and Stream structured output diverged: run=%#v stream=%#v", fromRun, fromStream)
	}
}

// TestSchemaChangeDoesNotAffectThreadSessionFingerprint: the thread
// fingerprint folds the environment payload (skills/MCP/profile), never the
// output schema — changing schema metadata between runs must resume the same
// provider session.
func TestSchemaChangeDoesNotAffectThreadSessionFingerprint(t *testing.T) {
	ctx := context.Background()
	fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	store := memory.NewStore()
	agent := adaptor.New(fake, adaptor.WithThreadStore(store))

	const key = "company/issue-structured-output"
	if _, err := agent.Thread(key).Run(ctx, "extract metadata", adaptor.WithSchema[projectMetadata]()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	req1 := fake.request(t, 0)
	if req1.Session == nil || req1.Session.State != nil {
		t.Fatalf("first run session = %#v, want a fresh session request", req1.Session)
	}
	rec1 := activeRecord(t, store, key)

	if _, err := agent.Thread(key).Run(ctx, "extract metadata again", adaptor.WithSchema[projectMetadata](adaptor.SchemaName("renamed_schema"))); err != nil {
		t.Fatalf("second run: %v", err)
	}
	req2 := fake.request(t, 1)
	if req2.Session == nil || req2.Session.State == nil || req2.Session.State.ResumeID != "structured-session-1" {
		t.Fatalf("second run session = %#v, want a resume of structured-session-1 (schema changes must not split the session)", req2.Session)
	}
	rec2 := activeRecord(t, store, key)
	if rec2.ID != rec1.ID {
		t.Errorf("thread record changed %s → %s, want the same record across schema changes", rec1.ID, rec2.ID)
	}
}

// TestRunAsDecodesAcrossRunners: RunAs accepts any Runner — the stateless
// Agent and a Thread behave identically — and propagates pre-launch errors
// with a zero T.
func TestRunAsDecodesAcrossRunners(t *testing.T) {
	ctx := context.Background()

	t.Run("agent", func(t *testing.T) {
		fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor","programming_languages":["go"]}`}
		agent := adaptor.New(fake)
		decoded, res, err := adaptor.RunAs[projectMetadata](ctx, agent, "extract")
		if err != nil {
			t.Fatalf("RunAs: %v", err)
		}
		if res == nil {
			t.Fatal("RunAs returned no Result")
		}
		if decoded.ProjectName != "agent-adaptor" {
			t.Errorf("unexpected decoded value: %#v", decoded)
		}
	})

	t.Run("thread", func(t *testing.T) {
		fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
		agent := adaptor.New(fake, adaptor.WithThreadStore(memory.NewStore()))
		var runner adaptor.Runner = agent.Thread("company/run-as") // compile-time Runner proof
		decoded, _, err := adaptor.RunAs[projectMetadata](ctx, runner, "extract")
		if err != nil {
			t.Fatalf("RunAs: %v", err)
		}
		if decoded.ProjectName != "agent-adaptor" {
			t.Errorf("unexpected decoded value: %#v", decoded)
		}
	})

	t.Run("explicit schema options win over the implicit schema", func(t *testing.T) {
		fake := &structuredFake{caps: fullStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
		agent := adaptor.New(fake)
		if _, _, err := adaptor.RunAs[projectMetadata](ctx, agent, "extract", adaptor.WithSchema[projectMetadata](adaptor.SchemaName("explicit"))); err != nil {
			t.Fatalf("RunAs: %v", err)
		}
		if got := fake.lastRequest(t).OutputSchema; got == nil || got.Name != "explicit" {
			t.Errorf("schema = %#v, want the explicit SchemaName to win", got)
		}
	})

	t.Run("pre-launch error yields zero value", func(t *testing.T) {
		caps := fullStructuredCaps()
		caps.JSONSchemaNative = false
		caps.JSONSchemaPromptValidate = false
		fake := &structuredFake{caps: caps, output: `{"project_name":"agent-adaptor"}`}
		agent := adaptor.New(fake)
		decoded, _, err := adaptor.RunAs[projectMetadata](ctx, agent, "extract")
		if !errors.Is(err, adaptor.ErrStructuredOutputUnsupported) {
			t.Fatalf("error = %v, want ErrStructuredOutputUnsupported", err)
		}
		if !reflect.DeepEqual(decoded, projectMetadata{}) {
			t.Errorf("decoded = %#v, want the zero value on error", decoded)
		}
		if fake.runCount() != 0 {
			t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
		}
	})
}
