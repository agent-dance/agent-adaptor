package agentadaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// fsWithRules builds an fstest.MapFS with a SKILL.md and references/rules.md
// under root "main/". The returned fstest.MapFS is used by tests to validate
// that FSSkill sources are materialised correctly by the SDK.
func fsWithRules() fstest.MapFS {
	return fstest.MapFS{
		"main/SKILL.md":             &fstest.MapFile{Data: []byte("---\nname: main\n---\n")},
		"main/references/rules.md":  &fstest.MapFile{Data: []byte("rules")},
	}
}

func TestSDKRunMaterializesFSSkillsAndCanonicalizesSelectedRefs(t *testing.T) {
	driver := &fakeDriver{}
	mainSkill := agentadaptor.Skill{
		Key:    "team/main",
		Source: agentadaptor.SkillFromFS{FS: fsWithRules(), Root: "main"},
	}
	coreSkill := agentadaptor.Require(
		agentadaptor.InlineSkill("system/core", "---\nname: core\n---\n"),
		"Required by the runtime catalog.",
	)

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(agentadaptor.Key("team/main")),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"team/main":   mainSkill,
			"system/core": coreSkill,
		}),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}

	keys := driver.lastSkills.Keys()
	if len(keys) != 2 {
		t.Fatalf("expected selected keys to include binding default + required provider skill, got %#v", keys)
	}
	if keys[0] != "system/core" || keys[1] != "team/main" {
		t.Fatalf("unexpected selected keys (want sorted system/core,team/main): %#v", keys)
	}

	var mainSource string
	for _, entry := range driver.lastSkills.Entries {
		if entry.Key == "team/main" {
			mainSource = entry.SourcePath
			break
		}
	}
	if mainSource == "" {
		t.Fatalf("missing ResolvedSkill entry for team/main: %#v", driver.lastSkills.Entries)
	}
	if _, err := os.Stat(filepath.Join(mainSource, "SKILL.md")); err != nil {
		t.Fatalf("expected materialized SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainSource, "references", "rules.md")); err != nil {
		t.Fatalf("expected materialized reference file: %v", err)
	}
}

func TestAdminSetSelectedSkillsUpdatesProcessLocalSelection(t *testing.T) {
	driver := &fakeDriver{}
	defaultSkill := agentadaptor.InlineSkill("team/default", "---\nname: default\n---\n")
	reviewSkill := agentadaptor.InlineSkill("team/review", "---\nname: review\n---\n")

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver,
			agentadaptor.WithDefaultSkills(defaultSkill),
		)),
		agentadaptor.WithSkillSet(agentadaptor.SkillSet{
			"team/default": defaultSkill,
			"team/review":  reviewSkill,
		}),
	)

	snapshot, err := sdk.Admin().Default().SetSelectedSkills(context.Background(), []string{"team/review"})
	if err != nil {
		t.Fatalf("set selected skills: %v", err)
	}
	if len(snapshot.Selected) != 1 || snapshot.Selected[0] != "team/review" {
		t.Fatalf("unexpected selected skills: %#v", snapshot.Selected)
	}

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run after sync: %v", err)
	}
	keys := driver.lastSkills.Keys()
	if len(keys) != 1 || keys[0] != "team/review" {
		t.Fatalf("expected run to use the process-local selection, got %#v", keys)
	}

	listed, err := sdk.Admin().Default().ListSkills(context.Background())
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(listed.Selected) != 1 || listed.Selected[0] != "team/review" {
		t.Fatalf("expected list to reflect selection override, got %#v", listed.Selected)
	}
}
