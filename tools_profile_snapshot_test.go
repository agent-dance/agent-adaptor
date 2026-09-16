package adaptor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/claude"
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
