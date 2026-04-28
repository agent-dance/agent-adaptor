package profilereconcile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	toml "github.com/pelletier/go-toml/v2"
)

type StructuredFileKind string

const (
	StructuredJSON StructuredFileKind = "json"
	StructuredTOML StructuredFileKind = "toml"
)

type StructuredPatch struct {
	FileKind StructuredFileKind
	Path     string
	Section  string
	Values   map[string]any
}

func ApplyStructuredPatch(patch StructuredPatch) error {
	path := filepath.Clean(strings.TrimSpace(patch.Path))
	if path == "." || path == "" {
		return fmt.Errorf("structured patch requires path")
	}
	root, err := readStructuredObject(patch.FileKind, path)
	if err != nil {
		return err
	}
	mergeStructuredValues(root, patch.Section, patch.Values)
	return writeStructuredObject(patch.FileKind, path, root)
}

func readStructuredObject(kind StructuredFileKind, path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	root := map[string]any{}
	switch kind {
	case StructuredJSON:
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("read JSON profile config %s: %w", path, err)
		}
	case StructuredTOML:
		if err := toml.Unmarshal(raw, &root); err != nil {
			return nil, fmt.Errorf("read TOML profile config %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("unsupported structured profile config kind %q", kind)
	}
	return root, nil
}

func writeStructuredObject(kind StructuredFileKind, path string, root map[string]any) error {
	var raw []byte
	var err error
	switch kind {
	case StructuredJSON:
		raw, err = json.MarshalIndent(root, "", "  ")
	case StructuredTOML:
		raw, err = toml.Marshal(root)
	default:
		return fmt.Errorf("unsupported structured profile config kind %q", kind)
	}
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return profilestate.AtomicWriteFile(path, raw, 0o644)
}

func mergeStructuredValues(root map[string]any, section string, values map[string]any) {
	target := root
	for _, part := range strings.Split(strings.TrimSpace(section), ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		child, _ := target[part].(map[string]any)
		if child == nil {
			child = map[string]any{}
			target[part] = child
		}
		target = child
	}
	for key, value := range values {
		target[key] = value
	}
}
