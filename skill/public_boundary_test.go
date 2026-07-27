package skill_test

import (
	"context"
	"testing"

	"github.com/agent-dance/agent-adaptor/skill"
)

// This file intentionally imports only the skill package and the standard
// library. It is the consumer-boundary proof that catalogue and materializer
// contracts no longer require the root package, driver, or internal engine.
func TestPublicBoundaryIsSelfContained(t *testing.T) {
	var provider skill.Provider = skill.Set{
		"review": skill.Inline("review", "# Review"),
	}
	var catalog skill.Catalog = provider.(skill.Catalog)

	got, err := provider.GetSkills(context.Background(), []string{"review"})
	if err != nil || got["review"].Key != "review" {
		t.Fatalf("GetSkills() = %#v, %v", got, err)
	}
	entries, err := catalog.Catalogue(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Key != "review" {
		t.Fatalf("Catalogue() = %#v, %v", entries, err)
	}

	var materializer skill.Materializer = skill.NewDefaultSkillMaterializer(
		skill.WithSkillCacheRoot(t.TempDir()),
	)
	path, err := materializer.Materialize(context.Background(), got["review"])
	if err != nil || path == "" {
		t.Fatalf("Materialize() path=%q err=%v", path, err)
	}
}
