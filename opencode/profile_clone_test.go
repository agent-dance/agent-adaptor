package opencode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	"github.com/agent-dance/agent-adaptor/internal/profilestate"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestProfileCloneSeparatesMCPAndSettings(t *testing.T) {
	ctx := context.Background()
	source := t.TempDir()
	base := []byte(`{"model":"local/model","provider":{"private":{"options":{"apiKey":"settings-secret"}}},"mcp":{"external":{"type":"local","command":["external-command"],"enabled":false}}}`)
	if err := os.WriteFile(filepath.Join(source, "opencode.json"), base, 0600); err != nil {
		t.Fatal(err)
	}
	config := []byte("{ // settings-comment-secret\n \"default_agent\":\"custom\", \"custom_number\":9007199254740993, }")
	if err := os.WriteFile(filepath.Join(source, "opencode.jsonc"), config, 0600); err != nil {
		t.Fatal(err)
	}
	spec := driver.MCPServerSpec{Key: "owned", Transport: driver.MCPTransportSSE, URL: "https://example.test/mcp", BearerTokenEnvVar: "MCP_TOKEN", Required: true, RequiredReason: "needed for the task"}
	layout, entries, err := mcpProfilePlan(source, driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(ctx, layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(source, "opencode.jsonc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		opts driver.CloneProfileOptions
	}{
		{"mcp", driver.CloneProfileOptions{IncludeMCP: true}},
		{"settings", driver.CloneProfileOptions{IncludeSettings: true}},
		{"both", driver.CloneProfileOptions{IncludeMCP: true, IncludeSettings: true}},
		{"neither", driver.CloneProfileOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "target")
			selection := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: source, Dir: target, Clone: &tc.opts}
			if _, err := resolveProfile(ctx, Config{}, selection, false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("read-only clone inspection initialized destination")
			}
			if _, err := resolveProfile(ctx, Config{}, selection, true); err != nil {
				t.Fatal(err)
			}
			var text string
			for _, file := range []string{"opencode.json", "opencode.jsonc"} {
				raw, err := os.ReadFile(filepath.Join(target, file))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				text += string(raw)
			}
			if strings.Contains(text, "settings-secret") != tc.opts.IncludeSettings || strings.Contains(text, "external-command") != tc.opts.IncludeMCP || strings.Contains(text, "settings-comment-secret") {
				t.Fatal("clone copied excluded fields or comments")
			}
			if tc.opts.IncludeSettings && !strings.Contains(text, "9007199254740993") {
				t.Fatal("clone rounded numeric setting")
			}
			payload, _, _, err := resolveProfileMCP(driver.Request{}, target)
			if err != nil {
				t.Fatal(err)
			}
			if tc.opts.IncludeMCP {
				if len(payload.Servers) != 1 || !payload.Servers[0].Required || payload.Servers[0].RequiredReason != spec.RequiredReason || payload.Servers[0].Transport != driver.MCPTransportSSE || payload.Servers[0].BearerTokenEnvVar != "MCP_TOKEN" {
					t.Fatalf("cloned MCP lost policy: %+v", payload)
				}
				// A fresh Agent can explicitly clear the copied managed entry;
				// the external source declaration remains external in the clone.
				if _, err := profilejson.Sync(ctx, mcpProfileLayout(target), nil, ""); err != nil {
					t.Fatal(err)
				}
				external, err := profilejson.ReadEntries(target, "opencode.json", "mcp")
				if err != nil || external["external"] == nil {
					t.Fatal("clear removed cloned external declaration")
				}
			} else if len(payload.Servers) != 0 {
				t.Fatal("settings clone included MCP")
			}
		})
	}
	after, err := os.ReadFile(filepath.Join(source, "opencode.jsonc"))
	if err != nil || string(before) != string(after) {
		t.Fatal("clone changed source")
	}
}

func TestProfileClonePreservesExistingConfigGroup(t *testing.T) {
	for _, existing := range []string{"opencode.json", "opencode.jsonc"} {
		t.Run(existing, func(t *testing.T) {
			source, target := t.TempDir(), t.TempDir()
			for _, file := range []string{"opencode.json", "opencode.jsonc"} {
				if err := os.WriteFile(filepath.Join(source, file), []byte(`{"model":"source/model","mcp":{}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			original := []byte(`{"model":"destination/model"}`)
			if err := os.WriteFile(filepath.Join(target, existing), original, 0600); err != nil {
				t.Fatal(err)
			}
			sel := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: source, Dir: target, Clone: &driver.CloneProfileOptions{IncludeSettings: true, IncludeMCP: true}}
			if _, err := resolveProfile(context.Background(), Config{}, sel, true); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{"opencode.json", "opencode.jsonc"} {
				raw, err := os.ReadFile(filepath.Join(target, file))
				if file == existing && (err != nil || string(raw) != string(original)) || file != existing && !os.IsNotExist(err) {
					t.Fatal("clone changed destination config precedence")
				}
			}
		})
	}
}

func TestProfileCloneUsesNormalizedSource(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "opencode.json"), []byte(`{"model":"source/model"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", source)
	for _, from := range []string{"  " + source + "  ", "  "} {
		target := t.TempDir()
		sel := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: from, Dir: target, Clone: &driver.CloneProfileOptions{IncludeSettings: true}}
		if _, err := resolveProfile(context.Background(), Config{}, sel, true); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(target, "opencode.json"))
		if err != nil || !strings.Contains(string(raw), "source/model") {
			t.Fatal("structured clone used a different source than profile resolution")
		}
	}
}

func TestProfileCloneRejectsUnverifiableMCPBeforeCopy(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	layout, entries, err := mcpProfilePlan(source, driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "required", Transport: driver.MCPTransportHTTP, URL: "https://example.test", Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "opencode.jsonc"), []byte(`{"mcp":{"required":{"type":"remote","url":"https://edited.test"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	sel := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: source, Dir: target, Clone: &driver.CloneProfileOptions{IncludeMCP: true}}
	if _, err := resolveProfile(context.Background(), Config{}, sel, true); err == nil {
		t.Fatal("clone silently dropped unverifiable required policy")
	}
	if _, err := os.Stat(filepath.Join(target, "opencode.jsonc")); !os.IsNotExist(err) {
		t.Fatal("invalid source copied to destination")
	}
}

func TestProfileCloneLockHonorsCancellation(t *testing.T) {
	source, target := t.TempDir(), t.TempDir()
	lock, err := profilestate.AcquireLock(context.Background(), source, profilestate.LockOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	sel := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: source, Dir: target, Clone: &driver.CloneProfileOptions{IncludeMCP: true}}
	_, err = resolveProfile(ctx, Config{}, sel, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("clone cancellation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, profilestate.LockName)); !os.IsNotExist(err) {
		t.Fatal("clone cancellation leaked destination lock")
	}
}

func TestProfileCloneMCPRequiredFailurePreventsPrompt(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "mcp-status.json"), []byte(`{"required":{"status":"failed"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	layout, entries, err := mcpProfilePlan(source, driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "required", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1", Required: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilejson.Sync(context.Background(), layout, entries, ""); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	sel := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: source, Dir: target, Clone: &driver.CloneProfileOptions{IncludeMCP: true}}
	if _, err := resolveProfile(context.Background(), Config{}, sel, true); err != nil {
		t.Fatal(err)
	}
	d := Driver(Config{Command: command})
	resp, err := d.Run(context.Background(), driver.Request{Profile: sel}, &testutil.EventRecorder{})
	if err == nil || !strings.Contains(err.Error(), "not connected") || resp.Checkpoint != nil {
		t.Fatalf("cloned required failure: %v %+v", err, resp)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("prompt delivered without cloned required MCP")
	}
}
