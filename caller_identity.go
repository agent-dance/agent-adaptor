package agentadaptor

import "context"

// callerIdentityKey is the unexported context key the SDK uses to
// stash the caller's AgentIdentity before invoking SkillProvider hooks.
// Hosts MUST access the value through CallerIdentityFromContext rather
// than the raw key — that's the public contract.
type callerIdentityKey struct{}

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
	return context.WithValue(ctx, callerIdentityKey{}, id)
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
	if ctx == nil {
		return AgentIdentity{}, false
	}
	id, ok := ctx.Value(callerIdentityKey{}).(AgentIdentity)
	return id, ok
}
