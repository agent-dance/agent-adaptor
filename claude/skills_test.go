package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

func createClaudeSkillDir(t *testing.T, root, name string) string {
	t.Helper()
	skillDir := filepath.Join(root, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return skillDir
}

func TestListClaudeSkillsIncludesExternalInstalls(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	externalSkill := createClaudeSkillDir(t, filepath.Join(home, ".claude", "skills"), "external-checks")
	payload := agentadaptor.ResolvedSkills{
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "team/main", RuntimeName: "main", SourcePath: createClaudeSkillDir(t, t.TempDir(), "main")},
		},
	}
	selected := []string{"team/main"}

	snapshot, err := listClaudeSkills(payload, selected, nil, []agentadaptor.EnvBinding{{Name: "HOME", Value: home}})
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}

	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected managed + external entries, got %#v", snapshot.Entries)
	}
	var externalFound bool
	for _, entry := range snapshot.Entries {
		if entry.Key != "external-checks" {
			continue
		}
		externalFound = true
		if entry.State != agentadaptor.SkillStateExternal {
			t.Fatalf("expected external state, got %#v", entry)
		}
		if filepath.Clean(entry.TargetPath) != filepath.Clean(externalSkill) {
			t.Fatalf("unexpected external target path: %#v", entry)
		}
	}
	if !externalFound {
		t.Fatalf("expected external Claude skill to be listed, got %#v", snapshot.Entries)
	}
}

func TestListClaudeSkillsUsesClaudeConfigDirWhenProvided(t *testing.T) {
	configDir := t.TempDir()
	externalSkill := createClaudeSkillDir(t, filepath.Join(configDir, "skills"), "external-checks")
	payload := agentadaptor.ResolvedSkills{
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "team/main", RuntimeName: "main", SourcePath: createClaudeSkillDir(t, t.TempDir(), "main")},
		},
	}
	selected := []string{"team/main"}

	snapshot, err := listClaudeSkills(payload, selected, nil, []agentadaptor.EnvBinding{{Name: "CLAUDE_CONFIG_DIR", Value: configDir}})
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}

	var externalFound bool
	for _, entry := range snapshot.Entries {
		if entry.Key != "external-checks" {
			continue
		}
		externalFound = true
		if entry.LocationLabel != filepath.Join(configDir, "skills") {
			t.Fatalf("unexpected location label: %#v", entry)
		}
		if filepath.Clean(entry.TargetPath) != filepath.Clean(externalSkill) {
			t.Fatalf("unexpected external target path: %#v", entry)
		}
	}
	if !externalFound {
		t.Fatalf("expected external Claude skill to be listed, got %#v", snapshot.Entries)
	}
}

func TestPrepareClaudePromptBundleMaterializesSelectedSkillsOnly(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	desired := createClaudeSkillDir(t, root, "main")
	// The prompt bundle only materialises whatever lands in
	// ResolvedSkills.Entries; the SDK is responsible for filtering that list
	// down to the Selected set before the adapter sees it.
	payload := agentadaptor.ResolvedSkills{
		Entries: []agentadaptor.ResolvedSkill{
			{Key: "team/main", RuntimeName: "main", SourcePath: desired},
		},
	}

	bundleRoot, _, err := prepareClaudePromptBundle(agentadaptor.AgentIdentity{TenantID: "tenant-a"}, payload)
	if err != nil {
		t.Fatalf("prepare bundle: %v", err)
	}
	if bundleRoot == "" {
		t.Fatal("expected non-empty bundle root")
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, ".claude", "skills", "main")); err != nil {
		t.Fatalf("expected desired skill in bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bundleRoot, ".claude", "skills", "other")); !os.IsNotExist(err) {
		t.Fatalf("expected undesired skill to be excluded, got err=%v", err)
	}
}

func TestListClaudeSkillsUsesProfileSelection(t *testing.T) {
	nativeHome := t.TempDir()
	dedicated := t.TempDir()
	_ = createClaudeSkillDir(t, filepath.Join(nativeHome, ".claude", "skills"), "native-only")
	dedicatedSkill := createClaudeSkillDir(t, filepath.Join(dedicated, "skills"), "dedicated-only")
	payload := agentadaptor.ResolvedSkills{}

	snapshot, err := NewAdapter().(agentadaptor.SkillAwareDriver).ListSkills(
		context.Background(),
		agentadaptor.ClaudeConfig{
			CommonConfig: agentadaptor.CommonConfig{Env: []agentadaptor.EnvBinding{{Name: "HOME", Value: nativeHome}}},
		},
		payload,
		nil,
		nil,
		&agentadaptor.ProfileSelection{Mode: agentadaptor.ProfileModeDedicated, Dir: dedicated},
	)
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	var foundDedicated, foundNative bool
	for _, entry := range snapshot.Entries {
		if entry.Key == "dedicated-only" && filepath.Clean(entry.TargetPath) == filepath.Clean(dedicatedSkill) {
			foundDedicated = true
		}
		if entry.Key == "native-only" {
			foundNative = true
		}
	}
	if !foundDedicated || foundNative {
		t.Fatalf("expected dedicated profile skills only, foundDedicated=%v foundNative=%v entries=%#v", foundDedicated, foundNative, snapshot.Entries)
	}
}
