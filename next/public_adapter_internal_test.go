package adaptor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

func TestWorkspaceBoundaryPreservesSpecsNilAndOwnership(t *testing.T) {
	tests := []struct {
		name string
		spec WorkspaceSpec
		want any
	}{
		{"nil", nil, nil},
		{"typed nil", (*GitWorktreeWorkspace)(nil), nil},
		{"shared", SharedWorkspace{}, engine.SharedWorkspace{}},
		{"adapter", AdapterManagedWorkspace{}, engine.AdapterManagedWorkspace{}},
		{"worktree", GitWorktreeWorkspace{BaseRef: "main", BranchTemplate: "run-{id}", WorktreeParentDir: "/tmp/wt"}, engine.GitWorktreeWorkspace{BaseRef: "main", BranchTemplate: "run-{id}", WorktreeParentDir: "/tmp/wt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workspaceSpecToEngine(tc.spec)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("engine spec = %#v, want %#v", got, tc.want)
			}
			if tc.want != nil {
				back := workspaceSpecFromEngine(got)
				if !reflect.DeepEqual(back, tc.spec) {
					t.Fatalf("round trip = %#v, want %#v", back, tc.spec)
				}
			}
		})
	}

	metadata := map[string]string{"owner": "host"}
	converted := workspaceRequestToEngine(WorkspaceRequest{Metadata: metadata})
	metadata["owner"] = "mutated"
	if converted.Metadata["owner"] != "host" {
		t.Fatal("workspace conversion retained caller metadata")
	}
	public := workspaceRequestFromEngine(converted)
	converted.Metadata["owner"] = "engine-mutated"
	if public.Metadata["owner"] != "host" {
		t.Fatal("workspace reverse conversion retained engine metadata")
	}
}

type labelsCapturingServiceManager struct{ labels map[string]string }

func (m *labelsCapturingServiceManager) Ensure(context.Context, ServiceRequest) ([]ServiceRef, error) {
	return nil, nil
}
func (m *labelsCapturingServiceManager) ReleaseByRun(context.Context, string) error { return nil }
func (m *labelsCapturingServiceManager) ReleaseByLabels(_ context.Context, labels map[string]string) error {
	m.labels = labels
	return nil
}

func TestServiceBoundaryPreservesIdentityAndOwnership(t *testing.T) {
	metadata := map[string]string{"tenant": "a"}
	desiredMetadata := map[string]string{"service": "db"}
	req := ServiceRequest{
		RunID:      "run-1",
		DriverType: "fake",
		Agent:      Identity{ID: "agent", Tenant: "tenant", Profile: "profile", Name: "name"},
		Desired:    []ServiceSpec{{ID: "db", Metadata: desiredMetadata}},
		Metadata:   metadata,
	}
	converted := serviceRequestToEngine(req)
	if converted.Agent != (driver.AgentIdentity{ID: "agent", TenantID: "tenant", ProfileID: "profile", Name: "name"}) {
		t.Fatalf("engine identity = %#v", converted.Agent)
	}
	metadata["tenant"] = "mutated"
	desiredMetadata["service"] = "mutated"
	if converted.Metadata["tenant"] != "a" || converted.Desired[0].Metadata["service"] != "db" {
		t.Fatal("service conversion retained caller maps")
	}

	back := serviceRequestFromEngine(converted)
	converted.Metadata["tenant"] = "engine-mutated"
	converted.Desired[0].Metadata["service"] = "engine-mutated"
	if back.Agent != req.Agent || back.Metadata["tenant"] != "a" || back.Desired[0].Metadata["service"] != "db" {
		t.Fatalf("public round trip = %#v", back)
	}

	manager := &labelsCapturingServiceManager{}
	labels := map[string]string{"task": "42"}
	if err := (serviceManagerAdapter{target: manager}).ReleaseByLabels(context.Background(), labels); err != nil {
		t.Fatal(err)
	}
	labels["task"] = "mutated"
	if manager.labels["task"] != "42" {
		t.Fatal("release adapter retained caller labels")
	}
}

func TestProfileSnapshotBoundaryDeepCopiesAndMapsEnums(t *testing.T) {
	managed := []string{"sdk"}
	warnings := []string{"warning"}
	engineSnapshot := engine.ProfileSnapshot{
		DriverType: "fake",
		Kind:       engine.ProfileKindHostManaged,
		Resources: []engine.ResourceSnapshot{{
			Kind:            engine.ProfileResourceSkills,
			Managed:         managed,
			Support:         engine.ProfileResourceSupportPortableCore,
			Materialization: engine.ProfileResourceMaterializationFileManaged,
			Warnings:        warnings,
		}},
		Warnings: warnings,
	}
	got := profileSnapshotFromEngine(engineSnapshot)
	if got.Kind != ProfileKindHostManaged || got.Resources[0].Kind != ProfileResourceSkills ||
		got.Resources[0].Support != ProfileResourceSupportPortableCore ||
		got.Resources[0].Materialization != ProfileResourceMaterializationFileManaged {
		t.Fatalf("enum mapping = %#v", got)
	}
	managed[0], warnings[0] = "mutated", "mutated"
	if got.Resources[0].Managed[0] != "sdk" || got.Resources[0].Warnings[0] != "warning" || got.Warnings[0] != "warning" {
		t.Fatal("profile conversion retained engine slices")
	}

	if nilSnapshot := profileSnapshotFromEngine(engine.ProfileSnapshot{}); nilSnapshot.Resources != nil || nilSnapshot.Warnings != nil {
		t.Fatalf("nil slices changed shape: %#v", nilSnapshot)
	}
	emptySnapshot := profileSnapshotFromEngine(engine.ProfileSnapshot{Resources: []engine.ResourceSnapshot{}, Warnings: []string{}})
	if emptySnapshot.Resources == nil || emptySnapshot.Warnings == nil {
		t.Fatalf("explicit empty slices changed to nil: %#v", emptySnapshot)
	}
}

func TestLeafErrorAliasesRemainUsableThroughRoot(t *testing.T) {
	cause := errors.New("cause")
	err := &SkillMaterializationError{Key: "review", Cause: cause}
	if !errors.Is(err, ErrSkillMaterializationFailed) || !errors.Is(err, cause) {
		t.Fatalf("root skill error identity lost: %v", err)
	}
}
