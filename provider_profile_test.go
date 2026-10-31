package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/skill"
)

func profileTestDriver(name, home string) driver.Driver {
	if name == "pi" {
		return pi.Driver(pi.Config{Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: home}}})
	}
	return opencode.Driver(opencode.Config{Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: home}}})
}

func profileSkillRow(t *testing.T, snapshot adaptor.ProfileSnapshot) adaptor.ResourceSnapshot {
	t.Helper()
	for _, row := range snapshot.Resources {
		if row.Kind == adaptor.ProfileResourceSkills {
			return row
		}
	}
	t.Fatal("skills row missing")
	return adaptor.ResourceSnapshot{}
}

func TestProviderProfileDurableSkills(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			destination := filepath.Join(t.TempDir(), "profile")
			definition := skill.Inline("team/proof", "---\nname: proof\ndescription: Durable skill proof.\n---\nReturn DURABLE_83.\n")
			definition.Metadata = map[string]string{driver.SkillMetadataRuntimeName: "proof"}
			a := adaptor.New(profileTestDriver(name, destination), adaptor.WithSkills(definition))
			defer a.Close(ctx)
			before, err := a.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(profileSkillRow(t, before).Managed) != 0 {
				t.Fatal("desired skill falsely observed as installed")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("ProfileState wrote profile: %v", err)
			}
			synced, err := a.SyncProfile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(profileSkillRow(t, synced).Managed, []string{"team/proof"}) {
				t.Fatalf("sync state: %+v", synced)
			}
			path := filepath.Join(destination, "skills", "proof", "SKILL.md")
			if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), "DURABLE_83") {
				t.Fatalf("installed bytes missing: %v", err)
			}
			fresh := adaptor.New(profileTestDriver(name, destination))
			defer fresh.Close(ctx)
			observed, err := fresh.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(profileSkillRow(t, observed).Managed, []string{"team/proof"}) {
				t.Fatalf("fresh Agent lost managed ownership: %+v", observed)
			}
			external := filepath.Join(destination, "skills", "external", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(external), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, []byte("external content"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := a.SelectSkills(ctx, []string{}); err != nil {
				t.Fatal(err)
			}
			cleared, err := a.SyncProfile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			row := profileSkillRow(t, cleared)
			if len(row.Managed) != 0 || !reflect.DeepEqual(row.External, []string{"external"}) {
				t.Fatalf("prune state: %+v", row)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("managed skill not removed: %v", err)
			}
			if raw, err := os.ReadFile(external); err != nil || string(raw) != "external content" {
				t.Fatal("external skill changed")
			}
		})
	}
}

func TestProviderProfileSkillConflictAndValidation(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			destination := t.TempDir()
			external := filepath.Join(destination, "skills", "proof", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(external), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, []byte("user-owned"), 0600); err != nil {
				t.Fatal(err)
			}
			definition := skill.Inline("proof", "---\nname: proof\ndescription: proof\n---\nSDK-owned\n")
			a := adaptor.New(profileTestDriver(name, destination), adaptor.WithSkills(definition))
			defer a.Close(ctx)
			if _, err := a.SyncProfile(ctx); err == nil || !strings.Contains(err.Error(), "external installation") {
				t.Fatalf("conflict: %v", err)
			}
			if raw, err := os.ReadFile(external); err != nil || string(raw) != "user-owned" {
				t.Fatal("external skill overwritten")
			}
			absent := filepath.Join(t.TempDir(), "absent")
			invalid := adaptor.New(profileTestDriver(name, absent), adaptor.WithProfile(profile.Dedicated(absent)), adaptor.WithSkills(definition), adaptor.WithProfileResources(profile.Resources{Hooks: []profile.Hook{}}))
			defer invalid.Close(ctx)
			if _, err := invalid.SyncProfile(ctx); err == nil {
				t.Fatal("unsupported durable resource silently ignored")
			}
			if _, err := os.Stat(absent); !os.IsNotExist(err) {
				t.Fatalf("unsupported sync partially materialized skills: %v", err)
			}
		})
	}
}
