package codebuddy

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestConfiguredDriverSessionFingerprintStableAndComplete(t *testing.T) {
	t.Parallel()
	env := []driver.EnvBinding{{Name: "TOKEN", Value: "secret"}}
	cfg := Config{
		CommonConfig: CommonConfig{Env: env},
		Model:        "codebuddy-model", Effort: "high", PermissionMode: PermissionPlan, MaxTurnsPerRun: 8,
	}
	bound := Driver(cfg)
	want := codebuddySessionFingerprint(t, bound)
	env[0].Value = "mutated"
	if got := codebuddySessionFingerprint(t, bound); got != want {
		t.Fatal("fingerprint did not use the construction-time config snapshot")
	}

	common := CommonConfig{Env: []driver.EnvBinding{{Name: "TOKEN", Value: "secret"}}}
	changes := []Config{
		{CommonConfig: common, Model: "other", Effort: cfg.Effort, PermissionMode: cfg.PermissionMode, MaxTurnsPerRun: cfg.MaxTurnsPerRun},
		{CommonConfig: common, Model: cfg.Model, Effort: "low", PermissionMode: cfg.PermissionMode, MaxTurnsPerRun: cfg.MaxTurnsPerRun},
		{CommonConfig: common, Model: cfg.Model, Effort: cfg.Effort, PermissionMode: PermissionDefault, MaxTurnsPerRun: cfg.MaxTurnsPerRun},
		{CommonConfig: common, Model: cfg.Model, Effort: cfg.Effort, PermissionMode: cfg.PermissionMode, MaxTurnsPerRun: 9},
	}
	for i, changed := range changes {
		if got := codebuddySessionFingerprint(t, Driver(changed)); got == want {
			t.Fatalf("provider config change %d did not change fingerprint", i)
		}
	}
}

func codebuddySessionFingerprint(t *testing.T, d driver.Driver) string {
	t.Helper()
	fingerprinter, ok := d.(driver.SessionConfigFingerprinter)
	if !ok {
		t.Fatalf("%T does not implement driver.SessionConfigFingerprinter", d)
	}
	fingerprint, err := fingerprinter.SessionConfigFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" {
		t.Fatal("empty session config fingerprint")
	}
	return fingerprint
}
