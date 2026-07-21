package main

import (
	"os"
	"testing"
)

func TestRunCleansTemporaryEnvironmentOnConfigError(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("TMP", tempRoot)
	t.Setenv("TEMP", tempRoot)
	t.Setenv("AGUI_AGENT", "invalid")

	if err := run(); err == nil {
		t.Fatal("run returned nil for an invalid AGUI_AGENT")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary environment leaked after configuration failure: %v", entries)
	}
}
