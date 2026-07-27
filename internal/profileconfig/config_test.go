package profileconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestSyncNativePatchesMaterializesRelativeConfigPatch(t *testing.T) {
	profileDir := t.TempDir()
	payload := agentadaptor.ProfileConfigPayload{
		Fingerprint: "config-fp",
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:      "sandbox",
			FileKind: agentadaptor.ProfileConfigFileTOML,
			Path:     "config.toml",
			Section:  "sandbox",
			Values:   map[string]any{"mode": "workspace-write"},
		}},
	}

	snapshot, err := SyncNativePatches(context.Background(), "codex", profileDir, payload)
	if err != nil {
		t.Fatalf("sync native patches: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportNativeEscape || snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if !sameStrings(snapshot.Managed, []string{"sandbox"}) {
		t.Fatalf("unexpected managed keys: %#v", snapshot.Managed)
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "mode = 'workspace-write'") {
		t.Fatalf("expected native patch content, got:\n%s", string(raw))
	}
}

func TestSyncNativePatchesRejectsProviderMismatch(t *testing.T) {
	_, err := SyncNativePatches(context.Background(), "codex", t.TempDir(), agentadaptor.ProfileConfigPayload{
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key: "wrong-provider",
			Native: &agentadaptor.NativeConfigPatch{
				Provider: "claude",
				FileKind: agentadaptor.ProfileConfigFileJSON,
				Path:     "settings.json",
			},
		}},
	})
	if err == nil {
		t.Fatal("expected provider mismatch to fail")
	}
}

func TestSyncNativePatchesRejectsPathEscape(t *testing.T) {
	_, err := SyncNativePatches(context.Background(), "codex", t.TempDir(), agentadaptor.ProfileConfigPayload{
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:      "escape",
			FileKind: agentadaptor.ProfileConfigFileJSON,
			Path:     "../settings.json",
		}},
	})
	if err == nil {
		t.Fatal("expected path escape to fail")
	}
}

func TestSyncNativePatchesUsesProfileLock(t *testing.T) {
	profileDir := t.TempDir()
	lock, err := profilestate.AcquireLock(context.Background(), profileDir, profilestate.LockOptions{StaleAfter: 10 * time.Minute})
	if err != nil {
		t.Fatalf("acquire initial lock: %v", err)
	}
	defer lock.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = SyncNativePatches(ctx, "codex", profileDir, agentadaptor.ProfileConfigPayload{
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:        "sandbox",
			Capability: "sandbox",
			Values:     map[string]any{"mode": "workspace-write"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "acquire profile lock") {
		t.Fatalf("expected profile lock acquisition failure, got %v", err)
	}
}

func TestSnapshotReportsUnsupportedCapabilityPatch(t *testing.T) {
	snapshot := Snapshot("codex", t.TempDir(), agentadaptor.ProfileConfigPayload{
		Patches: []agentadaptor.ProfileConfigPatch{{Key: "ui", Capability: "ui"}},
	}, true)
	if snapshot.Support != engine.ProfileResourceSupportUnsupported || snapshot.Error == "" {
		t.Fatalf("expected unsupported capability snapshot, got %#v", snapshot)
	}
}

func TestSyncNativePatchesRejectsUnsupportedCapabilityPatch(t *testing.T) {
	_, err := SyncNativePatches(context.Background(), "codex", t.TempDir(), agentadaptor.ProfileConfigPayload{
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:        "ui",
			Capability: "ui",
			Values:     map[string]any{"theme": "dark"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported capability to fail, got %v", err)
	}
}

func TestSyncCapabilityPatchMaterializesCodexSandbox(t *testing.T) {
	profileDir := t.TempDir()
	snapshot, err := SyncNativePatches(context.Background(), "codex", profileDir, agentadaptor.ProfileConfigPayload{
		Fingerprint: "config-fp",
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:        "sandbox",
			Capability: "sandbox",
			Values:     map[string]any{"mode": "workspace-write", "network": false},
		}},
	})
	if err != nil {
		t.Fatalf("sync capability patch: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportPortableExtended || snapshot.Materialization != engine.ProfileResourceMaterializationNativeManaged {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if !sameStrings(snapshot.Managed, []string{"sandbox"}) {
		t.Fatalf("unexpected managed keys: %#v", snapshot.Managed)
	}
	if len(snapshot.Warnings) == 0 || !strings.Contains(strings.Join(snapshot.Warnings, "\n"), "network") {
		t.Fatalf("expected unsupported field warning, got %#v", snapshot.Warnings)
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "sandbox_mode = 'workspace-write'") {
		t.Fatalf("expected sandbox_mode in config.toml, got:\n%s", string(raw))
	}
}

func TestSyncCapabilityPatchMaterializesClaudeEnv(t *testing.T) {
	profileDir := t.TempDir()
	snapshot, err := SyncNativePatches(context.Background(), "claude", profileDir, agentadaptor.ProfileConfigPayload{
		Fingerprint: "config-fp",
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:        "env",
			Capability: "env",
			Values:     map[string]any{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"},
		}},
	})
	if err != nil {
		t.Fatalf("sync capability patch: %v", err)
	}
	if snapshot.Support != engine.ProfileResourceSupportPortableExtended {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if !strings.Contains(string(raw), `"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"`) {
		t.Fatalf("expected env in settings.json, got:\n%s", string(raw))
	}
}

func TestSyncCapabilityPatchMaterializesCursorSandbox(t *testing.T) {
	profileDir := t.TempDir()
	_, err := SyncNativePatches(context.Background(), "cursor", profileDir, agentadaptor.ProfileConfigPayload{
		Fingerprint: "config-fp",
		Patches: []agentadaptor.ProfileConfigPatch{{
			Key:        "sandbox",
			Capability: "sandbox",
			Values:     map[string]any{"mode": "enabled", "networkAccess": "user_config_only"},
		}},
	})
	if err != nil {
		t.Fatalf("sync capability patch: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, "cli-config.json"))
	if err != nil {
		t.Fatalf("read cli config: %v", err)
	}
	if !strings.Contains(string(raw), `"networkAccess": "user_config_only"`) {
		t.Fatalf("expected sandbox in cli-config.json, got:\n%s", string(raw))
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
