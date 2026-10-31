package adaptor_test

import (
	"context"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

// This extension uses only public packages: neither its method signatures nor
// its snapshot values need an internal/engine import.
type externalProfileDriver struct {
	driver.Driver
	reads, writes int
	last          driver.ProfilePayload
}

var _ driver.ProfileResourceDriver = (*externalProfileDriver)(nil)

func (d *externalProfileDriver) SnapshotProfileResources(_ context.Context, _ any, _ driver.AgentIdentity, selection *driver.ProfileSelection, payload driver.ProfilePayload, _ []string, _ []driver.Skill) (driver.ProfileSnapshot, error) {
	d.reads++
	d.last = payload
	snapshot := driver.ProfileSnapshot{DriverType: d.Descriptor().Type, Profile: driver.AgentProfile{Supported: true, Dir: selection.Dir}, Kind: driver.ProfileKindHostManaged, Fingerprint: payload.Fingerprint}
	for _, kind := range []driver.ProfileResourceKind{driver.ProfileResourceSkills, driver.ProfileResourceMCP, driver.ProfileResourceAgents, driver.ProfileResourceHooks, driver.ProfileResourceInstructions, driver.ProfileResourceConfig} {
		snapshot.Resources = append(snapshot.Resources, driver.ResourceSnapshot{Kind: kind, Support: driver.ProfileResourceSupportUnsupported, Materialization: driver.ProfileResourceMaterializationNotMaterialized})
	}
	return snapshot, nil
}

func (d *externalProfileDriver) SyncProfileResources(ctx context.Context, value any, identity driver.AgentIdentity, selection *driver.ProfileSelection, payload driver.ProfilePayload, selected []string, resolved []driver.Skill) (driver.ProfileSnapshot, error) {
	d.writes++
	return d.SnapshotProfileResources(ctx, value, identity, selection, payload, selected, resolved)
}

func TestExternalProfileSPIReceivesResolvedDefaults(t *testing.T) {
	d := &externalProfileDriver{Driver: adaptertest.NewReferenceDriver(adaptertest.ReferenceConfig{})}
	directory := t.TempDir()
	a := adaptor.New(d, adaptor.WithProfile(profile.Dedicated(directory)))
	defer a.Close(context.Background())
	before, err := a.ProfileState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.reads != 1 || d.writes != 0 || before.Profile.Dir != directory || before.Fingerprint == "" || before.Fingerprint != d.last.Fingerprint {
		t.Fatalf("profile SPI not dispatched: %+v", before)
	}
	after, err := a.SyncProfile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.reads != 2 || d.writes != 1 || after.Fingerprint != before.Fingerprint || len(after.Resources) != 6 {
		t.Fatalf("sync SPI lost resolved state: %+v", after)
	}
}
