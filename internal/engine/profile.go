package engine

import (
	"os"
	"path/filepath"
	"strings"
)

// NormalizeProfileDir expands ~, resolves relative paths, and cleans the
// result. It is exported for hosts that want to validate profile paths before
// building an SDK, and used by the driver-side profile runtime.
func NormalizeProfileDir(dir string) (string, error) {
	trimmed := strings.TrimSpace(dir)
	if trimmed == "" {
		return "", nil
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		trimmed = home
	} else if strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		trimmed = filepath.Join(home, trimmed[2:])
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func cloneProfileSelection(selection *ProfileSelection) *ProfileSelection {
	if selection == nil {
		return nil
	}
	copySelection := *selection
	if selection.Clone != nil {
		copyClone := *selection.Clone
		copySelection.Clone = &copyClone
	}
	return &copySelection
}
