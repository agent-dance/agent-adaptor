package codex

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestConfiguredDriverSessionFingerprintStableAndComplete(t *testing.T) {
	t.Parallel()
	firstMap := map[string]any{"alpha": "one", "beta": []int{2}}
	first := Config{
		CommonConfig: CommonConfig{Instructions: &driver.InstructionsBundleRef{Native: firstMap}},
		Model:        "gpt", ReasoningEffort: "high", FastMode: true,
	}
	bound := Driver(first)
	want := codexSessionFingerprint(t, bound)
	firstMap["alpha"] = "mutated"
	if got := codexSessionFingerprint(t, bound); got != want {
		t.Fatal("fingerprint did not use the construction-time config snapshot")
	}

	secondMap := map[string]any{}
	secondMap["beta"] = []int{2}
	secondMap["alpha"] = "one"
	equivalent := Config{
		CommonConfig: CommonConfig{Instructions: &driver.InstructionsBundleRef{Native: secondMap}},
		Model:        "gpt", ReasoningEffort: "high", FastMode: true,
	}
	if got := codexSessionFingerprint(t, Driver(equivalent)); got != want {
		t.Fatal("equivalent maps with different insertion order changed fingerprint")
	}

	changes := []Config{
		{CommonConfig: equivalent.CommonConfig, Model: "gpt-2", ReasoningEffort: equivalent.ReasoningEffort, FastMode: equivalent.FastMode},
		{CommonConfig: equivalent.CommonConfig, Model: equivalent.Model, ReasoningEffort: "low", FastMode: equivalent.FastMode},
		{CommonConfig: equivalent.CommonConfig, Model: equivalent.Model, ReasoningEffort: equivalent.ReasoningEffort, FastMode: false},
	}
	for i, changed := range changes {
		if got := codexSessionFingerprint(t, Driver(changed)); got == want {
			t.Fatalf("provider config change %d did not change fingerprint", i)
		}
	}
}

func codexSessionFingerprint(t *testing.T, d driver.Driver) string {
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
