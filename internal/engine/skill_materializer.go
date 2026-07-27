package engine

import (
	"github.com/agent-dance/agent-adaptor/internal/skillmaterializer"
	"github.com/agent-dance/agent-adaptor/skill"
)

// SkillCacheRootEnv remains as a migration constant; package skill owns the
// public contract and the private materializer package owns the implementation.
const SkillCacheRootEnv = skill.SkillCacheRootEnv

func newDefaultSkillMaterializer() SkillMaterializer {
	return skillmaterializer.New(skill.ErrSkillSourceMissing)
}
