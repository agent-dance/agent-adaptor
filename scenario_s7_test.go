package adaptor_test

// Scenario S7 · settings/onboarding wizard: four read-only Inspect probes
// power the whole page.

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestScenarioS7OnboardingWizard(t *testing.T) {
	ctx := context.Background()
	pf := &probeFake{
		fakeDriver: newFakeDriver(),
		envReport: driver.EnvironmentReport{
			DriverType: "fake",
			Status:     driver.EnvironmentFail,
			Healthy:    false,
			Summary:    "provider CLI is not ready",
			Checks: []driver.EnvironmentCheck{
				{Code: "binary-missing", Level: "error", Message: "provider CLI is not installed", Hint: "install the CLI"},
				{Code: "not-logged-in", Level: "error", Message: "no login state found", Hint: "run the provider login"},
			},
		},
		models: []driver.ModelInfo{{ID: "model-a", Label: "Model A"}, {ID: "model-b", Label: "Model B"}},
		quota:  driver.QuotaReport{DriverType: "fake", Available: true, Windows: []driver.QuotaWindow{{Label: "5h", ValueLabel: "42%"}}},
		schema: &driver.ConfigSchema{Fields: []driver.ConfigField{{}}},
	}
	agent := adaptor.New(pf)

	var shown []adaptor.EnvironmentCheck
	env, err := agent.Inspect().Environment(ctx)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	if !env.Healthy {
		shown = env.Checks // wizard.Show(env.Checks) — CLI missing / not logged in / profile absent
	}
	models, err := agent.Inspect().Models(ctx) // model dropdown
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	fields, err := agent.Inspect().ConfigSchema(ctx) // dynamic form
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	quota, err := agent.Inspect().Quota(ctx) // quota bar
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if len(shown) != 2 || shown[0].Code != "binary-missing" || shown[1].Code != "not-logged-in" {
		t.Errorf("wizard problems = %#v, want the driver's two failing checks verbatim", shown)
	}
	if len(models) != 2 || models[0].ID != "model-a" {
		t.Errorf("model dropdown = %#v, want the probe's live model list", models)
	}
	if fields == nil || len(fields.Fields) != 1 {
		t.Errorf("config form = %#v, want the probe's schema", fields)
	}
	if !quota.Available || len(quota.Windows) != 1 || quota.Windows[0].ValueLabel != "42%" {
		t.Errorf("quota bar = %+v, want the probe's live windows", quota)
	}
}
