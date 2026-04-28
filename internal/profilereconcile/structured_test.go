package profilereconcile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyStructuredJSONPatchPreservesExistingFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "settings.json")
	if err := os.WriteFile(path, []byte(`{"keep":true,"sandbox":{"old":"value"}}`), 0o644); err != nil {
		t.Fatalf("write JSON: %v", err)
	}

	if err := ApplyStructuredPatch(StructuredPatch{
		FileKind: StructuredJSON,
		Path:     path,
		Section:  "sandbox",
		Values:   map[string]any{"mode": "workspace-write"},
	}); err != nil {
		t.Fatalf("patch JSON: %v", err)
	}
	text := readFile(t, path)
	for _, want := range []string{`"keep": true`, `"old": "value"`, `"mode": "workspace-write"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected JSON to contain %q, got:\n%s", want, text)
		}
	}
}

func TestApplyStructuredTOMLPatchPreservesExistingFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte("model = 'gpt-5'\n\n[sandbox]\nold = 'value'\n"), 0o644); err != nil {
		t.Fatalf("write TOML: %v", err)
	}

	if err := ApplyStructuredPatch(StructuredPatch{
		FileKind: StructuredTOML,
		Path:     path,
		Section:  "sandbox",
		Values:   map[string]any{"mode": "workspace-write"},
	}); err != nil {
		t.Fatalf("patch TOML: %v", err)
	}
	text := readFile(t, path)
	for _, want := range []string{"model = 'gpt-5'", "[sandbox]", "old = 'value'", "mode = 'workspace-write'"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected TOML to contain %q, got:\n%s", want, text)
		}
	}
}
