package adaptor_test

// Coverage for the Inspector panel and Agent profile verbs:
//
//   - drivers WITHOUT probes degrade honestly (descriptor-derived or
//     explicitly "unsupported" reports, never fabricated success) —
//     TestInspectFallbackPanel;
//   - drivers WITH probes pass their reports through verbatim — the panel
//     never recomputes or "fixes" a probe result — TestInspectProbePassthrough;
//   - SyncProfile without the profile-resource extension syncs the one
//     portable resource and reports the rest as not-materialized errors —
//     TestSyncProfileFallbackReportsUnsupportedAsErrors.
//
// The Skills panel and SelectSkills are exercised in skills_contract_test.go
// (TestInspectSkillsSnapshotCandidates, TestSelectSkills*); ProfileState's
// fallback snapshot matrix lives in profile_contract_test.go.

import (
	"context"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestInspectFallbackPanel(t *testing.T) {
	ctx := context.Background()
	fake := newFakeDriver()
	fake.descriptor = &driver.Descriptor{
		Type:         "fake",
		DisplayName:  "Fake Driver",
		Models:       []driver.ModelInfo{{ID: "m1", Label: "Model One"}},
		ConfigSchema: &driver.ConfigSchema{Fields: []driver.ConfigField{{}}},
	}
	inspect := adaptor.New(fake).Inspect()

	t.Run("environment reports a visible noop check", func(t *testing.T) {
		env, err := inspect.Environment(ctx)
		if err != nil {
			t.Fatalf("environment: %v", err)
		}
		if env.DriverType != "fake" || !env.Healthy || env.Status != driver.EnvironmentPass {
			t.Errorf("report = %+v, want a healthy pass for the noop fallback", env)
		}
		if len(env.Checks) != 1 {
			t.Fatalf("checks = %#v, want exactly the noop check", env.Checks)
		}
		check := env.Checks[0]
		if check.Code != "noop" || check.Level != "info" || check.Message != "agent does not expose environment checks" {
			t.Errorf("check = %+v, want the visible not-invented noop marker", check)
		}
	})

	t.Run("models fall back to the descriptor list", func(t *testing.T) {
		models, err := inspect.Models(ctx)
		if err != nil {
			t.Fatalf("models: %v", err)
		}
		if len(models) != 1 || models[0].ID != "m1" || models[0].Label != "Model One" {
			t.Fatalf("models = %#v, want the static descriptor list", models)
		}
		models[0].ID = "mutated"
		again, err := inspect.Models(ctx)
		if err != nil {
			t.Fatalf("models again: %v", err)
		}
		if again[0].ID != "m1" {
			t.Error("descriptor model list leaked by reference — Models must return a copy")
		}
	})

	t.Run("quota reports unavailable with an explanation", func(t *testing.T) {
		quota, err := inspect.Quota(ctx)
		if err != nil {
			t.Fatalf("quota: %v", err)
		}
		if quota.DriverType != "fake" || quota.Available || quota.Error != "agent does not expose live quota data" {
			t.Errorf("quota = %+v, want the honest unavailable report", quota)
		}
	})

	t.Run("config schema falls back to a descriptor copy", func(t *testing.T) {
		schema, err := inspect.ConfigSchema(ctx)
		if err != nil {
			t.Fatalf("config schema: %v", err)
		}
		if schema == nil || len(schema.Fields) != 1 {
			t.Fatalf("schema = %#v, want the one-field descriptor schema", schema)
		}
		if schema == fake.descriptor.ConfigSchema {
			t.Error("ConfigSchema returned the descriptor pointer — must be a copy")
		}
	})
}

// probeFake wraps the plain fake with every inspection probe so the
// passthrough contract is assertable: whatever the driver reports is what
// the panel returns, verbatim — including reports the SDK could "improve".
type probeFake struct {
	*fakeDriver
	envReport  driver.EnvironmentReport
	models     []driver.ModelInfo
	quota      driver.QuotaReport
	schema     *driver.ConfigSchema
	profileRep driver.AgentProfile

	mu           sync.Mutex
	quotaProfile *driver.ProfileSelection
}

var (
	_ driver.EnvironmentProbe     = (*probeFake)(nil)
	_ driver.ModelLister          = (*probeFake)(nil)
	_ driver.QuotaProbe           = (*probeFake)(nil)
	_ driver.ConfigSchemaProvider = (*probeFake)(nil)
	_ driver.ProfileReporter      = (*probeFake)(nil)
)

func (p *probeFake) CheckEnvironment(context.Context, any) (driver.EnvironmentReport, error) {
	return p.envReport, nil
}

func (p *probeFake) ListModels(context.Context, any) ([]driver.ModelInfo, error) {
	return p.models, nil
}

func (p *probeFake) GetQuota(_ context.Context, _ any, sel *driver.ProfileSelection) (driver.QuotaReport, error) {
	p.mu.Lock()
	p.quotaProfile = sel
	p.mu.Unlock()
	return p.quota, nil
}

func (p *probeFake) ConfigSchema(context.Context, any) (*driver.ConfigSchema, error) {
	return p.schema, nil
}

func (p *probeFake) GetProfile(context.Context, any, driver.AgentIdentity, *driver.ProfileSelection) (driver.AgentProfile, error) {
	return p.profileRep, nil
}

func TestInspectProbePassthrough(t *testing.T) {
	ctx := context.Background()
	pf := &probeFake{
		fakeDriver: newFakeDriver(),
		envReport: driver.EnvironmentReport{
			DriverType: "fake",
			Status:     driver.EnvironmentWarn,
			Healthy:    false,
			Summary:    "canned summary",
			Checks:     []driver.EnvironmentCheck{{Code: "binary", Level: "warn", Message: "old version", Hint: "upgrade"}},
		},
		models: []driver.ModelInfo{{ID: "live-1", Label: "Live Model"}},
		quota: driver.QuotaReport{
			DriverType: "fake",
			Provider:   "anthropic",
			Source:     "live",
			Available:  true,
			Windows:    []driver.QuotaWindow{{Label: "5h", ValueLabel: "42%"}},
		},
		schema: &driver.ConfigSchema{Fields: []driver.ConfigField{{}, {}}},
		profileRep: driver.AgentProfile{
			DriverType: "fake",
			Supported:  true,
			Dir:        "/fake/profile",
			Source:     "custom-source",
			Managed:    true,
		},
	}
	agent := adaptor.New(pf, adaptor.WithProfile(profile.Native()))
	inspect := agent.Inspect()

	env, err := inspect.Environment(ctx)
	if err != nil {
		t.Fatalf("environment: %v", err)
	}
	if env.Status != driver.EnvironmentWarn || env.Healthy || env.Summary != "canned summary" ||
		len(env.Checks) != 1 || env.Checks[0].Code != "binary" {
		t.Errorf("environment report was not passed through verbatim: %+v", env)
	}

	models, err := inspect.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 1 || models[0].ID != "live-1" {
		t.Errorf("models = %#v, want the probe's live list, not the descriptor", models)
	}

	quota, err := inspect.Quota(ctx)
	if err != nil {
		t.Fatalf("quota: %v", err)
	}
	if !quota.Available || quota.Provider != "anthropic" || len(quota.Windows) != 1 || quota.Windows[0].Label != "5h" {
		t.Errorf("quota report was not passed through verbatim: %+v", quota)
	}
	pf.mu.Lock()
	quotaProfile := pf.quotaProfile
	pf.mu.Unlock()
	if quotaProfile == nil || quotaProfile.Mode != driver.ProfileModeNative {
		t.Errorf("quota probe received profile %+v, want the agent's WithProfile selection", quotaProfile)
	}

	schema, err := inspect.ConfigSchema(ctx)
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	if schema != pf.schema {
		t.Error("ConfigSchema must return the provider result as-is for probe-implementing drivers")
	}

	snapshot, err := agent.ProfileState(ctx)
	if err != nil {
		t.Fatalf("profile state: %v", err)
	}
	if !snapshot.Profile.Supported || snapshot.Profile.Dir != "/fake/profile" || snapshot.Profile.Source != "custom-source" {
		t.Errorf("ProfileState.Profile = %+v, want the driver's GetProfile report verbatim", snapshot.Profile)
	}
}

// TestSyncProfileFallbackReportsUnsupportedAsErrors: without the
// profile-resource extension, SyncProfile syncs the portable resource
// (skills) and must report desired-but-unsupported kinds as not-materialized
// errors — never as silently applied.
func TestSyncProfileFallbackReportsUnsupportedAsErrors(t *testing.T) {
	agent := adaptor.New(newFakeDriver(), adaptor.WithProfileResources(profile.Resources{
		Agents: []profile.SubAgent{{Key: "reviewer", Instructions: "review"}},
	}))

	snapshot, err := agent.SyncProfile(context.Background())
	if err != nil {
		t.Fatalf("sync profile: %v", err)
	}
	if snapshot.DriverType != "fake" {
		t.Errorf("driver type = %q, want fake", snapshot.DriverType)
	}
	agents := profileResourceByKind(t, snapshot, adaptor.ProfileResourceAgents)
	if agents.Materialization != adaptor.ProfileResourceMaterializationNotMaterialized {
		t.Errorf("agents materialization = %q, want not_materialized after a sync that cannot apply them", agents.Materialization)
	}
	if agents.Error == "" {
		t.Error("agents row has no Error — an unappliable desired resource must fail loudly on sync")
	}
}
