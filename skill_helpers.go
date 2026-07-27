package agentadaptor

// Thin shell for the built-in materializer's public constant surface. The
// materializer implementation moved to internal/engine in P5.2 together
// with the archive machinery, and the conflict-aware skill merger that used
// to sit here was dead code (internal/engine owns the live copy).

import "github.com/agent-dance/agent-adaptor/internal/engine"

// SkillCacheRootEnv is the environment variable that overrides the default
// materializer's cache root. Adapters inspect the same variable (indirectly
// through internal/skillruntime.ManagedSkillCacheRoot) to identify the
// paths they are allowed to manage, so exposing it here keeps both sides of
// the contract in sync.
const SkillCacheRootEnv = engine.SkillCacheRootEnv
