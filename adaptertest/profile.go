package adaptertest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func checkProfileResources(t *testing.T, d driver.Driver, desc driver.Descriptor, c *suiteConfig) {
	capability, ok := d.(driver.ProfileResourceDriver)
	if !ok {
		t.Skip("ProfileResourceDriver not implemented")
	}
	for _, mode := range []driver.ProfileMode{driver.ProfileModeDedicated, driver.ProfileModeClone} {
		t.Run(string(mode), func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "absent")
			source := t.TempDir()
			selection := &driver.ProfileSelection{Mode: mode, Dir: destination, From: source, Clone: &driver.CloneProfileOptions{IncludeSettings: true, IncludeSkills: true}}
			payload := driver.ProfilePayload{Fingerprint: "adaptertest-desired"}
			snapshot, err := capability.SnapshotProfileResources(probeContext(t), c.config, driver.AgentIdentity{}, selection, payload, nil, nil)
			if err != nil {
				t.Fatalf("PRO-01: snapshot of missing profile: %v", err)
			}
			if snapshot.DriverType != desc.Type || snapshot.Profile.Dir != destination || snapshot.Fingerprint != payload.Fingerprint {
				t.Errorf("PRO-02: snapshot identity or desired fingerprint not preserved: %+v", snapshot)
			}
			kinds := map[driver.ProfileResourceKind]bool{}
			for _, resource := range snapshot.Resources {
				if kinds[resource.Kind] {
					t.Errorf("PRO-02: duplicate resource kind %q", resource.Kind)
				}
				kinds[resource.Kind] = true
				if len(resource.Managed) != 0 || len(resource.External) != 0 {
					t.Errorf("PRO-02: missing profile has observed resources: %+v", resource)
				}
			}
			for _, kind := range []driver.ProfileResourceKind{driver.ProfileResourceSkills, driver.ProfileResourceMCP, driver.ProfileResourceAgents, driver.ProfileResourceHooks, driver.ProfileResourceInstructions, driver.ProfileResourceConfig} {
				if !kinds[kind] {
					t.Errorf("PRO-02: snapshot omits %s", kind)
				}
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Errorf("PRO-01: snapshot initialized missing profile: %v", err)
			}
			if skills, ok := d.(driver.SkillSupport); ok {
				if _, err := skills.ListSkills(probeContext(t), c.config, driver.ResolvedSkills{}, nil, nil, selection); err != nil {
					t.Fatalf("PRO-01: read-only skills probe: %v", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Errorf("PRO-01: skills probe initialized missing profile: %v", err)
				}
			}
		})
	}
}
