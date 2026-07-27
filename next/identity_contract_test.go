package adaptor_test

import (
	"context"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestIdentityZeroValueIsExplicitlyInjected(t *testing.T) {
	fake := newFakeDriver()
	var got adaptor.Identity
	var ok bool
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		got, ok = adaptor.IdentityFromContext(ctx)
		return driver.Response{Output: "ok"}, nil
	}

	if _, err := adaptor.New(fake).Run(context.Background(), "zero", adaptor.WithIdentity(adaptor.Identity{})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ok || got != (adaptor.Identity{}) {
		t.Fatalf("IdentityFromContext = (%+v, %v), want explicit zero identity and ok=true", got, ok)
	}
}

func TestPerCallIdentityOverridesAgentDefault(t *testing.T) {
	fake := newFakeDriver()
	var got adaptor.Identity
	var identityPresent bool
	fake.runFunc = func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		got, identityPresent = adaptor.IdentityFromContext(ctx)
		return driver.Response{Output: "ok"}, nil
	}

	defaultIdentity := adaptor.Identity{ID: "default", Tenant: "tenant-a", Profile: "profile-a", Name: "Default"}
	override := adaptor.Identity{ID: "override", Tenant: "tenant-b", Profile: "profile-b", Name: "Override"}
	agent := adaptor.New(fake, adaptor.WithIdentity(defaultIdentity))
	if _, err := agent.Run(context.Background(), "override", adaptor.WithIdentity(override)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !identityPresent || got != override {
		t.Fatalf("context identity = (%+v, %v), want per-call override %+v and ok=true", got, identityPresent, override)
	}
	wantDriver := (driver.AgentIdentity{ID: override.ID, TenantID: override.Tenant, ProfileID: override.Profile, Name: override.Name})
	if requestIdentity := fake.lastRequest(t).Agent; requestIdentity != wantDriver {
		t.Fatalf("driver identity = %+v, want %+v", requestIdentity, wantDriver)
	}
}
