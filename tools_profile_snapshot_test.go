package adaptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

// Values are synthetic. The complete shape is evidenced by the official
// Claude 2.1.159 bootstrap and its exact migration completion version 13.
func alignmentClaudeBootstrap() map[string]any {
	return map[string]any{"firstStartTime": "2026-09-16T01:02:03.456Z", "userID": strings.Repeat("a", 64), "seenNotifications": map[string]any{}, "migrationVersion": 13, "opusProMigrationComplete": true, "sonnet1m45MigrationComplete": true}
}

func TestAlignmentProfileClaudeCompletedBootstrap(t *testing.T) {
	dir := t.TempDir()
	fingerprint := func() string {
		t.Helper()
		fp, err := hostedToolMaterializedProfileFingerprint("claude", dir)
		if err != nil {
			t.Fatal(err)
		}
		return fp
	}
	write := func(object map[string]any) {
		t.Helper()
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	before := fingerprint()
	emptyDigest := sha256.Sum256(nil)
	wantLegacyEmpty := engine.StableHash("adaptor/hosted-tool-materialized-profile/v2", "claude", []hostedToolProfileFingerprintEntry{{Path: ".claude.json", Mode: hostedToolMCPMode(runtime.GOOS, nil), Fingerprint: hex.EncodeToString(emptyDigest[:])}})
	if before != wantLegacyEmpty {
		t.Fatal("empty-profile legacy fingerprint changed")
	}
	write(alignmentClaudeBootstrap())
	if got := fingerprint(); got != before {
		t.Fatal("completed provider bootstrap changed otherwise empty profile compatibility")
	}
	varying := alignmentClaudeBootstrap()
	varying["firstStartTime"] = "2026-09-17T02:03:04.567Z"
	varying["userID"] = strings.Repeat("b", 64)
	varying["seenNotifications"] = map[string]any{"subscription-switch": 2}
	write(varying)
	physicalBefore, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint() != before {
		t.Fatal("completed bookkeeping values changed compatibility")
	}
	physicalAfter, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil || string(physicalBefore) != string(physicalAfter) {
		t.Fatal("compatibility view rewrote provider configuration")
	}
	for _, key := range []string{"firstStartTime", "userID", "seenNotifications", "migrationVersion", "opusProMigrationComplete", "sonnet1m45MigrationComplete"} {
		t.Run("missing_"+key, func(t *testing.T) {
			v := alignmentClaudeBootstrap()
			delete(v, key)
			write(v)
			if fingerprint() == before {
				t.Fatal("incomplete bootstrap was normalized")
			}
		})
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"false_opus", "opusProMigrationComplete", false}, {"false_sonnet", "sonnet1m45MigrationComplete", false},
		{"old_migration", "migrationVersion", 12}, {"unknown_migration", "migrationVersion", 14}, {"string_migration", "migrationVersion", "13"},
		{"invalid_time", "firstStartTime", "invalid"}, {"invalid_id", "userID", "other"}, {"uppercase_id", "userID", strings.Repeat("A", 64)}, {"negative_notice", "seenNotifications", map[string]any{"notice": -1}}, {"fractional_notice", "seenNotifications", map[string]any{"notice": 0.5}}, {"invalid_notice", "seenNotifications", []any{}},
		{"real_model", "model", "other-model"}, {"project_settings", "projects", map[string]any{"workspace": map[string]any{"allowedTools": []string{"Bash"}}}}, {"unknown", "futureSetting", true},
		{"nested_metadata", "nested", alignmentClaudeBootstrap()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := alignmentClaudeBootstrap()
			v[tc.key] = tc.value
			write(v)
			if fingerprint() == before {
				t.Fatal("unproved or semantic configuration was ignored")
			}
		})
	}
	for _, path := range []string{"settings.json", "config.json"} {
		t.Run(path, func(t *testing.T) {
			write(alignmentClaudeBootstrap())
			if err := os.WriteFile(filepath.Join(dir, path), []byte(`{"model":"other"}`), 0644); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(filepath.Join(dir, path))
			if fingerprint() == before {
				t.Fatal("actual settings ignored")
			}
		})
	}
	write(alignmentClaudeBootstrap())
	info, err := os.Stat(filepath.Join(dir, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, ".claude.json"), 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(dir, ".claude.json"), info.Mode().Perm())
	if fingerprint() == before {
		t.Fatal("actual mode drift ignored")
	}
}

func TestAlignmentProfileClaudeBootstrapOldRecordBoundary(t *testing.T) {
	dir := t.TempDir()
	raw, err := json.Marshal(alignmentClaudeBootstrap())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	legacyMaterialized := engine.StableHash("adaptor/hosted-tool-materialized-profile/v2", "claude", []hostedToolProfileFingerprintEntry{{Path: ".claude.json", Mode: info.Mode().Perm(), Fingerprint: hex.EncodeToString(digest[:])}})
	canonicalMaterialized, err := hostedToolMaterializedProfileFingerprint("claude", dir)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalMaterialized == legacyMaterialized {
		t.Fatal("fixture did not exercise changed legacy metadata hash")
	}
	d := claude.Driver(claude.Config{})
	a := New(d)
	defer a.Close(context.Background())
	contract, err := validateThreadDriverContract(d)
	if err != nil {
		t.Fatal(err)
	}
	makeFingerprint := func(materialized string) string {
		profile := hostedToolProfileCompatibilityView{Version: "persistent-clone/v1", SourceDir: "/fixture", MaterializedFingerprint: materialized}
		req := driver.Request{ProfilePayload: driver.ProfilePayload{SessionCompatibilityFingerprint: engine.StableHash("adaptor/resolved-profile-session/v1", "declared", materialized)}}
		return a.threadInvocationFingerprint(driver.AgentIdentity{}, req, contract, "mcp", profile)
	}
	old := threadstore.Record{ID: "legacy-record", Key: "legacy-key", Status: threadstore.StatusActive, DriverType: "claude", SessionCodec: contract.codecName, State: &driver.SessionState{ResumeID: "legacy-provider"}, Fingerprint: makeFingerprint(legacyMaterialized), CompatibilityFingerprint: makeFingerprint(legacyMaterialized)}
	store := memory.NewStore()
	ctx := context.Background()
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{Record: old, Key: old.Key, RebindActive: true}); err != nil {
		t.Fatal(err)
	}
	_, err = engine.PrepareThreadSessionForDriver(ctx, engineStore{store: store}, engine.SessionRequest{Namespace: threadNamespace, Key: old.Key, Mode: driver.SessionContinueOnly}, driver.AgentIdentity{}, d, makeFingerprint(canonicalMaterialized))
	if !errors.Is(err, engine.ErrSessionIncompatible) {
		t.Fatalf("legacy ResumeOnly result=%v", err)
	}
	after, err := store.Resolve(ctx, threadstore.Query{Key: old.Key})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&old, after) {
		t.Fatal("rejection mutated healthy legacy checkpoint")
	}
}

// Complete path/type/content evidence for the official 0.153.4 embedded assets
// at 3d2ee51ca2d5db578f328aa75e20aa22c0197c9a. The installer's default observed
// modes are supplied explicitly below; executable source bits are not preserved
// by its fs::write. The marker is computed by the official sorted v1 hash.
const alignmentCodexSystemManifest = `d skills/.system
af2d6853e26c9bf6a4ad2e9588586dba9f06fdc4b91864861b9866921abe98ac skills/.system/.codex-system-skills.marker
d skills/.system/imagegen
4dd13869245e356246a5b770723247bbb80a8f07a181d1d3d873a1734297cdb9 skills/.system/imagegen/LICENSE.txt
681ddb4ad6d06a2acc78a3535b583f8d0c1ea800ecda3d56370d3310fd2cd4ba skills/.system/imagegen/SKILL.md
d skills/.system/imagegen/agents
9ca574af14580dc7a2a3dc37a1796d17f93cb8850be66501f0799ef8603e9dc0 skills/.system/imagegen/agents/openai.yaml
d skills/.system/imagegen/assets
cff5f34f57ff60b3ee92eaedd17b15e96dd4b9e776df3e78936c9e00d42be294 skills/.system/imagegen/assets/imagegen-small.svg
95952f644064eb9e890f98d8db07216347186526e4c41ad66d3420629eb86e20 skills/.system/imagegen/assets/imagegen.png
d skills/.system/imagegen/references
ecfc2e09261a0feb3482517a5fa0ff410cb7d1958e3cbd2ac6b61586f5b81405 skills/.system/imagegen/references/cli.md
c88298ca4481f6116a16fa7987434fc977f8b311c1bbc0c3d862ffd0c5981148 skills/.system/imagegen/references/codex-network.md
dc975d7af8a4888967251a0276014b4a71ea30455294944b762256373ce3e569 skills/.system/imagegen/references/image-api.md
b210b051c775860267080941eba968212bf0ac7fce581d75c5dcc217d8293f8b skills/.system/imagegen/references/prompting.md
70474177d151855b175c6133de2aae1d90b7f146b0dab50ec830972c47d72183 skills/.system/imagegen/references/sample-prompts.md
d skills/.system/imagegen/scripts
b4345cf835e5b593b97df6bb2b70bda493b8a97eee4cabbc99b2fa57dba9b448 skills/.system/imagegen/scripts/image_gen.py
a5893c4bd04b21abced33731ea89d2ac428e81d92a0368eb18ffbe4fda5253e2 skills/.system/imagegen/scripts/remove_chroma_key.py
d skills/.system/openai-docs
4dd13869245e356246a5b770723247bbb80a8f07a181d1d3d873a1734297cdb9 skills/.system/openai-docs/LICENSE.txt
7cb8fa1b2a0c635b5c61ffe1da7b8594a7ea0fce5b71e8d523e2025d88b2a05e skills/.system/openai-docs/SKILL.md
d skills/.system/openai-docs/agents
44b9efac6be1bae32d869aa2942fecbe4dcae82682ee03e4120f2f9b7d4658ec skills/.system/openai-docs/agents/openai.yaml
d skills/.system/openai-docs/assets
45be1f0757eb18889eefb1e7db79668ef46a275dc4e0e78e8df5ebd7f6cdeadc skills/.system/openai-docs/assets/openai-small.svg
156cc84d7332bfe95b310350bd470b690d22aa33d65340cc6c2e06022946194c skills/.system/openai-docs/assets/openai.png
d skills/.system/openai-docs/references
8c8fb00e6e5cb1977924f5164684a6095427fa828bbc765225f17d9aeb79a912 skills/.system/openai-docs/references/codex-self-knowledge.md
f25e351e522dd6e30e82d482f31f44c992e794b11031cdcb6ac7c0e6b20c9d5d skills/.system/openai-docs/references/latest-model.md
49bbd2f73df7bbd7f86c80425dea4da2d301c22046080399a36bfc0ca49509e9 skills/.system/openai-docs/references/mcp-diagnostics.md
5f20c38fbbb10319767b216d91ba74bae49c68fc1bfd6d1abd7c9b4cc9cb9ab0 skills/.system/openai-docs/references/model-migration.md
ba2d164abbca30435a460a0bc3a7d82398dce2bdf092705c98ba55b3f3af38a8 skills/.system/openai-docs/references/model-selection.md
7962f2dce55089b93bde4115bb89fd42f20993c1597a2b13edd4956f463875b9 skills/.system/openai-docs/references/official-docs.md
db913884cfe0fabf29bee1a139918f56e299decfa0a14d61c48596f23f76621d skills/.system/openai-docs/references/prompting-guide.md
ed1b75a89b8ec4d67787774ef6c4e8b98eace16c63348f42e969e6ffa67cb656 skills/.system/openai-docs/references/upgrade-guide.md
9a918a0c8dd051d574f2fd0309201afa8a9b9c08241c11ca1fb0ac2f4724e7ac skills/.system/openai-docs/references/upgrading-to-gpt-5p6-sol.md
d skills/.system/openai-docs/scripts
f53eb6d2f286e9efcc397e8bee93a938e37296c90953e4e06e94899ef1b6c363 skills/.system/openai-docs/scripts/fetch-codex-manual.mjs
7354dbb030ca0736dd633a7ca1b930cf640abd40370725dea3a458cb51d49523 skills/.system/openai-docs/scripts/resolve-latest-model-info
eeb1bb486018e16b37edfc06b1a37179dbc672982d501040d4f7142f29dd2e64 skills/.system/openai-docs/scripts/resolve-latest-model-info.cjs
d skills/.system/plugin-creator
71b95b8219644f95d633721e7f7cd3c469edfc8fe50f8415d400dfb2d74bc7b9 skills/.system/plugin-creator/SKILL.md
d skills/.system/plugin-creator/agents
fecaf35d692bd3d33d1a065648258d12e393afa9055d78adf6e57b42f4142f6d skills/.system/plugin-creator/agents/openai.yaml
d skills/.system/plugin-creator/assets
6591bf8ea9bb9435890dbdea299e0d2bd05f3aa893a335d26e4c535e93c8e7fb skills/.system/plugin-creator/assets/plugin-creator-small.svg
a4024b0306ddb05847e1012879d37aaf1e658205199da596f5145ed7a88d9162 skills/.system/plugin-creator/assets/plugin-creator.png
d skills/.system/plugin-creator/references
91c4781d48568fcc708b45566b08fb610ad1c88672720ae512f9525a1cf9cb20 skills/.system/plugin-creator/references/installing-and-updating.md
eeb640130f69636affaa299d4170d5a7ae6a0ff978296ddf75c409ce6dd87b91 skills/.system/plugin-creator/references/plugin-json-spec.md
d skills/.system/plugin-creator/scripts
272cb14e02ad7c76ac40777443c49cfecfa333ee28ab7f6214210b72c8fd02ae skills/.system/plugin-creator/scripts/create_basic_plugin.py
a6d51ce4a9a7e8f85626ff5808a467a67574e7f8cdf1167ffb467c5f67e57223 skills/.system/plugin-creator/scripts/identifier_validation.py
ba24e6d91eed6f778bde022a967be335c6253983b5ecd1c5e30c8483385887fd skills/.system/plugin-creator/scripts/read_marketplace_name.py
6a630dfbdc2bca6952580da9c2c7ece08030926819ccd81d9deb518c24c1b0ed skills/.system/plugin-creator/scripts/update_plugin_cachebuster.py
f4eeadb733b28b0c3e714de263a76d6542866a672f3e99bdffcf4dbcdf85e944 skills/.system/plugin-creator/scripts/validate_plugin.py
d skills/.system/review-agent
07079efd0dc76f05fade424e5dfb048dce1de2df7626e1a4f56292a4f3f92228 skills/.system/review-agent/SKILL.md
d skills/.system/review-agent/agents
4d867a46d15e36ac880176484aae160f59855340c6059b2ea6ab9fbc9af084de skills/.system/review-agent/agents/openai.yaml
d skills/.system/skill-creator
6656e54755638e8efcf275a472b9672eaa8a9a1b9e59dc210e275b03b59e1e66 skills/.system/skill-creator/SKILL.md
d skills/.system/skill-creator/agents
d07d21b93fcf3d4dc8d9a3399c05fc226a49a333a96d3e1c68b451b8dd9eade6 skills/.system/skill-creator/agents/openai.yaml
d skills/.system/skill-creator/assets
6591bf8ea9bb9435890dbdea299e0d2bd05f3aa893a335d26e4c535e93c8e7fb skills/.system/skill-creator/assets/skill-creator-small.svg
a4024b0306ddb05847e1012879d37aaf1e658205199da596f5145ed7a88d9162 skills/.system/skill-creator/assets/skill-creator.png
cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30 skills/.system/skill-creator/license.txt
d skills/.system/skill-creator/references
ffac39318e408108141d40f820968e59f70434a891694f9bf1d25be8237b150c skills/.system/skill-creator/references/openai_yaml.md
d skills/.system/skill-creator/scripts
816df12072d74faf924d802a6599a139997e1441e90bfe4869ec304f2e5e2851 skills/.system/skill-creator/scripts/generate_openai_yaml.py
bf0656a5f9d8d8cdecf0245eaf27e1df8e0e9b6d40ccdda814eb8e3d5f6992c8 skills/.system/skill-creator/scripts/init_skill.py
ee6dba90f44d37171c5a6edb8095979c54919ff6822c1a907afca2e78c48738c skills/.system/skill-creator/scripts/quick_validate.py
d skills/.system/skill-installer
cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30 skills/.system/skill-installer/LICENSE.txt
d68b77e5bbb34dedab89d134da52855f140fc4b4299b80104f534e3b9e98f8ee skills/.system/skill-installer/SKILL.md
d skills/.system/skill-installer/agents
5ce223d8b1070b82c42298538f1b8d376f788eb9e7a42a987e8c094070d73f0e skills/.system/skill-installer/agents/openai.yaml
d skills/.system/skill-installer/assets
3928703ff00dc1a681e7a22401843b7edcbd4b2051651ce4c43b75f7e140504e skills/.system/skill-installer/assets/skill-installer-small.svg
d0a230b1a79b71b858b7c215a0fbb0768d6459c14ea4ef80c61592629bf0e605 skills/.system/skill-installer/assets/skill-installer.png
d skills/.system/skill-installer/scripts
61c1bbe2ae217433b4b6f9f09f21aca4df52c12598068343ade719f706e4859b skills/.system/skill-installer/scripts/github_utils.py
38f311b75664bb063808f982c600271ebc4b560830f491705190f88a94e3e781 skills/.system/skill-installer/scripts/install-skill-from-github.py
9d6dfb2abf3afeee7f027a89fb1626b918a973da18f4eb6c25681286301beb41 skills/.system/skill-installer/scripts/list-skills.py`

func alignmentCodexSystemEntries(goos string) []hostedToolProfileFingerprintEntry {
	fileMode, dirMode := fs.FileMode(0644), fs.ModeDir|0755
	if goos == "windows" {
		fileMode, dirMode = 0666, fs.ModeDir|0777
	}
	var entries []hostedToolProfileFingerprintEntry
	for _, line := range strings.Split(alignmentCodexSystemManifest, "\n") {
		digest, path, _ := strings.Cut(line, " ")
		mode := fileMode
		if digest == "d" {
			mode = dirMode
			digest = "directory"
		}
		entries = append(entries, hostedToolProfileFingerprintEntry{Path: path, Mode: mode, Fingerprint: digest})
	}
	return entries
}

func TestAlignmentProfileCodexSystemBundleProof(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			bundle := alignmentCodexSystemEntries(goos)
			if len(bundle) != 87 {
				t.Fatal("official manifest incomplete")
			}
			parentMode := fs.ModeDir | 0755
			if goos == "windows" {
				parentMode = fs.ModeDir | 0777
			}
			outside := []hostedToolProfileFingerprintEntry{{Path: "config.toml", Mode: 0600, Fingerprint: "actual settings"}, {Path: "skills", Mode: parentMode, Fingerprint: "directory"}, {Path: "skills/.system-custom", Mode: 0644, Fingerprint: "ordinary user file"}}
			entries := append(slices.Clone(bundle), outside...)
			sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
			original := slices.Clone(entries)
			if got := canonicalCodexSystemSkills(entries, goos); !reflect.DeepEqual(got, outside) {
				t.Fatalf("known bundle did not normalize exactly: %d entries", len(got))
			}
			if !reflect.DeepEqual(entries, original) {
				t.Fatal("view changed input snapshot")
			}
			for _, kind := range []string{"content", "marker", "extra", "missing", "readonly", "directory_mode", "executable"} {
				t.Run(kind, func(t *testing.T) {
					changed := slices.Clone(entries)
					i := slices.IndexFunc(changed, func(e hostedToolProfileFingerprintEntry) bool { return strings.HasSuffix(e.Path, "/SKILL.md") })
					marker := slices.IndexFunc(changed, func(e hostedToolProfileFingerprintEntry) bool {
						return strings.HasSuffix(e.Path, "/.codex-system-skills.marker")
					})
					root := slices.IndexFunc(changed, func(e hostedToolProfileFingerprintEntry) bool { return e.Path == "skills/.system" })
					switch kind {
					case "content":
						changed[i].Fingerprint = "changed actual bytes"
					case "marker":
						changed[marker].Fingerprint = "unknown embedded version"
					case "extra":
						changed = append(changed, hostedToolProfileFingerprintEntry{Path: "skills/.system/user-attachment", Mode: 0644, Fingerprint: "actual bytes"})
					case "missing":
						changed = append(changed[:i], changed[i+1:]...)
					case "readonly":
						changed[i].Mode = 0444
					case "directory_mode":
						changed[root].Mode = fs.ModeDir | 0700
					case "executable":
						changed[i].Mode = 0755
					}
					sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
					if got := canonicalCodexSystemSkills(changed, goos); !reflect.DeepEqual(got, changed) {
						t.Fatal("unproved resource or permission drift erased")
					}
				})
			}
			for _, present := range []bool{false, true} {
				changed := slices.Clone(bundle)
				if present {
					changed = append([]hostedToolProfileFingerprintEntry{{Path: "skills", Mode: fs.ModeDir | 0700, Fingerprint: "directory"}}, changed...)
				}
				got := canonicalCodexSystemSkills(changed, goos)
				if !present && len(got) != 0 {
					t.Fatal("view invented skills parent")
				}
				if present && (len(got) != 1 || got[0].Mode != fs.ModeDir|0700) {
					t.Fatal("view erased skills parent permissions")
				}
			}
		})
	}
	// A filename and plausible marker are not bundle proof, including empty data.
	for _, entries := range [][]hostedToolProfileFingerprintEntry{nil, {}, {{Path: "skills/.system/.codex-system-skills.marker", Mode: 0644, Fingerprint: "marker alone"}}} {
		if got := canonicalCodexSystemSkills(entries, "linux"); !reflect.DeepEqual(got, entries) {
			t.Fatal("unproved bootstrap normalized")
		}
	}
}

// Earlier records which hashed a pre-existing complete bundle are deliberately
// not accepted through a second legacy hash. Rejection preserves the old record.
func TestAlignmentProfileCodexBootstrapOldRecordBoundary(t *testing.T) {
	d := codex.Driver(codex.Config{})
	a := New(d)
	defer a.Close(context.Background())
	contract, err := validateThreadDriverContract(d)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest := sha256.Sum256(nil)
	original := []hostedToolProfileFingerprintEntry{{Path: "config.toml", Mode: hostedToolMCPMode(runtime.GOOS, nil), Fingerprint: hex.EncodeToString(emptyDigest[:])}}
	hash := func(entries []hostedToolProfileFingerprintEntry) string {
		return engine.StableHash("adaptor/hosted-tool-materialized-profile/v2", "codex", entries)
	}
	if hash(original) != hash(canonicalCodexSystemSkills(original, runtime.GOOS)) {
		t.Fatal("empty legacy hash changed")
	}
	oldEntries := append(slices.Clone(original), alignmentCodexSystemEntries(runtime.GOOS)...)
	sort.Slice(oldEntries, func(i, j int) bool { return oldEntries[i].Path < oldEntries[j].Path })
	oldMaterialized, newMaterialized := hash(oldEntries), hash(canonicalCodexSystemSkills(oldEntries, runtime.GOOS))
	if oldMaterialized == newMaterialized {
		t.Fatal("fixture missed affected old hash")
	}
	fingerprint := func(materialized string) string {
		view := hostedToolProfileCompatibilityView{Version: "persistent-clone/v1", SourceDir: "/fixture", MaterializedFingerprint: materialized}
		req := driver.Request{ProfilePayload: driver.ProfilePayload{SessionCompatibilityFingerprint: engine.StableHash("adaptor/resolved-profile-session/v1", "declared", materialized)}}
		return a.threadInvocationFingerprint(driver.AgentIdentity{}, req, contract, "mcp", view)
	}
	old := threadstore.Record{ID: "old-codex", Key: "codex-key", Status: threadstore.StatusActive, DriverType: "codex", SessionCodec: contract.codecName, State: &driver.SessionState{ResumeID: "old-provider"}, Fingerprint: fingerprint(oldMaterialized), CompatibilityFingerprint: fingerprint(oldMaterialized)}
	store := memory.NewStore()
	ctx := context.Background()
	if err := store.Finalize(ctx, threadstore.FinalizeRequest{Record: old, Key: old.Key, RebindActive: true}); err != nil {
		t.Fatal(err)
	}
	_, err = engine.PrepareThreadSessionForDriver(ctx, engineStore{store: store}, engine.SessionRequest{Namespace: threadNamespace, Key: old.Key, Mode: driver.SessionContinueOnly}, driver.AgentIdentity{}, d, fingerprint(newMaterialized))
	if !errors.Is(err, engine.ErrSessionIncompatible) {
		t.Fatal("affected old record silently accepted", err)
	}
	after, err := store.Resolve(ctx, threadstore.Query{Key: old.Key})
	if err != nil || !reflect.DeepEqual(&old, after) {
		t.Fatal("old healthy state was changed", err)
	}
}
