package adaptor

import (
	"context"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Identity is host-supplied caller identity propagated into SDK hooks and
// the driver request. The SDK does not use these fields for routing; they
// exist so host-provided components (SkillProvider, WorkspaceManager,
// ServiceManager) can scope lookups without inventing their own
// context keys.
type Identity struct {
	// ID is the logical agent identifier.
	ID string
	// Tenant partitions catalogues and stores by tenant.
	Tenant string
	// Profile partitions user-private resources within a tenant.
	Profile string
	// Name is the logical agent display name.
	Name string
}

// driverIdentity maps the consumer Identity onto the driver SPI contract.
func (id Identity) driverIdentity() driver.AgentIdentity {
	return driver.AgentIdentity{
		ID:        id.ID,
		TenantID:  id.Tenant,
		ProfileID: id.Profile,
		Name:      id.Name,
	}
}

func identityFromDriver(id driver.AgentIdentity) Identity {
	return Identity{
		ID:      id.ID,
		Tenant:  id.TenantID,
		Profile: id.ProfileID,
		Name:    id.Name,
	}
}

// identityContextKey is the unexported context key the SDK uses to stash the
// caller Identity before invoking hooks and the driver. Read it through
// IdentityFromContext — that is the public contract.
type identityContextKey struct{}

// contextWithIdentity attaches id to ctx. The run pipeline injects it
// automatically before dispatching. Consumers read the value through
// IdentityFromContext.
func contextWithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityContextKey{}, id)
}

// IdentityFromContext returns the Identity the SDK injected into ctx before
// invoking provider hooks or the driver. Implementations that need scoping
// (Tenant for catalogue partitioning, Profile for user-private skills, ...)
// read it via this helper. The boolean is false when ctx carries no identity
// (e.g. a provider invoked directly in tests without SDK plumbing).
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	id, ok := ctx.Value(identityContextKey{}).(Identity)
	return id, ok
}
