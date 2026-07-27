package codebuddy

import (
	"context"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestDriverReturnsNonNilDriver(t *testing.T) {
	if Driver(Config{}) == nil {
		t.Fatal("Driver(Config{}) returned nil")
	}
}

func TestDriverDescriptorMatchesAdapterImplementation(t *testing.T) {
	want := (adapter{}).Descriptor()
	if got := Driver(Config{Model: "claude-sonnet-5"}).Descriptor(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Driver(cfg).Descriptor() = %+v\nwant adapter descriptor %+v", got, want)
	}
}

func TestDriverInjectsCapturedConfigIntoRunRequest(t *testing.T) {
	cfg := Config{
		CommonConfig: CommonConfig{
			Command:   "custom-codebuddy",
			CWD:       "/workspaces/repo",
			ExtraArgs: []string{"--verbose"},
		},
		Model:          "claude-sonnet-5",
		Effort:         "high",
		PermissionMode: PermissionPlan,
		MaxTurnsPerRun: 5,
	}
	d, ok := Driver(cfg).(configuredDriver)
	if !ok {
		t.Fatalf("Driver returned %T, want configuredDriver", Driver(cfg))
	}

	req := d.requestWithConfig(driver.Request{})
	injected, ok := req.Config.(Config)
	if !ok {
		t.Fatalf("req.Config is %T, want codebuddy.Config", req.Config)
	}
	if !reflect.DeepEqual(injected, cfg) {
		t.Fatalf("injected config = %+v, want %+v", injected, cfg)
	}

	// An explicit request-level package config supplied by a direct SPI caller
	// wins untouched.
	override := Config{Model: "gpt-5.5"}
	req = d.requestWithConfig(driver.Request{Config: override})
	if !reflect.DeepEqual(req.Config, override) {
		t.Fatalf("explicit req.Config was overwritten: %+v", req.Config)
	}
}

func TestConfiguredDriverProbesUseCapturedConfigAndRespectExplicitOverride(t *testing.T) {
	probe := Driver(Config{Model: "captured-model"}).(driver.ModelDetector)
	got, err := probe.DetectModel(context.Background(), nil, nil)
	if err != nil || got == nil || got.Model != "captured-model" {
		t.Fatalf("DetectModel(nil) = (%+v, %v), want captured-model", got, err)
	}
	override, err := probe.DetectModel(context.Background(), Config{Model: "override-model"}, nil)
	if err != nil || override == nil || override.Model != "override-model" {
		t.Fatalf("DetectModel(override) = (%+v, %v), want override-model", override, err)
	}
}

func TestDriverCapturedConfigIsMutationIsolated(t *testing.T) {
	nested := map[string]any{"value": "original"}
	cfg := Config{CommonConfig: CommonConfig{
		Env: []driver.EnvBinding{{Name: "KEY", Value: "original"}}, ExtraArgs: []string{"original"},
		Instructions:      &driver.InstructionsBundleRef{ID: "original", Native: map[string]any{"nested": nested}},
		WorkspaceStrategy: &driver.WorkspaceStrategy{BaseRef: "original"},
		WorkspaceRuntime:  &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{ID: "original", Metadata: map[string]string{"key": "original"}}}},
	}, Model: "original-model", Effort: "high", PermissionMode: PermissionPlan, MaxTurnsPerRun: 7}
	d := Driver(cfg).(configuredDriver)
	cfg.Env[0].Value, cfg.ExtraArgs[0], cfg.Instructions.ID = "mutated", "mutated", "mutated"
	nested["value"] = "mutated"
	cfg.WorkspaceStrategy.BaseRef = "mutated"
	cfg.WorkspaceRuntime.Services[0].ID = "mutated"
	cfg.WorkspaceRuntime.Services[0].Metadata["key"] = "mutated"
	cfg.Model, cfg.Effort, cfg.PermissionMode, cfg.MaxTurnsPerRun = "mutated-model", "low", PermissionAuto, 1

	captured := d.requestWithConfig(driver.Request{}).Config.(Config)
	assertConfigSnapshot(t, captured)
	captured.Env[0].Value = "mutated-by-consumer"
	captured.Instructions.Native["nested"].(map[string]any)["value"] = "mutated-by-consumer"
	assertConfigSnapshot(t, d.requestWithConfig(driver.Request{}).Config.(Config))
}

func assertConfigSnapshot(t *testing.T, got Config) {
	t.Helper()
	if got.Model != "original-model" || got.Effort != "high" || got.PermissionMode != PermissionPlan || got.MaxTurnsPerRun != 7 || got.Env[0].Value != "original" || got.ExtraArgs[0] != "original" || got.Instructions.ID != "original" || got.Instructions.Native["nested"].(map[string]any)["value"] != "original" || got.WorkspaceStrategy.BaseRef != "original" || got.WorkspaceRuntime.Services[0].ID != "original" || got.WorkspaceRuntime.Services[0].Metadata["key"] != "original" {
		t.Fatalf("captured Config was mutated through caller-owned data: %#v", got)
	}
}

func TestDriverValidateConfigStaysDeferredAndUsesCapturedConfig(t *testing.T) {
	if err := Driver(Config{}).ValidateConfig(nil); err != nil {
		t.Fatalf("ValidateConfig(nil) = %v, want nil (captured config)", err)
	}
	if err := Driver(Config{}).ValidateConfig(42); err == nil {
		t.Fatal("ValidateConfig(42) = nil, want type error")
	}
	// Invalid captured config surfaces on probe, not at construction:
	// Driver itself must not panic or reject eagerly.
	bad := Driver(Config{PermissionMode: "bogus"})
	if err := bad.ValidateConfig(nil); err == nil {
		t.Fatal("ValidateConfig(nil) = nil for invalid captured PermissionMode, want error")
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
	if _, ok := d.(driver.SkillSupport); !ok {
		t.Error("driver.SkillSupport lost on v1 driver")
	}
	if _, ok := d.(driver.SessionCodecProvider); !ok {
		t.Error("driver.SessionCodecProvider lost on v1 driver")
	}
	if _, ok := d.(driver.StreamSupport); !ok {
		t.Error("driver.StreamSupport lost on v1 driver")
	}
	if _, ok := d.(engine.ProfileResourceDriver); !ok {
		t.Error("profile resource capability lost on v1 driver")
	}
}
