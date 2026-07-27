package profilekind

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// Classify maps an adapter-resolved effective profile to the SDK's neutral
// shared vs host-managed profile kind. Adapters should classify the effective
// profile after resolving provider env/profile options rather than branching
// directly on ProfileSelection.Mode.
func Classify(profile driver.AgentProfile, canonicalShared string) engine.ProfileKind {
	if profile.Managed {
		return engine.ProfileKindHostManaged
	}
	if SamePath(profile.Dir, canonicalShared) {
		return engine.ProfileKindShared
	}
	return engine.ProfileKindHostManaged
}

func SamePath(left, right string) bool {
	return samePathForOS(left, right, runtime.GOOS)
}

// SamePathForOSForTest exposes OS-specific comparison to legacy package tests.
func SamePathForOSForTest(left, right, goos string) bool {
	return samePathForOS(left, right, goos)
}

func samePathForOS(left, right, goos string) bool {
	normalizedLeft := canonicalPath(left)
	normalizedRight := canonicalPath(right)
	if normalizedLeft == "" || normalizedRight == "" {
		return false
	}
	if goos == "windows" {
		return strings.EqualFold(normalizedLeft, normalizedRight)
	}
	return normalizedLeft == normalizedRight
}

func canonicalPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		absolute = trimmed
	}
	cleaned := filepath.Clean(absolute)
	if _, err := os.Stat(cleaned); err == nil {
		if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
			return filepath.Clean(resolved)
		}
	}
	return cleaned
}
