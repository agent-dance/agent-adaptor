package adaptor_test

// Scenario S8 (design doc docs/api-v1-redesign.md §S8): the
// tenant-isolated dedicated profile of a desktop product — a cloned
// provider profile per tenant that shares the machine's OAuth login state
// by link (never copying token files), plus profile-shaped resources
// declared on the agent.
//
// Documented deviation from the doc sketch: the doc's Resources field
// SubAgents is currently spelled Agents on the public profile.Resources
// declaration. The resource types themselves are owned by profile.

import (
	"context"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestScenarioS8TenantIsolatedProfile(t *testing.T) {
	ctx := context.Background()
	appData := t.TempDir()
	const tenantID = "tenant-42"
	fake := newFakeDriver()

	// --- design doc S8, near-verbatim ---
	agent := adaptor.New(fake,
		adaptor.WithProfile(profile.CloneNative(
			filepath.Join(appData, "profiles", tenantID),
			profile.LinkAuth(), // share the machine's OAuth login state, do not copy token files
		)),
		adaptor.WithProfileResources(profile.Resources{
			Instructions: profile.Text("Follow ACME coding standards."),
			Agents:       []profile.SubAgent{{Key: "tester", Instructions: "Write and run the tests."}},
		}),
	)
	// --- end near-verbatim ---

	if _, err := agent.Run(ctx, "fix the flaky login test"); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := fake.lastRequest(t)

	if req.Profile == nil {
		t.Fatal("driver request carries no profile selection")
	}
	if req.Profile.Mode != profile.ModeClone {
		t.Errorf("profile mode = %q, want clone", req.Profile.Mode)
	}
	if want := filepath.Join(appData, "profiles", tenantID); req.Profile.Dir != want {
		t.Errorf("profile dir = %q, want the tenant directory %q", req.Profile.Dir, want)
	}
	if req.Profile.Clone == nil || req.Profile.Clone.AuthMode != profile.AuthLink {
		t.Errorf("clone options = %#v, want linked auth", req.Profile.Clone)
	}

	if req.Instructions == nil || req.Instructions.Content != "Follow ACME coding standards." {
		t.Errorf("instructions = %#v, want the declared ACME standards", req.Instructions)
	}
	payload := req.ProfilePayload
	if !payload.Declared.Instructions || !payload.Declared.Agents {
		t.Errorf("declared = %#v, want instructions and agents declared", payload.Declared)
	}
	if len(payload.Agents.Agents) != 1 || payload.Agents.Agents[0].RuntimeName != "tester" {
		t.Errorf("agents payload = %#v, want exactly the tester sub-agent", payload.Agents.Agents)
	}
}
