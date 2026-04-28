package profilekind

import (
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestClassifyTreatsManagedHomesAsHostManaged(t *testing.T) {
	kind := Classify(agentadaptor.AgentProfile{Dir: t.TempDir(), Managed: true}, t.TempDir())
	if kind != agentadaptor.ProfileKindHostManaged {
		t.Fatalf("expected managed profile to be host-managed, got %q", kind)
	}
}

func TestClassifyResolvesSymlinksBeforeComparingCanonicalSharedPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	kind := Classify(agentadaptor.AgentProfile{Dir: link}, target)
	if kind != agentadaptor.ProfileKindShared {
		t.Fatalf("expected symlinked shared profile to classify as shared, got %q", kind)
	}
}

func TestSamePathForWindowsIgnoresCase(t *testing.T) {
	if !samePathForOS(`C:\Users\Dev\.cursor`, `c:\users\dev\.cursor`, "windows") {
		t.Fatal("expected Windows path comparison to ignore case")
	}
}
