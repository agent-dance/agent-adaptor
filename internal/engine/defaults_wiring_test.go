package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// One narrow probe guards the PRE wiring boundary without repeating the
// already accepted materializer/archive suite.
func TestEngineDefaultsAreSelfContained(t *testing.T) {
	if got := LeaseTTL(); got != 5*time.Minute {
		t.Fatalf("LeaseTTL() = %v", got)
	}
	if got := LeaseRenewInterval(); got != 2*time.Minute {
		t.Fatalf("LeaseRenewInterval() = %v", got)
	}

	t.Setenv(SkillCacheRootEnv, t.TempDir())
	materializer := newDefaultSkillMaterializer()
	path, err := materializer.Materialize(context.Background(), InlineSkill("probe", "# probe"))
	if err != nil {
		t.Fatalf("default materializer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
		t.Fatalf("default materializer output: %v", err)
	}
}
