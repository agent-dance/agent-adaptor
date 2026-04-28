package mcpruntime

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func readJSONObject(path string) (map[string]any, error) {
	payload := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return payload, nil
		}
		return nil, err
	}
	if len(raw) == 0 {
		return payload, nil
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func writeJSONObject(path string, payload map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return profilestate.AtomicWriteFile(path, raw, 0o644)
}
