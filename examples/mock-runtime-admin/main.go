package main

import (
	"context"
	"encoding/json"
	"flag"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/examples/internal/mockkit"
)

func main() {
	timeout := flag.Duration("timeout", 30*time.Second, "Maximum time to wait for the mock runtime/admin example")
	flag.Parse()

	used := 65
	driver := mockkit.NewRecordingDriver("Mock Runtime Admin")
	driver.Quota = &agentadaptor.QuotaReport{
		DriverType: "mock",
		Provider:   "mock-provider",
		Source:     "example",
		Available:  true,
		Windows: []agentadaptor.QuotaWindow{
			{Label: "24h", UsedPercent: &used, ValueLabel: "65% used"},
		},
	}
	driver.DetectedModel = &agentadaptor.DetectedModel{
		Model:      "mock-model",
		Provider:   "mock-provider",
		Source:     "example",
		Candidates: []string{"mock-model"},
	}
	driver.Profile = &agentadaptor.AgentProfile{
		DriverType: "mock",
		Supported:  true,
		Dir:        "C:/profiles/mock-runtime-admin",
		EnvVar:     "MOCK_AGENT_HOME",
		Source:     agentadaptor.AgentProfileSourceDefault,
	}
	driver.RunFunc = func(_ context.Context, req agentadaptor.DriverRunRequest, _ agentadaptor.EventSink) (agentadaptor.DriverRunResult, error) {
		cost := 0.42
		raw, err := json.MarshalIndent(req, "", "  ")
		if err != nil {
			return agentadaptor.DriverRunResult{}, err
		}
		return agentadaptor.DriverRunResult{
			Output:      "runtime-aware mock run completed",
			RawStreams:  &agentadaptor.RawStreams{Stdout: string(raw)},
			ExitCode:    0,
			Provider:    "mock-provider",
			Biller:      "mock-biller",
			Model:       "mock-model",
			BillingType: "subscription",
			CostUSD:     &cost,
			Summary:     "runtime-aware mock run completed",
		}, nil
	}

	runtimeManager := &mockkit.ObservingRuntimeManager{
		Refs: []agentadaptor.RuntimeServiceRef{
			{ID: "svc-api", Name: "api", URL: "http://127.0.0.1:4010"},
			{ID: "svc-db", Name: "db", URL: "postgres://127.0.0.1:15432/app"},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(agentadaptor.BindTyped(driver, mockkit.Config{Label: "runtime-admin"},
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID:       "runtime-admin",
				TenantID: "examples",
				Name:     "runtime-admin",
			}),
			agentadaptor.WithDefaultRuntimeServices(agentadaptor.RuntimeServiceSpec{
				ID:          "api",
				Name:        "api",
				URL:         "http://workspace-api",
				Description: "Workspace API endpoint",
			}),
		)),
		agentadaptor.WithRuntimeServiceManager(runtimeManager),
	)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := sdk.Run(ctx, "Describe the runtime services made available to this run.",
		agentadaptor.WithRuntimeServices(
			agentadaptor.RuntimeServiceSpec{ID: "api", Name: "api", URL: "http://workspace-api"},
			agentadaptor.RuntimeServiceSpec{ID: "db", Name: "db", URL: "postgres://workspace-db"},
		),
	)
	exampleutil.Must(err, "run runtime/admin mock example")
	exampleutil.Check(result.RunID != "", "expected run id to be populated")
	exampleutil.Check(len(result.RuntimeServices) == 2, "expected 2 runtime services, got %d", len(result.RuntimeServices))
	exampleutil.Check(result.Provider == "mock-provider", "expected provider to round-trip, got %q", result.Provider)

	admin := sdk.Admin().Default()
	schema, err := admin.ConfigSchema(ctx)
	exampleutil.Must(err, "load config schema")
	quota, err := admin.GetQuota(ctx)
	exampleutil.Must(err, "load quota report")
	model, err := admin.DetectModel(ctx)
	exampleutil.Must(err, "detect model")
	profile, err := admin.GetProfile(ctx)
	exampleutil.Must(err, "load profile report")
	env, err := admin.CheckEnvironment(ctx)
	exampleutil.Must(err, "check environment")

	exampleutil.Check(result.RawStreams != nil, "expected RawStreams to be populated")
	var request agentadaptor.DriverRunRequest
	err = json.Unmarshal([]byte(result.RawStreams.Stdout), &request)
	exampleutil.Must(err, "decode driver request")

	exampleutil.PrintJSON(map[string]any{
		"example": "mock-runtime-admin",
		"run": map[string]any{
			"run_id":           result.RunID,
			"transcript":       result.Transcript,
			"provider":         result.Provider,
			"model":            result.Model,
			"billing_type":     result.BillingType,
			"cost_usd":         result.CostUSD,
			"runtime_services": result.RuntimeServices,
		},
		"request": request,
		"admin": map[string]any{
			"config_schema":  schema,
			"quota":          quota,
			"detected_model": model,
			"profile":        profile,
			"environment":    env,
		},
		"runtime_manager": map[string]any{
			"last_request": runtimeManager.LastRequest,
			"released":     runtimeManager.ReleasedRuns,
		},
	})
}
