package agentadaptor_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type runtimeAdminDriver struct {
	lastReq       agentadaptor.DriverRunRequest
	mcpCapability agentadaptor.MCPCapability
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
		MCP:     d.mcpCapability,
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

func (d *runtimeAdminDriver) DetectModel(_ context.Context, _ any, _ *agentadaptor.ProfileSelection) (*agentadaptor.DetectedModel, error) {
	return &agentadaptor.DetectedModel{
		Model:      "example-model",
		Provider:   "example-provider",
		Source:     "test",
		Candidates: []string{"example-model"},
	}, nil
}

func (d *runtimeAdminDriver) GetProfile(_ context.Context, _ any, _ agentadaptor.AgentIdentity, _ *agentadaptor.ProfileSelection) (agentadaptor.AgentProfile, error) {
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

func (d *runtimeAdminDriver) GetQuota(_ context.Context, _ any, _ *agentadaptor.ProfileSelection) (agentadaptor.QuotaReport, error) {
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

func (m *observingRuntimeManager) ReleaseByLabels(_ context.Context, _ map[string]string) error {
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

func TestRuntimeServiceMetadataInjectsMCPIntoDriverAndProfile(t *testing.T) {
	driver := &runtimeAdminDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, HTTP: true},
	}
	runtimeManager := &runtimeMCPManager{
		ref: agentadaptor.RuntimeServiceRef{
			ID:   "svc-delegation",
			Name: "a2a-delegation",
			URL:  "http://127.0.0.1:43127/mcp",
			Metadata: map[string]string{
				"agentadaptor.mcp.enabled":                 "true",
				"agentadaptor.mcp.key":                     "delegate-a2a",
				"agentadaptor.mcp.transport":               "http",
				"agentadaptor.mcp.headers_json":            `{"X-Run-Token":"env:DELEGATION_TOKEN"}`,
				"agentadaptor.mcp.bearer_token_env_var":    "DELEGATION_TOKEN",
				"agentadaptor.mcp.required":                "true",
				"agentadaptor.mcp.required_reason":         "visual A2A subagent delegation",
				"delegation.secret_should_not_be_promoted": "sk-test-secret",
			},
		},
	}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"},
			agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{Key: "host", Transport: agentadaptor.MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp"}}}),
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{Name: "a2a-delegation"}),
		)),
		agentadaptor.WithRuntimeServiceManager(runtimeManager),
	)

	if _, err := sdk.Run(context.Background(), "use delegation"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(driver.lastReq.MCP.Servers) != 2 {
		t.Fatalf("expected host + runtime MCP servers, got %#v", driver.lastReq.MCP.Servers)
	}
	var injected agentadaptor.MCPServerSpec
	for _, server := range driver.lastReq.MCP.Servers {
		if server.Key == "delegate-a2a" {
			injected = server
		}
	}
	if injected.Key == "" {
		t.Fatalf("missing runtime-injected MCP server: %#v", driver.lastReq.MCP.Servers)
	}
	if injected.Transport != agentadaptor.MCPTransportHTTP || injected.URL != "http://127.0.0.1:43127/mcp" {
		t.Fatalf("unexpected injected server endpoint: %#v", injected)
	}
	if injected.Headers["X-Run-Token"] != "env:DELEGATION_TOKEN" || injected.BearerTokenEnvVar != "DELEGATION_TOKEN" {
		t.Fatalf("expected run-scoped auth references, got %#v", injected)
	}
	if !injected.Required || injected.RequiredReason != "visual A2A subagent delegation" {
		t.Fatalf("expected required runtime MCP server, got %#v", injected)
	}
	if driver.lastReq.ProfilePayload.MCP.Fingerprint != driver.lastReq.MCP.Fingerprint {
		t.Fatalf("profile MCP fingerprint %q did not match driver MCP fingerprint %q", driver.lastReq.ProfilePayload.MCP.Fingerprint, driver.lastReq.MCP.Fingerprint)
	}
	if driver.lastReq.ProfilePayload.Fingerprint == "" {
		t.Fatal("expected profile fingerprint to include runtime MCP payload")
	}
}

func TestRuntimeServiceMCPMetadataRejectsMalformedJSON(t *testing.T) {
	driver := &runtimeAdminDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, HTTP: true},
	}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"},
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{Name: "bad-mcp"}),
		)),
		agentadaptor.WithRuntimeServiceManager(&runtimeMCPManager{ref: agentadaptor.RuntimeServiceRef{
			Name: "bad-mcp",
			URL:  "http://127.0.0.1:43127/mcp",
			Metadata: map[string]string{
				"agentadaptor.mcp.enabled":      "true",
				"agentadaptor.mcp.headers_json": `{"Authorization": 123}`,
			},
		}}),
	)

	if _, err := sdk.Run(context.Background(), "use delegation"); err == nil {
		t.Fatal("expected malformed runtime MCP metadata to fail")
	}
}

func TestRuntimeServiceMCPDuplicateKeyIsRejected(t *testing.T) {
	driver := &runtimeAdminDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, HTTP: true},
	}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"},
			agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{Servers: []agentadaptor.MCPServerSpec{{Key: "delegate-a2a", Transport: agentadaptor.MCPTransportHTTP, URL: "http://127.0.0.1:1/mcp"}}}),
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{Name: "a2a-delegation"}),
		)),
		agentadaptor.WithRuntimeServiceManager(&runtimeMCPManager{ref: agentadaptor.RuntimeServiceRef{
			Name: "a2a-delegation",
			URL:  "http://127.0.0.1:43127/mcp",
			Metadata: map[string]string{
				"agentadaptor.mcp.enabled": "true",
				"agentadaptor.mcp.key":     "delegate-a2a",
			},
		}}),
	)

	if _, err := sdk.Run(context.Background(), "use delegation"); err == nil {
		t.Fatal("expected duplicate runtime MCP key to fail")
	}
}

func TestRuntimeMCPChangesSessionFingerprint(t *testing.T) {
	driver := &runtimeAdminDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, HTTP: true},
	}
	runtimeManager := &runtimeMCPManager{}
	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.Bind(driver, fakeConfig{Label: "runtime"},
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{Name: "delegate"}),
		)),
		agentadaptor.WithRuntimeServiceManager(runtimeManager),
	)

	runtimeManager.ref = agentadaptor.RuntimeServiceRef{
		Name: "delegate",
		URL:  "http://127.0.0.1:43127/mcp",
		Metadata: map[string]string{
			"agentadaptor.mcp.enabled": "true",
			"agentadaptor.mcp.key":     "delegate-a",
		},
	}
	if _, err := sdk.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := driver.lastReq.ProfilePayload.Fingerprint

	runtimeManager.ref = agentadaptor.RuntimeServiceRef{
		Name: "delegate",
		URL:  "http://127.0.0.1:43128/mcp",
		Metadata: map[string]string{
			"agentadaptor.mcp.enabled": "true",
			"agentadaptor.mcp.key":     "delegate-b",
		},
	}
	if _, err := sdk.Run(context.Background(), "second"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if driver.lastReq.ProfilePayload.Fingerprint == first {
		t.Fatal("expected runtime MCP profile fingerprint to change")
	}
}

type runtimeMCPManager struct {
	ref agentadaptor.RuntimeServiceRef
}

func (m *runtimeMCPManager) Ensure(_ context.Context, _ agentadaptor.RuntimeServiceRequest) ([]agentadaptor.RuntimeServiceRef, error) {
	return []agentadaptor.RuntimeServiceRef{m.ref}, nil
}

func (m *runtimeMCPManager) ReleaseByRun(_ context.Context, _ string) error { return nil }

func (m *runtimeMCPManager) ReleaseByLabels(_ context.Context, _ map[string]string) error { return nil }

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
