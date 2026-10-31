package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestPrepareInstructionsCapturesSourceOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instructions.md")
	if err := os.WriteFile(path, []byte("original source"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := &driver.InstructionsBundleRef{Path: path, Scope: driver.InstructionScopeRun, Fingerprint: "host-fingerprint"}
	prepared, err := prepareInstructions(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if prepared.Path != "" || prepared.Content != "original source" || prepared.Fingerprint != ref.Fingerprint || ref.Path != path || ref.Content != "" {
		t.Fatalf("source not captured without mutating caller: %+v", prepared)
	}
}
