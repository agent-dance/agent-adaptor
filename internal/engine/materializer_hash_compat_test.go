package engine

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRehomedMaterializerPreservesCacheFingerprint(t *testing.T) {
	value := Skill{Key: "team/review", Source: SkillFromInline{SkillMD: "# Review"}}
	path, err := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir())).Materialize(context.Background(), value)
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
