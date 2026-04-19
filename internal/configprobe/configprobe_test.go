package configprobe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadNestedJSONString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"ANTHROPIC_BASE_URL":"https://example.invalid"},"model":"claude-sonnet-4"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	value, ok, err := ReadNestedJSONString(path, "env", "ANTHROPIC_BASE_URL")
	if err != nil {
		t.Fatalf("read nested string: %v", err)
	}
	if !ok || value != "https://example.invalid" {
		t.Fatalf("unexpected nested value: %q %v", value, ok)
	}
}

func TestReadJSONObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"gpt-5.4","env":{"OPENAI_BASE_URL":"https://example.invalid"}}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	payload, err := ReadJSONObject(path)
	if err != nil {
		t.Fatalf("read object: %v", err)
	}
	if payload["model"] != "gpt-5.4" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestReadTopLevelJSONStringDelegatesToNestedHelper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"gpt-5.4"}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	value, ok, err := ReadTopLevelJSONString(path, "model")
	if err != nil {
		t.Fatalf("read top-level string: %v", err)
	}
	if !ok || value != "gpt-5.4" {
		t.Fatalf("unexpected top-level value: %q %v", value, ok)
	}
}
