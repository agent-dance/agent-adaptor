package agentadaptor_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type runtimeAdminDriver struct {
	lastReq agentadaptor.DriverRunRequest
}

func (d *runtimeAdminDriver) Descriptor() agentadaptor.DriverDescriptor {
	return agentadaptor.DriverDescriptor{
		Type:        "runtime-admin",
		DisplayName: "Runtime Admin",
		ConfigSchema: &agentadaptor.ConfigSchema{
			Fields: []agentadaptor.ConfigField{
				{Name: "label", Type: "text", Description: "Example field"},
			},
		},
		Runtime: agentadaptor.RuntimeCapability{ReportsServices: true},
	}
}

func (d *runtimeAdminDriver) ValidateConfig(cfg any) error {
	switch cfg.(type) {
	case fakeConfig, *fakeConfig:
		return nil
	default:
		return nil
	}
}

func (d *runtimeAdminDriver) Run(_ context.Context, req agentadaptor.DriverRunRequest, _ agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
	d.lastReq = req
	cost := 1.25
	return agentadaptor.DriverRunResult{
		Output:      "runtime-ok",
		ExitCode:    0,
		Provider:    "example-provider",
		Biller:      "example-biller",
		Model:       "example-model",
		BillingType: "subscription",
		CostUSD:     &cost,
		Summary:     "runtime summary",
		Result: map[string]any{
			"ok": true,
		},
	}, nil
}

func (d *runtimeAdminDriver) CheckEnvironment(_ context.Context, _ any) (agentadaptor.EnvironmentReport, error) {
	return agentadaptor.EnvironmentReport{
		DriverType: "runtime-admin",
		Status:     agentadaptor.EnvironmentPass,
		Healthy:    true,
		Summary:    "all checks passed",
		Checks: []agentadaptor.EnvironmentCheck{
			{Code: "command_found", Level: "info", Message: "ready"},
		},
	}, nil
}

func (d *runtimeAdminDriver) DetectModel(_ context.Context, _ any) (*agentadaptor.DetectedModel, error) {
	return &agentadaptor.DetectedModel{
		Model:      "example-model",
		Provider:   "example-provider",
		Source:     "test",
		Candidates: []string{"example-model"},
	}, nil
}

func (d *runtimeAdminDriver) GetProfile(_ context.Context, _ any, _ agentadaptor.AgentIdentity) (agentadaptor.AgentProfile, error) {
	return agentadaptor.AgentProfile{
		DriverType: "runtime-admin",
		Supported:  true,
		Dir:        "C:/profiles/runtime-admin",
		EnvVar:     "RUNTIME_ADMIN_HOME",
		Source:     agentadaptor.AgentProfileSourceDefault,
	}, nil
}

func (d *runtimeAdminDriver) ConfigSchema(_ context.Context, _ any) (*agentadaptor.ConfigSchema, error) {
	return d.Descriptor().ConfigSchema, nil
}

func (d *runtimeAdminDriver) GetQuota(_ context.Context, _ any) (agentadaptor.QuotaReport, error) {
	used := 80
	return agentadaptor.QuotaReport{
		DriverType: "runtime-admin",
		Provider:   "example-provider",
		Source:     "test",
		Available:  true,
		Windows: []agentadaptor.QuotaWindow{
			{Label: "24h", UsedPercent: &used, ValueLabel: "80% used"},
		},
	}, nil
}

type observingRuntimeManager struct {
	requests []agentadaptor.RuntimeServiceRequest
	released []string
}

func (m *observingRuntimeManager) Ensure(_ context.Context, req agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	m.requests = append(m.requests, req)
	return []agentadaptor.RuntimeServiceRef{
		{
			ID:       "svc-db",
			Name:     "db",
			URL:      "http://127.0.0.1:15432",
			Status:   agentadaptor.RuntimeServiceRunning,
			Health:   agentadaptor.RuntimeHealthHealthy,
			Metadata: map[string]string{"manager": "test"},
		},
	}, nil
}

func (m *observingRuntimeManager) ReleaseByRun(_ context.Context, runID string) error {
	m.released = append(m.released, runID)
	return nil
}

func TestRuntimeServicesFlowThroughSingleExecutionPath(t *testing.T) {
	driver := &runtimeAdminDriver{}
	runtimeManager := &observingRuntimeManager{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{ID: "agent-runtime"}),
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
				ID:          "db",
				Name:        "db",
				URL:         "postgres://workspace-db",
				Description: "Workspace database",
				Lifecycle:   agentadaptor.RuntimeLifecycleEphemeral,
				ReuseKey:    "workspace-db",
				Command:     "docker compose up db",
				CWD:         "C:/services",
				Port:        15432,
				Metadata:    map[string]string{"source": "binding"},
			}),
		)),
		agentadaptor.WithRuntimeServiceManager(runtimeManager),
	)

	result, err := sdk.Run(context.Background(), "use the runtime")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.RunID == "" {
		t.Fatal("expected run id")
	}
	if len(runtimeManager.requests) != 1 {
		t.Fatalf("expected 1 runtime ensure request, got %d", len(runtimeManager.requests))
	}
	if len(runtimeManager.released) != 1 || runtimeManager.released[0] != result.RunID {
		t.Fatalf("expected release for run %q, got %#v", result.RunID, runtimeManager.released)
	}
	if len(driver.lastReq.Runtime.Requested) != 1 || driver.lastReq.Runtime.Requested[0].Name != "db" {
		t.Fatalf("expected requested runtime service in driver request, got %#v", driver.lastReq.Runtime)
	}
	if len(driver.lastReq.Runtime.Ensured) != 1 || driver.lastReq.Runtime.Ensured[0].URL != "http://127.0.0.1:15432" {
		t.Fatalf("expected ensured runtime service in driver request, got %#v", driver.lastReq.Runtime)
	}
	if driver.lastReq.Runtime.Ensured[0].Lifecycle != agentadaptor.RuntimeLifecycleEphemeral ||
		driver.lastReq.Runtime.Ensured[0].ReuseKey != "workspace-db" ||
		driver.lastReq.Runtime.Ensured[0].OwnerAgentID != "agent-runtime" ||
		driver.lastReq.Runtime.Ensured[0].Health != agentadaptor.RuntimeHealthHealthy {
		t.Fatalf("expected normalized runtime lifecycle fields, got %#v", driver.lastReq.Runtime.Ensured[0])
	}
	if len(result.RuntimeServices) != 1 || result.RuntimeServices[0].URL != "http://127.0.0.1:15432" {
		t.Fatalf("expected runtime services in run result, got %#v", result.RuntimeServices)
	}
	if result.RuntimeServices[0].Lifecycle != agentadaptor.RuntimeLifecycleEphemeral ||
		result.RuntimeServices[0].ReuseKey != "workspace-db" ||
		result.RuntimeServices[0].Command != "docker compose up db" ||
		result.RuntimeServices[0].Port != 15432 ||
		result.RuntimeServices[0].OwnerAgentID != "agent-runtime" {
		t.Fatalf("expected richer runtime reports, got %#v", result.RuntimeServices[0])
	}
	if result.Provider != "example-provider" || result.Model != "example-model" || result.CostUSD == nil {
		t.Fatalf("expected richer run result fields, got %#v", result)
	}
}

func TestAdminExposesConfigSchemaQuotaAndDetectedModel(t *testing.T) {
	driver := &runtimeAdminDriver{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"})),
	)

	admin := sdk.Admin().Default()
	schema, err := admin.ConfigSchema(context.Background())
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	if schema == nil || len(schema.Fields) != 1 || schema.Fields[0].Name != "label" {
		t.Fatalf("unexpected schema: %#v", schema)
	}

	quota, err := admin.GetQuota(context.Background())
	if err != nil {
		t.Fatalf("get quota: %v", err)
	}
	if !quota.Available || len(quota.Windows) != 1 || quota.Windows[0].Label != "24h" {
		t.Fatalf("unexpected quota report: %#v", quota)
	}

	model, err := admin.DetectModel(context.Background())
	if err != nil {
		t.Fatalf("detect model: %v", err)
	}
	if model == nil || model.Model != "example-model" {
		t.Fatalf("unexpected detected model: %#v", model)
	}

	profile, err := admin.GetProfile(context.Background())
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if !profile.Supported || profile.Dir != "C:/profiles/runtime-admin" || profile.EnvVar != "RUNTIME_ADMIN_HOME" {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

func TestAdminConfigSchemaReturnsDeepClone(t *testing.T) {
	driver := &runtimeAdminDriver{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"})),
	)

	admin := sdk.Admin().Default()
	schema, err := admin.ConfigSchema(context.Background())
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	if schema == nil {
		t.Fatal("expected schema")
	}
	schema.Fields[0].Name = "mutated"

	second, err := admin.ConfigSchema(context.Background())
	if err != nil {
		t.Fatalf("config schema second read: %v", err)
	}
	if second == nil || len(second.Fields) != 1 || second.Fields[0].Name != "label" {
		t.Fatalf("expected pristine schema clone, got %#v", second)
	}
}
