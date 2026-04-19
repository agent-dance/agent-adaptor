package agentadaptor

import (
	"context"
	"testing"
)

type passthroughCodecDriver struct{}

func (passthroughCodecDriver) Descriptor() DriverDescriptor {
	return DriverDescriptor{Type: "passthrough", DisplayName: "Passthrough"}
}

func (passthroughCodecDriver) ValidateConfig(any) error { return nil }

func (passthroughCodecDriver) Run(_ context.Context, _ DriverRunRequest, _ EventSink) (DriverRunResult, error) {
	return DriverRunResult{}, nil
}

func TestSessionCodecForFallsBackToPassthrough(t *testing.T) {
	driver := passthroughCodecDriver{}
	codec := SessionCodecFor(driver)
	state := &DriverSessionState{
		ResumeID: "resume-1",
		Data: map[string]string{
			SessionParamCWD: "C:/workspace",
		},
	}

	params := codec.ToParams(state)
	if params.ResumeID != "resume-1" {
		t.Fatalf("expected resume id to round-trip, got %#v", params)
	}
	if params.DisplayID != "resume-1" {
		t.Fatalf("expected display id to default to resume id, got %#v", params)
	}
	if params.Values[SessionParamCWD] != "C:/workspace" {
		t.Fatalf("expected session values to round-trip, got %#v", params.Values)
	}

	restored := codec.FromParams(params)
	if restored == nil || restored.ResumeID != state.ResumeID || restored.DisplayID != state.ResumeID {
		t.Fatalf("expected restored state, got %#v", restored)
	}

	firstGuard := codec.GuardFingerprint(params)
	secondGuard := codec.GuardFingerprint(codec.ToParams(restored))
	if firstGuard == "" || firstGuard != secondGuard {
		t.Fatalf("expected stable guard fingerprint, got %q and %q", firstGuard, secondGuard)
	}
}
