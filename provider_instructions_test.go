package adaptor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/profile"
)

func TestOpenCodeReplacementSyncRemainsExplicitlyUnsupported(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "absent")
	a := adaptor.New(profileTestDriver("opencode", directory), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{Content: "replacement", Mode: profile.InstructionModeReplace}}))
	defer a.Close(context.Background())
	snapshot, err := a.ProfileState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if row := instructionRow(t, snapshot); row.Support != adaptor.ProfileResourceSupportUnsupported || len(row.Managed) != 0 {
		t.Fatal("invocation replacement was advertised as durable sync")
	}
	if _, err := a.SyncProfile(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("sync=%v", err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("unsupported sync initialized profile")
	}
}

func TestProviderInstructionValidationBeforeProfileCreation(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			for _, ref := range []*driver.InstructionsBundleRef{
				{Content: "text", Mode: "invalid-mode"},
				{Content: "text", Scope: "invalid-scope"},
				{Content: "text", Native: map[string]any{"unknown": true}},
				{Content: "text", Path: "both-are-set"},
				{Path: filepath.Join(t.TempDir(), "missing-instructions.md")},
			} {
				directory := filepath.Join(t.TempDir(), "absent")
				d := profileTestDriver(name, directory)
				_, err := d.Run(context.Background(), driver.Request{Instructions: ref, Profile: &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: directory}}, &testutil.EventRecorder{})
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "instruction") {
					t.Fatalf("invalid instruction was not rejected before launch: %v", err)
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatalf("invalid instruction initialized profile: %v", err)
				}
			}
		})
	}
}

func TestProviderInstructionObservationReturnsSourceIOFailure(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "absent")
			a := adaptor.New(profileTestDriver(name, directory), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{Path: filepath.Join(t.TempDir(), "missing.md"), Fingerprint: "host-supplied-fingerprint"}}))
			defer a.Close(context.Background())
			if _, err := a.ProfileState(context.Background()); err == nil {
				t.Fatal("missing source was downgraded to an unsupported warning")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatal("failed source observation initialized profile")
			}
		})
	}
}

func instructionRow(t *testing.T, snapshot adaptor.ProfileSnapshot) adaptor.ResourceSnapshot {
	t.Helper()
	for _, row := range snapshot.Resources {
		if row.Kind == adaptor.ProfileResourceInstructions {
			return row
		}
	}
	t.Fatal("instructions row missing")
	return adaptor.ResourceSnapshot{}
}

func TestProviderProfileInstructions(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			directory := filepath.Join(t.TempDir(), "profile")
			filename := "AGENTS.md"
			if name == "pi" {
				filename = "APPEND_SYSTEM.md"
			}
			path := filepath.Join(directory, filename)
			makeAgent := func(options ...adaptor.Option) *adaptor.Agent {
				a := adaptor.New(profileTestDriver(name, directory), options...)
				t.Cleanup(func() { _ = a.Close(ctx) })
				return a
			}
			a := makeAgent(adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{ID: "team", Content: "ORIGINAL_56", Scope: profile.InstructionScopeUser}}))
			before, err := a.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(instructionRow(t, before).Managed) != 0 {
				t.Fatal("desired instructions reported as installed")
			}
			if _, err := os.Stat(directory); !os.IsNotExist(err) {
				t.Fatalf("instruction inspection initialized profile: %v", err)
			}
			synced, err := a.SyncProfile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			row := instructionRow(t, synced)
			if !reflect.DeepEqual(row.Managed, []string{"team"}) || row.Materialization != adaptor.ProfileResourceMaterializationNativeManaged {
				t.Fatalf("sync state: %+v", row)
			}
			fresh := makeAgent()
			observed, err := fresh.ProfileState(ctx)
			if err != nil || !reflect.DeepEqual(instructionRow(t, observed).Managed, []string{"team"}) {
				t.Fatalf("fresh observation: %+v %v", observed, err)
			}
			if _, err := fresh.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(path); err != nil || string(raw) != "ORIGINAL_56" {
				t.Fatal("undeclared instructions were removed or changed")
			}
			updated := makeAgent(adaptor.WithInstructions("UPDATED_67"))
			if _, err := updated.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if raw, err := os.ReadFile(path); err != nil || string(raw) != "UPDATED_67" {
				t.Fatal("new bundle did not replace old managed bytes")
			}
			if err := os.WriteFile(path, []byte("USER_EDIT_78"), 0600); err != nil {
				t.Fatal(err)
			}
			observed, err = fresh.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			row = instructionRow(t, observed)
			if len(row.Managed) != 0 || !reflect.DeepEqual(row.External, []string{filename}) {
				t.Fatalf("external edit still claimed as managed: %+v", row)
			}
			clear := makeAgent(adaptor.WithInstructions(""))
			for _, agent := range []*adaptor.Agent{updated, clear} {
				if _, err := agent.SyncProfile(ctx); err == nil || !strings.Contains(err.Error(), "externally modified") {
					t.Fatalf("external edit was overwritten/removed: %v", err)
				}
			}
			if raw, _ := os.ReadFile(path); string(raw) != "USER_EDIT_78" {
				t.Fatal("user edit lost")
			}
			if err := os.WriteFile(path, []byte("UPDATED_67"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := clear.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("explicit clear left owned instructions: %v", err)
			}
		})
	}
}

func TestProviderInstructionSyncScopeAndConflict(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			for _, scope := range []profile.InstructionScope{profile.InstructionScopeRun, profile.InstructionScopeProject, profile.InstructionScopeLocal} {
				directory := filepath.Join(t.TempDir(), "absent")
				a := adaptor.New(profileTestDriver(name, directory), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{Content: "scoped", Scope: scope}}))
				defer a.Close(ctx)
				if _, err := a.SyncProfile(ctx); err == nil {
					t.Fatalf("%s instructions leaked into global profile", scope)
				}
				if _, err := os.Stat(directory); !os.IsNotExist(err) {
					t.Fatalf("invalid scope initialized profile: %v", err)
				}
			}
			directory := t.TempDir()
			filename := "AGENTS.md"
			if name == "pi" {
				filename = "APPEND_SYSTEM.md"
			}
			path := filepath.Join(directory, filename)
			if err := os.WriteFile(path, []byte("external"), 0600); err != nil {
				t.Fatal(err)
			}
			a := adaptor.New(profileTestDriver(name, directory), adaptor.WithInstructions("managed"))
			defer a.Close(ctx)
			if _, err := a.SyncProfile(ctx); err == nil || !strings.Contains(err.Error(), "external entry") {
				t.Fatalf("external conflict: %v", err)
			}
			if raw, _ := os.ReadFile(path); string(raw) != "external" {
				t.Fatal("external native instruction changed")
			}
		})
	}
}

func TestPiPersistentInstructionModeChanges(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	for _, mode := range []profile.InstructionMode{profile.InstructionModeAdditive, profile.InstructionModeReplace, profile.InstructionModeAdditive} {
		a := adaptor.New(profileTestDriver("pi", directory), adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{ID: "team", Content: "PI_MODE_89", Mode: mode}}))
		defer a.Close(ctx)
		if _, err := a.SyncProfile(ctx); err != nil {
			t.Fatal(err)
		}
		present, absent := "APPEND_SYSTEM.md", "SYSTEM.md"
		if mode == profile.InstructionModeReplace {
			present, absent = absent, present
		}
		if raw, err := os.ReadFile(filepath.Join(directory, present)); err != nil || string(raw) != "PI_MODE_89" {
			t.Fatalf("mode %q file missing: %v", mode, err)
		}
		if _, err := os.Stat(filepath.Join(directory, absent)); !os.IsNotExist(err) {
			t.Fatalf("old mode file retained: %v", err)
		}
	}
}
