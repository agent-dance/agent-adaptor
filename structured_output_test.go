package agentadaptor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
)

type structuredTestDriver struct {
	caps                  agentadaptor.StructuredOutputCapability
	output                string
	forceStructuredOutput bool

	calls   int
	lastReq agentadaptor.DriverRunRequest

	sessionCounter int
}

func (d *structuredTestDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:             "structured-test",
		DisplayName:      "Structured Test",
		Sessions:         agentadaptor.SessionCapability{SupportsResume: true},
		StructuredOutput: d.caps,
	}
}

func (d *structuredTestDriver) ValidateConfig(any) error { return nil }

func (d *structuredTestDriver) Run(_ context.Context, req agentadaptor.DriverRunRequest, _ agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.calls++
	d.lastReq = req
	var checkpoint *agentadaptor.DriverCheckpoint
	if req.Session != nil {
		state := req.Session.State
		if state == nil || state.ResumeID == "" {
			d.sessionCounter++
			state = &agentadaptor.DriverSessionState{
				ResumeID:  fmt.Sprintf("structured-session-%d", d.sessionCounter),
				DisplayID: fmt.Sprintf("Structured Session %d", d.sessionCounter),
			}
		}
		checkpoint = &agentadaptor.DriverCheckpoint{State: state, Valid: true}
	}
	var structured *agentadaptor.StructuredOutput
	if d.forceStructuredOutput || (req.OutputSchema != nil && req.OutputSchema.Mode != agentadaptor.StructuredOutputPromptValidate) {
		mode := agentadaptor.StructuredOutputNativeStrict
		if req.OutputSchema != nil {
			mode = req.OutputSchema.Mode
		}
		structured = &agentadaptor.StructuredOutput{
			Format:  agentadaptor.OutputFormatJSONSchema,
			Mode:    mode,
			Source:  agentadaptor.StructuredOutputSourceNative,
			RawJSON: []byte(d.output),
			Valid:   true,
		}
	}
	return agentadaptor.DriverRunResult{
		Output:           d.output,
		RawStreams:       &agentadaptor.RawStreams{},
		ExitCode:         0,
		Checkpoint:       checkpoint,
		StructuredOutput: structured,
	}, nil
}

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

func TestJSONSchemaForAndDecodeStructuredOutput(t *testing.T) {
	first, err := agentadaptor.JSONSchemaFor[projectMetadata]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	second, err := agentadaptor.JSONSchemaFor[projectMetadata]()
	if err != nil {
		t.Fatalf("JSONSchemaFor second: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("expected deterministic schema bytes:\nfirst=%s\nsecond=%s", first, second)
	}
	if !strings.Contains(string(first), `"additionalProperties":false`) {
		t.Fatalf("expected strict object schema, got %s", first)
	}
	if !strings.Contains(string(first), `"project_name"`) {
		t.Fatalf("expected json tag name in schema, got %s", first)
	}

	decoded, err := agentadaptor.DecodeStructuredOutput[projectMetadata](agentadaptor.RunResult{
		StructuredOutput: &agentadaptor.StructuredOutput{
			Valid:   true,
			RawJSON: []byte(`{"project_name":"agent-adaptor","programming_languages":["go"]}`),
		},
	})
	if err != nil {
		t.Fatalf("DecodeStructuredOutput: %v", err)
	}
	if decoded.ProjectName != "agent-adaptor" || len(decoded.ProgrammingLanguages) != 1 || decoded.ProgrammingLanguages[0] != "go" {
		t.Fatalf("unexpected decoded value: %#v", decoded)
	}
}

func TestWithJSONSchemaOutputForPreservesGeneratedSchema(t *testing.T) {
	driver := &structuredTestDriver{
		caps:   promptStructuredCaps(),
		output: `{"project_name":"agent-adaptor","artifact":{"project_name":"nested","programming_languages":["go"]}}`,
	}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
	want, err := agentadaptor.JSONSchemaFor[nestedProjectMetadata]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}

	res, err := sdk.Run(context.Background(), "extract nested metadata", agentadaptor.WithJSONSchemaOutputFor[nestedProjectMetadata]())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StructuredOutput == nil || !res.StructuredOutput.Valid {
		t.Fatalf("expected valid structured output, got %#v", res.StructuredOutput)
	}
	if driver.lastReq.OutputSchema == nil {
		t.Fatal("expected output schema on driver request")
	}
	if got := string(driver.lastReq.OutputSchema.SchemaJSON); got != string(want) {
		t.Fatalf("WithJSONSchemaOutputFor changed the generated schema:\ngot  %s\nwant %s", got, want)
	}
}

func TestWithJSONSchemaOutputForSupportsRecursiveTypes(t *testing.T) {
	driver := &structuredTestDriver{
		caps:   promptStructuredCaps(),
		output: `{"project_name":"root","children":[]}`,
	}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
	schema, schemaErr := agentadaptor.JSONSchemaFor[recursiveProjectMetadata]()
	if schemaErr != nil {
		t.Fatalf("JSONSchemaFor: %v", schemaErr)
	}

	res, err := sdk.Run(context.Background(), "extract tree", agentadaptor.WithJSONSchemaOutputFor[recursiveProjectMetadata]())
	if err != nil {
		t.Fatalf("run: %v; schema=%s", err, schema)
	}
	if res.StructuredOutput == nil || !res.StructuredOutput.Valid {
		t.Fatalf("expected valid recursive structured output, got %#v", res.StructuredOutput)
	}
	if driver.lastReq.OutputSchema == nil || !strings.Contains(string(driver.lastReq.OutputSchema.SchemaJSON), `"$ref"`) {
		t.Fatalf("expected recursive schema references, got %#v", driver.lastReq.OutputSchema)
	}
}

func TestJSONSchemaForRejectsInliningRecursiveTypes(t *testing.T) {
	_, err := agentadaptor.JSONSchemaFor[recursiveProjectMetadata](agentadaptor.SchemaInlineReferences())
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "cannot inline recursive Go type") {
		t.Fatalf("error = %v, want recursive ErrInvalidOutputSchema", err)
	}
}

func TestWithJSONSchemaOutputForSupportsRecursiveCollections(t *testing.T) {
	tests := []struct {
		name string
		run  func(*structuredTestDriver) error
	}{
		{
			name: "slice",
			run: func(driver *structuredTestDriver) error {
				driver.output = `[]`
				sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
				_, err := sdk.Run(context.Background(), "extract list", agentadaptor.WithJSONSchemaOutputFor[recursiveList]())
				return err
			},
		},
		{
			name: "map",
			run: func(driver *structuredTestDriver) error {
				driver.output = `{}`
				sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
				_, err := sdk.Run(context.Background(), "extract map", agentadaptor.WithJSONSchemaOutputFor[recursiveMap]())
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			driver := &structuredTestDriver{caps: promptStructuredCaps()}
			if err := tc.run(driver); err != nil {
				t.Fatalf("run: %v", err)
			}
			if driver.lastReq.OutputSchema == nil || !strings.Contains(string(driver.lastReq.OutputSchema.SchemaJSON), `"$ref"`) {
				t.Fatalf("schema = %#v", driver.lastReq.OutputSchema)
			}
		})
	}
}

func TestJSONSchemaForRejectsSelfReferentialPointer(t *testing.T) {
	_, err := agentadaptor.JSONSchemaFor[recursivePointer]()
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "self-referential pointer") {
		t.Fatalf("error = %v, want self-referential pointer ErrInvalidOutputSchema", err)
	}
}

func TestJSONSchemaForRejectsSelfReferentialMapKeyPointer(t *testing.T) {
	_, err := agentadaptor.JSONSchemaFor[recursivePointerMap]()
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) || !strings.Contains(err.Error(), "self-referential map key") {
		t.Fatalf("error = %v, want self-referential map key ErrInvalidOutputSchema", err)
	}
}

func TestJSONSchemaForPreservesLargeConstraintNumbers(t *testing.T) {
	raw, err := agentadaptor.JSONSchemaFor[largeSchemaNumber]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	if !strings.Contains(string(raw), `"minimum":9007199254740993`) {
		t.Fatalf("large schema number was not preserved: %s", raw)
	}
}

func TestJSONSchemaForRejectsUnsupportedTypes(t *testing.T) {
	type invalid struct {
		Done chan struct{} `json:"done"`
	}
	_, err := agentadaptor.JSONSchemaFor[invalid]()
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) {
		t.Fatalf("expected ErrInvalidOutputSchema, got %v", err)
	}
}

func TestStructuredOutputRejectsInvalidSchemaBeforeAdapterLaunch(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))

	_, err := sdk.Run(context.Background(), "extract", agentadaptor.WithJSONSchemaOutput([]byte(`{`), agentadaptor.PromptValidateOutput()))
	if !errors.Is(err, agentadaptor.ErrInvalidOutputSchema) {
		t.Fatalf("expected ErrInvalidOutputSchema, got %v", err)
	}
	if driver.calls != 0 {
		t.Fatalf("adapter should not launch for invalid schema, got %d calls", driver.calls)
	}
}

func TestStructuredOutputRejectsUnsupportedNativeStrictBeforeAdapterLaunch(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	driver.caps.JSONSchemaNative = false
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))

	_, err := sdk.Run(context.Background(), "extract", agentadaptor.WithJSONSchemaOutputFor[projectMetadata]())
	if !errors.Is(err, agentadaptor.ErrStructuredOutputUnsupported) {
		t.Fatalf("expected ErrStructuredOutputUnsupported, got %v", err)
	}
	if driver.calls != 0 {
		t.Fatalf("adapter should not launch for unsupported native strict, got %d calls", driver.calls)
	}
}

func TestPromptValidateStructuredOutputPassesAndInjectsInstructions(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":"agent-adaptor","programming_languages":["go"]}`}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))

	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"},"programming_languages":{"type":"array","items":{"type":"string"}}},"required":["project_name"],"additionalProperties":false}`)
	opt := agentadaptor.WithJSONSchemaOutput(schema, agentadaptor.PromptValidateOutput(), agentadaptor.StructuredOutputName("project_metadata"))
	schema[0] = '[' // prove WithJSONSchemaOutput made its own copy.

	res, err := sdk.Run(context.Background(), "extract metadata", opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("unexpected failure: %#v", res.Failure)
	}
	if res.StructuredOutput == nil || !res.StructuredOutput.Valid || res.StructuredOutput.Source != agentadaptor.StructuredOutputSourcePromptValidate {
		t.Fatalf("unexpected structured output: %#v", res.StructuredOutput)
	}
	if !strings.Contains(driver.lastReq.Prompt, "Return only a single JSON value") || !strings.Contains(driver.lastReq.Prompt, "project_metadata") {
		t.Fatalf("prompt-validation instructions were not injected: %q", driver.lastReq.Prompt)
	}
	if driver.lastReq.OutputSchema == nil || driver.lastReq.OutputSchema.SchemaJSON[0] != '{' {
		t.Fatalf("expected deep-copied schema on DriverRunRequest, got %#v", driver.lastReq.OutputSchema)
	}
	decoded, err := agentadaptor.DecodeStructuredOutput[projectMetadata](res)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.ProjectName != "agent-adaptor" {
		t.Fatalf("unexpected decoded result: %#v", decoded)
	}
}

func TestPromptValidateStructuredOutputFailurePolicies(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"project_name":{"type":"string"}},"required":["project_name"],"additionalProperties":false}`)

	failDriver := &structuredTestDriver{caps: promptStructuredCaps(), output: "```json\n{\"project_name\":\"agent-adaptor\"}\n```"}
	failSDK := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(failDriver, struct{}{})))
	failRes, err := failSDK.Run(context.Background(), "extract", agentadaptor.WithJSONSchemaOutput(schema, agentadaptor.PromptValidateOutput()))
	if err != nil {
		t.Fatalf("run fail policy: %v", err)
	}
	if failRes.StructuredOutput == nil || failRes.StructuredOutput.Valid {
		t.Fatalf("expected invalid structured output, got %#v", failRes.StructuredOutput)
	}
	if failRes.Failure == nil || failRes.Failure.Code != agentadaptor.FailurePolicyError {
		t.Fatalf("expected policy failure, got %#v", failRes.Failure)
	}

	returnDriver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":42}`}
	returnSDK := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(returnDriver, struct{}{})))
	returnRes, err := returnSDK.Run(context.Background(), "extract", agentadaptor.WithJSONSchemaOutput(schema, agentadaptor.PromptValidateOutput(), agentadaptor.ReturnInvalidStructuredOutput()))
	if err != nil {
		t.Fatalf("run return invalid: %v", err)
	}
	if returnRes.StructuredOutput == nil || returnRes.StructuredOutput.Valid {
		t.Fatalf("expected invalid structured output, got %#v", returnRes.StructuredOutput)
	}
	if returnRes.Failure != nil {
		t.Fatalf("return_invalid must not mark run failed, got %#v", returnRes.Failure)
	}
}

func TestStructuredOutputIgnoredWithoutSchemaRequest(t *testing.T) {
	driver := &structuredTestDriver{
		caps:                  promptStructuredCaps(),
		output:                `{"project_name":"agent-adaptor"}`,
		forceStructuredOutput: true,
	}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))

	res, err := sdk.Run(context.Background(), "ordinary run")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.StructuredOutput != nil {
		t.Fatalf("ordinary runs must not expose unrequested structured output: %#v", res.StructuredOutput)
	}
}

func TestPromptValidateStructuredOutputPreservesLargeNumbers(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"id":9007199254740993}`}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
	schema := []byte(`{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}},"required":["id"],"additionalProperties":false}`)

	res, err := sdk.Run(context.Background(), "extract", agentadaptor.WithJSONSchemaOutput(schema, agentadaptor.PromptValidateOutput()))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Failure != nil {
		t.Fatalf("unexpected failure: %#v", res.Failure)
	}
	if res.StructuredOutput == nil || !res.StructuredOutput.Valid {
		t.Fatalf("expected valid large integer output, got %#v", res.StructuredOutput)
	}
	if string(res.StructuredOutput.RawJSON) != `{"id":9007199254740993}` {
		t.Fatalf("large integer was not preserved: %s", res.StructuredOutput.RawJSON)
	}
}

func TestRunAndStartWaitReturnEquivalentStructuredOutput(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})))
	opt := agentadaptor.WithJSONSchemaOutputFor[projectMetadata](agentadaptor.PromptValidateOutput())

	runRes, err := sdk.Run(context.Background(), "extract", opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	handle, err := sdk.Start(context.Background(), "extract", opt)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitRes, err := handle.Wait(context.Background())
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if runRes.StructuredOutput == nil || waitRes.StructuredOutput == nil {
		t.Fatalf("structured output missing: run=%#v wait=%#v", runRes.StructuredOutput, waitRes.StructuredOutput)
	}
	if string(runRes.StructuredOutput.RawJSON) != string(waitRes.StructuredOutput.RawJSON) ||
		runRes.StructuredOutput.Source != waitRes.StructuredOutput.Source ||
		runRes.StructuredOutput.Valid != waitRes.StructuredOutput.Valid {
		t.Fatalf("Run and Start().Wait structured output diverged:\nrun=%#v\nwait=%#v", runRes.StructuredOutput, waitRes.StructuredOutput)
	}
}

func TestStructuredOutputSchemaDoesNotAffectSessionFingerprint(t *testing.T) {
	driver := &structuredTestDriver{caps: promptStructuredCaps(), output: `{"project_name":"agent-adaptor"}`}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, struct{}{})),
		agentadaptor.WithSessionStore(memory.NewSessionStore()),
	)

	first, err := sdk.Run(
		context.Background(),
		"extract metadata",
		agentadaptor.WithSessionKey("company", "issue-structured-output"),
		agentadaptor.WithJSONSchemaOutputFor[projectMetadata](agentadaptor.NativeStrictOutput()),
	)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Session == nil || first.Session.ID == "" || !first.Session.Created {
		t.Fatalf("expected first run to create a session, got %#v", first.Session)
	}

	second, err := sdk.Run(
		context.Background(),
		"extract metadata again",
		agentadaptor.WithSessionKey("company", "issue-structured-output"),
		agentadaptor.WithJSONSchemaOutputFor[projectMetadata](agentadaptor.PromptValidateOutput()),
	)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Session == nil || second.Session.ID != first.Session.ID || !second.Session.Reused {
		t.Fatalf("expected structured-output mode/schema changes to reuse the same session: first=%#v second=%#v", first.Session, second.Session)
	}
	if driver.lastReq.Session == nil || driver.lastReq.Session.State == nil || driver.lastReq.Session.State.ResumeID == "" {
		t.Fatalf("expected second adapter request to carry resumable state, got %#v", driver.lastReq.Session)
	}
}

func promptStructuredCaps() agentadaptor.StructuredOutputCapability {
	return agentadaptor.StructuredOutputCapability{
		JSONSchemaNative:         true,
		JSONSchemaPromptValidate: true,
		WorksWithRun:             true,
		WorksWithStart:           true,
		WorksWithStreaming:       true,
		WorksWithHITL:            true,
	}
}
