package agentadaptor

// Thin wrappers preserving the historical root-package function surface for
// public helpers whose implementations moved to internal/engine (P0.2).
// Signatures are identical; behavior is delegation only.

import (
	"context"
	"io/fs"

	"github.com/agent-dance/agent-adaptor/internal/engine"
)

// --- Caller identity (was caller_identity.go) -------------------------------

// WithCallerIdentity returns a new context that carries id as the
// "caller identity" metadata for SDK hooks (currently SkillProvider.GetSkills
// and SkillCatalog.Catalogue). SDK invokes this internally before
// dispatching; adapters or middleware composing providers may also use
// it to forward identity through their own ctx chains.
//
// Most hosts do not need to call this directly: SDK already injects
// the AgentIdentity resolved from binding defaults / WithAgentIdentity
// before invoking provider hooks.
func WithCallerIdentity(ctx context.Context, id AgentIdentity) context.Context {
	return engine.WithCallerIdentity(ctx, id)
}

// CallerIdentityFromContext returns the AgentIdentity SDK injected
// into ctx before invoking SkillProvider.GetSkills / SkillCatalog.Catalogue.
//
// Provider implementations that need scoping (TenantID for catalogue
// partitioning, ProfileID for user-private skills, etc.) read it via
// this helper:
//
//	func (p *MyProvider) GetSkills(ctx context.Context, keys []string) (map[string]agentadaptor.Skill, error) {
//	    id, _ := agentadaptor.CallerIdentityFromContext(ctx)
//	    return p.store.BatchGet(ctx, id.TenantID, keys)
//	}
//
// The boolean is false when ctx has no identity attached (e.g. tests
// invoking the provider directly without SDK plumbing). Public
// providers and providers serving zero-tenant catalogues can ignore
// the helper entirely.
func CallerIdentityFromContext(ctx context.Context) (AgentIdentity, bool) {
	return engine.CallerIdentityFromContext(ctx)
}

// --- Session codec (was session_codec.go) -----------------------------------

// Well-known session parameter keys used by the built-in adapters.
//
// Hosts should prefer SessionCodec over direct map access, but these constants
// define the stable meanings for the SDK's built-in adapters and examples.
const (
	// SessionParamCWD records the workspace directory captured in a session.
	SessionParamCWD = engine.SessionParamCWD
	// SessionParamWorkspaceID records the SDK workspace lease identifier.
	SessionParamWorkspaceID = engine.SessionParamWorkspaceID
	// SessionParamPromptBundleKey records the prompt/skill bundle fingerprint
	// used as a resume guard by skill-aware adapters.
	//
	// Deprecated: built-in adapters now use SessionParamProfileFingerprint so
	// MCP, skills, agents, hooks, instructions, and config share one guard.
	SessionParamPromptBundleKey = engine.SessionParamPromptBundleKey
	// SessionParamProfileFingerprint records the provider-visible effective
	// profile resource fingerprint captured by a resumable session.
	SessionParamProfileFingerprint = engine.SessionParamProfileFingerprint
)

// SessionCodecFor returns the adapter's explicit session codec when available,
// otherwise it falls back to a passthrough codec that simply round-trips
// DriverSessionState fields.
func SessionCodecFor(driver DriverAdapter) SessionCodec {
	return engine.SessionCodecFor(driver)
}

// --- Skill constructors (was skill_types.go) --------------------------------

// Key is the idiomatic constructor for a SkillRef referring to a provider
// key. It is equivalent to converting the string to SkillKey directly.
func Key(k string) SkillRef { return engine.Key(k) }

// LocalSkill builds a Skill sourced from a local directory. Key defaults to
// the directory basename; callers may override it by assigning to the
// returned Skill's Key field.
func LocalSkill(dir string) Skill { return engine.LocalSkill(dir) }

// FSSkill builds a Skill sourced from a fs.FS sub-tree rooted at root.
func FSSkill(f fs.FS, root string) Skill { return engine.FSSkill(f, root) }

// InlineSkill builds a Skill whose entire content is the given SKILL.md
// string. Key is required.
func InlineSkill(key, skillMD string) Skill { return engine.InlineSkill(key, skillMD) }

// Require returns a copy of s marked Required=true with the given reason.
func Require(s Skill, reason string) Skill { return engine.Require(s, reason) }
