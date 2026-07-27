package claude

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestDriverReturnsNonNilDriver(t *testing.T) {
	if Driver(Config{}) == nil {
		t.Fatal("Driver(Config{}) returned nil")
	}
}

func TestDriverDescriptorMatchesPackageAdapter(t *testing.T) {
	cfg := Config{Model: "claude-fable-5"}
	want := adapter{}.Descriptor()
	if got := Driver(cfg).Descriptor(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Driver(cfg).Descriptor() = %+v\nwant adapter descriptor %+v", got, want)
	}
}

func TestDriverInjectsCapturedConfigIntoRunRequest(t *testing.T) {
	cfg := Config{
		CommonConfig: CommonConfig{
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

	// An explicit request-level config wins untouched.
	override := Config{Model: "claude-sonnet-5"}
	req = d.requestWithConfig(driver.Request{Config: override})
	if !reflect.DeepEqual(req.Config, override) {
		t.Fatalf("explicit req.Config was overwritten: %+v", req.Config)
	}
}

func TestConfigMapsEveryCommonFieldToEngine(t *testing.T) {
	instructions := &driver.InstructionsBundleRef{ID: "instructions"}
	common := CommonConfig{
		Command:                 "claude-bin",
		CWD:                     "/repo",
		Env:                     []driver.EnvBinding{{Name: "KEY", Value: "value"}},
		Instructions:            instructions,
		PromptTemplate:          "prompt {{.Prompt}}",
		BootstrapPromptTemplate: "bootstrap {{.Prompt}}",
		WorkspaceStrategy:       &driver.WorkspaceStrategy{Type: driver.WorkspaceStrategyGitWorktree, BaseRef: "main", BranchTemplate: "run/{{.RunID}}", WorktreeParentDir: "/worktrees"},
		WorkspaceRuntime:        &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{ID: "svc", Name: "service"}}},
		Timeout:                 2 * time.Minute,
		GracePeriod:             3 * time.Second,
		ExtraArgs:               []string{"--verbose"},
	}
	want := engine.CommonConfig{
		Command:                 common.Command,
		CWD:                     common.CWD,
		Env:                     common.Env,
		Instructions:            instructions,
		PromptTemplate:          common.PromptTemplate,
		BootstrapPromptTemplate: common.BootstrapPromptTemplate,
		WorkspaceStrategy:       &engine.WorkspaceStrategy{Type: driver.WorkspaceStrategyGitWorktree, BaseRef: "main", BranchTemplate: "run/{{.RunID}}", WorktreeParentDir: "/worktrees"},
		WorkspaceRuntime:        &engine.WorkspaceRuntimeConfig{Services: common.WorkspaceRuntime.Services},
		Timeout:                 common.Timeout,
		GracePeriod:             common.GracePeriod,
		ExtraArgs:               common.ExtraArgs,
	}
	if got := (Config{CommonConfig: common}).engineConfig().CommonConfig; !reflect.DeepEqual(got, want) {
		t.Fatalf("engine common config = %#v, want %#v", got, want)
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

func TestDriverCapturedConfigAndEngineConversionAreMutationIsolated(t *testing.T) {
	nested := map[string]any{"value": "original"}
	cfg := Config{CommonConfig: CommonConfig{
		Env:                     []driver.EnvBinding{{Name: "KEY", Value: "original"}},
		Instructions:            &driver.InstructionsBundleRef{ID: "original", Native: map[string]any{"nested": nested}},
		WorkspaceStrategy:       &driver.WorkspaceStrategy{BaseRef: "original"},
		WorkspaceRuntime:        &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{ID: "original", Metadata: map[string]string{"key": "original"}}}},
		ExtraArgs:               []string{"original"},
		PromptTemplate:          "original",
		BootstrapPromptTemplate: "original",
	}, Model: "original-model", Effort: "high", MaxTurnsPerRun: 7}
	d := Driver(cfg).(configuredDriver)
	converted := cfg.engineConfig()

	cfg.Env[0].Value = "mutated"
	cfg.ExtraArgs[0] = "mutated"
	cfg.Instructions.ID = "mutated"
	nested["value"] = "mutated"
	cfg.WorkspaceStrategy.BaseRef = "mutated"
	cfg.WorkspaceRuntime.Services[0].ID = "mutated"
	cfg.WorkspaceRuntime.Services[0].Metadata["key"] = "mutated"
	cfg.Model, cfg.Effort, cfg.MaxTurnsPerRun = "mutated-model", "low", 1

	captured := d.requestWithConfig(driver.Request{}).Config.(Config)
	assertConfigSnapshot(t, captured)
	assertEngineCommonConfigSnapshot(t, converted)

	captured.Env[0].Value = "mutated-by-consumer"
	captured.Instructions.Native["nested"].(map[string]any)["value"] = "mutated-by-consumer"
	assertConfigSnapshot(t, d.requestWithConfig(driver.Request{}).Config.(Config))
}

func assertConfigSnapshot(t *testing.T, got Config) {
	t.Helper()
	if got.Model != "original-model" || got.Effort != "high" || got.MaxTurnsPerRun != 7 || got.Env[0].Value != "original" || got.ExtraArgs[0] != "original" || got.Instructions.ID != "original" || got.Instructions.Native["nested"].(map[string]any)["value"] != "original" || got.WorkspaceStrategy.BaseRef != "original" || got.WorkspaceRuntime.Services[0].ID != "original" || got.WorkspaceRuntime.Services[0].Metadata["key"] != "original" {
		t.Fatalf("captured Config was mutated through caller-owned data: %#v", got)
	}
}

func assertEngineCommonConfigSnapshot(t *testing.T, got engine.ClaudeConfig) {
	t.Helper()
	if got.Model != "original-model" || got.Effort != "high" || got.MaxTurnsPerRun != 7 || got.Env[0].Value != "original" || got.ExtraArgs[0] != "original" || got.Instructions.ID != "original" || got.Instructions.Native["nested"].(map[string]any)["value"] != "original" || got.WorkspaceStrategy.BaseRef != "original" || got.WorkspaceRuntime.Services[0].ID != "original" || got.WorkspaceRuntime.Services[0].Metadata["key"] != "original" {
		t.Fatalf("engine Config was mutated through source data: %#v", got)
	}
}

func TestDriverValidateConfigStaysDeferredAndUsesCapturedConfig(t *testing.T) {
	if err := Driver(Config{}).ValidateConfig(nil); err != nil {
		t.Fatalf("ValidateConfig(nil) = %v, want nil (captured config)", err)
	}
	if err := Driver(Config{}).ValidateConfig(42); err == nil {
		t.Fatal("ValidateConfig(42) = nil, want provider Config type error")
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
	if _, ok := d.(engine.ProfileResourceDriver); !ok {
		t.Error("profile resource capability lost on v1 driver")
	}
}
