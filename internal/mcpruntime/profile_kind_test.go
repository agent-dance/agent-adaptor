package mcpruntime

import (
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
)

func TestClassifyProfileTreatsManagedHomesAsDedicated(t *testing.T) {
	kind := ClassifyProfile(agentadaptor.AgentProfile{
		Dir:     t.TempDir(),
		Managed: true,
	}, t.TempDir())
	if kind != ProfileKindDedicated {
		t.Fatalf("expected managed profile to be dedicated, got %q", kind)
	}
}

func TestClassifyProfileResolvesSymlinksBeforeComparingCanonicalSharedPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	kind := ClassifyProfile(agentadaptor.AgentProfile{Dir: link}, target)
	if kind != ProfileKindShared {
		t.Fatalf("expected symlinked shared profile to classify as shared, got %q", kind)
	}
}

func TestSamePathForWindowsIgnoresCase(t *testing.T) {
	if !samePathForOS(`C:\Users\Dev\.cursor`, `c:\users\dev\.cursor`, "windows") {
		t.Fatal("expected Windows path comparison to ignore case")
	}
}
