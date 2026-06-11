package agentadaptor_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// modelCapturingDriver records the DriverRunRequest it last received so tests
// can assert how per-run options are threaded into the adapter.
type modelCapturingDriver struct {
	lastReq agentadaptor.DriverRunRequest
}

func (d *modelCapturingDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{Type: "model-capture", DisplayName: "Model Capture"}
}

func (d *modelCapturingDriver) ValidateConfig(any) error { return nil }

func (d *modelCapturingDriver) Run(_ context.Context, req agentadaptor.DriverRunRequest, _ agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.lastReq = req
	return agentadaptor.DriverRunResult{Output: "ok", ExitCode: 0}, nil
}

func newModelCaptureSDK(driver agentadaptor.DriverAdapter) agentadaptor.SDK {
	return agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "model"})),
	)
}

func TestWithModelThreadsModelOverride(t *testing.T) {
	driver := &modelCapturingDriver{}
	sdk := newModelCaptureSDK(driver)

	if _, err := sdk.Run(context.Background(), "hi", agentadaptor.WithModel("gpt-5.4")); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := driver.lastReq.ModelOverride; got != "gpt-5.4" {
		t.Fatalf("ModelOverride = %q, want gpt-5.4", got)
	}
}

func TestWithModelTrimsWhitespaceToNoOverride(t *testing.T) {
	driver := &modelCapturingDriver{}
	sdk := newModelCaptureSDK(driver)

	if _, err := sdk.Run(context.Background(), "hi", agentadaptor.WithModel("   ")); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := driver.lastReq.ModelOverride; got != "" {
		t.Fatalf("ModelOverride = %q, want empty for whitespace-only model", got)
	}
}

func TestRunWithoutWithModelLeavesOverrideEmpty(t *testing.T) {
	driver := &modelCapturingDriver{}
	sdk := newModelCaptureSDK(driver)

	if _, err := sdk.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := driver.lastReq.ModelOverride; got != "" {
		t.Fatalf("ModelOverride = %q, want empty when WithModel is not used", got)
	}
}
