package cursor

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestConfiguredDriverSessionFingerprintStableAndComplete(t *testing.T) {
	t.Parallel()
	metadata := map[string]string{"alpha": "one", "beta": "two"}
	cfg := Config{
		CommonConfig: CommonConfig{WorkspaceRuntime: &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{ID: "runtime", Metadata: metadata}}}},
		Model:        "cursor-model", Mode: "agent",
	}
	bound := Driver(cfg)
	want := cursorSessionFingerprint(t, bound)
	metadata["alpha"] = "mutated"
	if got := cursorSessionFingerprint(t, bound); got != want {
		t.Fatal("fingerprint did not use the construction-time config snapshot")
	}

	ordered := map[string]string{}
	ordered["beta"] = "two"
	ordered["alpha"] = "one"
	equivalent := Config{
		CommonConfig: CommonConfig{WorkspaceRuntime: &driver.WorkspaceRuntimeConfig{Services: []driver.RuntimeServiceSpec{{ID: "runtime", Metadata: ordered}}}},
		Model:        "cursor-model", Mode: "agent",
	}
	if got := cursorSessionFingerprint(t, Driver(equivalent)); got != want {
		t.Fatal("equivalent maps with different insertion order changed fingerprint")
	}
	if got := cursorSessionFingerprint(t, Driver(Config{CommonConfig: equivalent.CommonConfig, Model: "other", Mode: equivalent.Mode})); got == want {
		t.Fatal("Model change did not change fingerprint")
	}
	if got := cursorSessionFingerprint(t, Driver(Config{CommonConfig: equivalent.CommonConfig, Model: equivalent.Model, Mode: "plan"})); got == want {
		t.Fatal("Mode change did not change fingerprint")
	}
}

func cursorSessionFingerprint(t *testing.T, d driver.Driver) string {
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
