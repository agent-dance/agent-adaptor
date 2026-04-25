package agentadaptor_test

import (
	"context"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func TestCallerIdentityFromContext_NilContext(t *testing.T) {
	t.Parallel()
	// CallerIdentityFromContext must be nil-safe: passing a nil ctx
	// is a contract test against accidental panics in adapters that
	// forget to thread ctx through middleware.
	id, ok := agentadaptor.CallerIdentityFromContext(nil) //nolint:staticcheck // intentional nil ctx
	if ok {
		t.Errorf("nil ctx: ok = true; want false")
	}
	if id != (agentadaptor.AgentIdentity{}) {
		t.Errorf("nil ctx: identity = %#v; want zero value", id)
	}
}

func TestCallerIdentityFromContext_EmptyContext(t *testing.T) {
	t.Parallel()
	id, ok := agentadaptor.CallerIdentityFromContext(context.Background())
	if ok {
		t.Errorf("empty ctx: ok = true; want false")
	}
	if id != (agentadaptor.AgentIdentity{}) {
		t.Errorf("empty ctx: identity = %#v; want zero value", id)
	}
}

func TestCallerIdentityRoundTrip(t *testing.T) {
	t.Parallel()
	want := agentadaptor.AgentIdentity{
		ID:        "caller-123",
		TenantID:  "tenant-A",
		ProfileID: "profile-prod",
		Name:      "alice",
	}
	ctx := agentadaptor.WithCallerIdentity(context.Background(), want)
	got, ok := agentadaptor.CallerIdentityFromContext(ctx)
	if !ok {
		t.Fatalf("ok = false after WithCallerIdentity; want true")
	}
	if got != want {
		t.Errorf("identity round-trip mismatch:\n  got  = %#v\n  want = %#v", got, want)
	}
}

func TestCallerIdentityNestedOverride(t *testing.T) {
	t.Parallel()
	// Inner WithCallerIdentity shadows outer. Verifies the helper
	// follows context.Value's standard lookup semantics rather than
	// merging or refusing to overwrite.
	outer := agentadaptor.AgentIdentity{TenantID: "tenant-A"}
	inner := agentadaptor.AgentIdentity{TenantID: "tenant-B", Name: "bob"}

	ctx := agentadaptor.WithCallerIdentity(context.Background(), outer)
	ctx = agentadaptor.WithCallerIdentity(ctx, inner)

	got, ok := agentadaptor.CallerIdentityFromContext(ctx)
	if !ok {
		t.Fatalf("ok = false; want true")
	}
	if got != inner {
		t.Errorf("inner override not honoured:\n  got  = %#v\n  want = %#v", got, inner)
	}
}

func TestCallerIdentityZeroValueIsValid(t *testing.T) {
	t.Parallel()
	// Storing the zero AgentIdentity is a legitimate operation
	// (e.g. tests that want to make the helper return ok=true
	// without supplying real fields). The helper distinguishes
	// "no identity" from "zero identity" via the second return.
	ctx := agentadaptor.WithCallerIdentity(context.Background(), agentadaptor.AgentIdentity{})
	got, ok := agentadaptor.CallerIdentityFromContext(ctx)
	if !ok {
		t.Fatalf("zero-value identity: ok = false; want true")
	}
	if got != (agentadaptor.AgentIdentity{}) {
		t.Errorf("zero-value identity: got %#v; want zero", got)
	}
}
