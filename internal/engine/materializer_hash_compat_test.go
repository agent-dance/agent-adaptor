package engine

import (
	"context"
	"path/filepath"
	"testing"

	publicskill "github.com/agent-dance/agent-adaptor/skill"
)

func TestRehomedMaterializerPreservesCacheFingerprint(t *testing.T) {
	value := Skill{Key: "team/review", Source: publicskill.InlineSource{SkillMD: "# Review"}}
	path, err := publicskill.NewDefaultSkillMaterializer(publicskill.WithSkillCacheRoot(t.TempDir())).Materialize(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	runtimeName := defaultSkillRuntimeName(value)
	contentHash := stableHash([]byte("# Review"))
	fingerprint := stableHash(value.Key, runtimeName, value.Required, value.Reason, value.Metadata, map[string]string{"SKILL.md": contentHash})
	wantBase := runtimeName + "--" + fingerprint[:12]
	if got := filepath.Base(path); got != wantBase {
		t.Fatalf("materializer cache key changed during rehome: got %q, want %q", got, wantBase)
	}
}
