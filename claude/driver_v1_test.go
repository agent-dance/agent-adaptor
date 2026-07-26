package claude

import (
	"reflect"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestDriverReturnsNonNilDriver(t *testing.T) {
	if Driver(Config{}) == nil {
		t.Fatal("Driver(Config{}) returned nil")
	}
}

func TestDriverDescriptorMatchesLegacyEntryPoints(t *testing.T) {
	cfg := Config{Model: "claude-fable-5"}
	want := New(cfg).Adapter().Descriptor()
	if got := Driver(cfg).Descriptor(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Driver(cfg).Descriptor() = %+v\nwant legacy binding descriptor %+v", got, want)
	}
	if got := NewAdapter().Descriptor(); !reflect.DeepEqual(got, want) {
		t.Fatalf("NewAdapter().Descriptor() = %+v\nwant %+v", got, want)
	}
}

func TestDriverInjectsCapturedConfigIntoRunRequest(t *testing.T) {
	cfg := Config{
		CommonConfig: agentadaptor.CommonConfig{
			Command:   "custom-claude",
			CWD:       "/workspaces/repo",
			ExtraArgs: []string{"--verbose"},
		},
		Model:          "claude-fable-5",
		Effort:         "high",
		MaxTurnsPerRun: 7,
	}
	d, ok := Driver(cfg).(configuredDriver)
	if !ok {
		t.Fatalf("Driver returned %T, want configuredDriver", Driver(cfg))
	}

	req := d.requestWithConfig(driver.Request{})
	injected, ok := req.Config.(Config)
	if !ok {
		t.Fatalf("req.Config is %T, want claude.Config", req.Config)
	}
	if !reflect.DeepEqual(injected, cfg) {
		t.Fatalf("injected config = %+v, want %+v", injected, cfg)
	}

	// Parity with the legacy entry point: New captures the identical value.
	if legacy := New(cfg).TypedConfig(); !reflect.DeepEqual(legacy, cfg) {
		t.Fatalf("legacy binding config = %+v, want %+v", legacy, cfg)
	}

	// An explicit request-level config (legacy binding path) wins untouched.
	override := Config{Model: "claude-sonnet-5"}
	req = d.requestWithConfig(driver.Request{Config: override})
	if !reflect.DeepEqual(req.Config, override) {
		t.Fatalf("explicit req.Config was overwritten: %+v", req.Config)
	}
}

func TestDriverValidateConfigStaysDeferredAndUsesCapturedConfig(t *testing.T) {
	if err := Driver(Config{}).ValidateConfig(nil); err != nil {
		t.Fatalf("ValidateConfig(nil) = %v, want nil (captured config)", err)
	}
	if err := Driver(Config{}).ValidateConfig(42); err == nil {
		t.Fatal("ValidateConfig(42) = nil, want type error (legacy semantics)")
	}
}

func TestDriverPreservesOptionalCapabilityInterfaces(t *testing.T) {
	d := Driver(Config{})
	if _, ok := d.(driver.EnvironmentProbe); !ok {
		t.Error("driver.EnvironmentProbe lost on v1 driver")
	}
	if _, ok := d.(driver.ModelLister); !ok {
		t.Error("driver.ModelLister lost on v1 driver")
	}
	if _, ok := d.(driver.ModelDetector); !ok {
		t.Error("driver.ModelDetector lost on v1 driver")
	}
	if _, ok := d.(driver.ProfileReporter); !ok {
		t.Error("driver.ProfileReporter lost on v1 driver")
	}
	if _, ok := d.(driver.ConfigSchemaProvider); !ok {
		t.Error("driver.ConfigSchemaProvider lost on v1 driver")
	}
	if _, ok := d.(driver.QuotaProbe); !ok {
		t.Error("driver.QuotaProbe lost on v1 driver")
	}
	if _, ok := d.(driver.SkillSupport); !ok {
		t.Error("driver.SkillSupport lost on v1 driver")
	}
	if _, ok := d.(driver.SessionCodecProvider); !ok {
		t.Error("driver.SessionCodecProvider lost on v1 driver")
	}
	if _, ok := d.(driver.StreamSupport); !ok {
		t.Error("driver.StreamSupport lost on v1 driver")
	}
}
