package engine

import (
	"context"

	"github.com/agent-dance/agent-adaptor/skill"
)

// SkillProvider resolves catalogue keys into concrete skills for a run.
// Providers may return additional Required skills that were not explicitly
// requested. Caller identity is carried in context by the engine.
type SkillProvider interface {
	GetSkills(ctx context.Context, keys []string) (map[string]Skill, error)
}

// SkillCatalog extends SkillProvider with deterministic enumeration for
// inspection surfaces.
type SkillCatalog interface {
	SkillProvider
	Catalogue(ctx context.Context) ([]Skill, error)
}

// SkillMaterializer writes a skill source to a directory containing SKILL.md.
type SkillMaterializer interface {
	Materialize(ctx context.Context, s Skill) (sourcePath string, err error)
}

type (
	// SkillKeyConflictError is owned by package skill.
	SkillKeyConflictError = skill.SkillKeyConflictError
	// SkillMaterializationError is owned by package skill.
	SkillMaterializationError = skill.SkillMaterializationError
)
