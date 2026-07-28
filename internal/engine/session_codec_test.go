package engine_test

import (
	"context"
	"maps"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

type statelessToolDriver struct{}

func (statelessToolDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "stateless-tool"}
}
func (statelessToolDriver) ValidateConfig(any) error { return nil }
func (statelessToolDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{}, nil
}

// SessionCodecFor remains a permissive SPI inspection utility for callers
// that are not opening a Thread. Public Thread coordination has a separate,
// strict resume-capability gate and cannot reach this fallback.
func TestSessionCodecForStatelessToolingKeepsPassthroughRoundTrip(t *testing.T) {
	codec := engine.SessionCodecFor(statelessToolDriver{})
	state := &driver.SessionState{ResumeID: "resume-1", Data: map[string]string{"cwd": "/repo"}}
	params := codec.ToParams(state)
	if params.ResumeID != state.ResumeID || params.DisplayID != state.ResumeID || !maps.Equal(params.Values, state.Data) {
		t.Fatalf("params = %+v, want a lossless pass-through with DisplayID defaulting", params)
	}
	restored := codec.FromParams(params)
	if restored == nil || restored.ResumeID != state.ResumeID || !maps.Equal(restored.Data, state.Data) {
		t.Fatalf("restored = %+v, want the original state", restored)
	}
}
