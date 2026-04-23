package mcpruntime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type ProfileKind string

const (
	ProfileKindShared    ProfileKind = "shared"
	ProfileKindDedicated ProfileKind = "dedicated"
)

func ClassifyProfile(profile agentadaptor.AgentProfile, canonicalShared string) ProfileKind {
	if profile.Managed {
		return ProfileKindDedicated
	}
	if samePath(profile.Dir, canonicalShared) {
		return ProfileKindShared
	}
	return ProfileKindDedicated
}

func samePath(left, right string) bool {
	return samePathForOS(left, right, runtime.GOOS)
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
