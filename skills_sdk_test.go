package agentadaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

type staticSkillCatalog struct {
	skills map[string]agentadaptor.Skill
	order  []agentadaptor.Skill
}

func (c *staticSkillCatalog) Resolve(_ context.Context, _ string, refs []string) ([]agentadaptor.Skill, error) {
	out := make([]agentadaptor.Skill, 0, len(refs))
	for _, ref := range refs {
		if skill, ok := c.skills[ref]; ok {
			out = append(out, skill)
		}
	}
	return out, nil
}

func (c *staticSkillCatalog) List(_ context.Context, _ string) ([]agentadaptor.Skill, error) {
	out := make([]agentadaptor.Skill, len(c.order))
	copy(out, c.order)
	return out, nil
}

func TestSDKRunMaterializesInlineSkillsAndCanonicalizesDesiredRefs(t *testing.T) {
	driver := &fakeDriver{}
	catalog := &staticSkillCatalog{
		skills: map[string]agentadaptor.Skill{
			"team/main": {
				Key:     "team/main",
				Content: "---\nname: main\n---\n",
				Files: []agentadaptor.SkillFile{
					{Path: "references/rules.md", Kind: agentadaptor.SkillFileReference, Content: "rules"},
				},
			},
			"system/core": {
				Key:            "system/core",
				Content:        "---\nname: core\n---\n",
				Required:       true,
				RequiredReason: "Required by the runtime catalog.",
			},
		},
		order: []agentadaptor.Skill{
			{
				Key:     "team/main",
				Content: "---\nname: main\n---\n",
				Files: []agentadaptor.SkillFile{
					{Path: "references/rules.md", Kind: agentadaptor.SkillFileReference, Content: "rules"},
				},
			},
			{
				Key:            "system/core",
				Content:        "---\nname: core\n---\n",
				Required:       true,
				RequiredReason: "Required by the runtime catalog.",
			},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver, agentadaptor.WithDefaultSkills("main"))),
		agentadaptor.WithSkillCatalog(catalog),
	)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(driver.lastSkills.Requested) != 2 {
		t.Fatalf("expected desired skills to include canonical + required, got %#v", driver.lastSkills.Requested)
	}
	if driver.lastSkills.Requested[0] != "team/main" || driver.lastSkills.Requested[1] != "system/core" {
		t.Fatalf("unexpected desired skills: %#v", driver.lastSkills.Requested)
	}
	if len(driver.lastSkills.RuntimeEntries) != 2 {
		t.Fatalf("expected runtime entries for available catalog skills, got %#v", driver.lastSkills.RuntimeEntries)
	}

	var mainSource string
	for _, entry := range driver.lastSkills.RuntimeEntries {
		if entry.Key == "team/main" {
			mainSource = entry.SourcePath
			break
		}
	}
	if mainSource == "" {
		t.Fatalf("missing runtime entry for team/main: %#v", driver.lastSkills.RuntimeEntries)
	}
	if _, err := os.Stat(filepath.Join(mainSource, "SKILL.md")); err != nil {
		t.Fatalf("expected materialized SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainSource, "references", "rules.md")); err != nil {
		t.Fatalf("expected materialized reference file: %v", err)
	}
}

func TestAdminSyncSkillsUpdatesProcessLocalDesiredSelection(t *testing.T) {
	driver := &fakeDriver{}
	catalog := &staticSkillCatalog{
		skills: map[string]agentadaptor.Skill{
			"team/default": {Key: "team/default", Content: "---\nname: default\n---\n"},
			"team/review":  {Key: "team/review", Content: "---\nname: review\n---\n"},
		},
		order: []agentadaptor.Skill{
			{Key: "team/default", Content: "---\nname: default\n---\n"},
			{Key: "team/review", Content: "---\nname: review\n---\n"},
		},
	}

	sdk := agentadaptor.New(
		agentadaptor.WithDefaultAgent(fakeBinding("default", driver, agentadaptor.WithDefaultSkills("team/default"))),
		agentadaptor.WithSkillCatalog(catalog),
	)

	snapshot, err := sdk.Admin().Default().SyncSkills(context.Background(), []string{"team/review"})
	if err != nil {
		t.Fatalf("sync skills: %v", err)
	}
	if len(snapshot.Desired) != 1 || snapshot.Desired[0] != "team/review" {
		t.Fatalf("unexpected synced desired skills: %#v", snapshot.Desired)
	}

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run after sync: %v", err)
	}
	if len(driver.lastSkills.Requested) != 1 || driver.lastSkills.Requested[0] != "team/review" {
		t.Fatalf("expected run to use synced desired skills, got %#v", driver.lastSkills.Requested)
	}

	listed, err := sdk.Admin().Default().ListSkills(context.Background())
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(listed.Desired) != 1 || listed.Desired[0] != "team/review" {
		t.Fatalf("expected list to reflect synced desired skills, got %#v", listed.Desired)
	}
}
