package opencode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/mcp"
)

func TestMCPExactInstallationAndPersistentReuse(t *testing.T) {
	f := persistentServerFixture(t)
	spec := mcp.Server{Key: "tool", Transport: mcp.TransportHTTP, URL: "http://127.0.0.1:1", Headers: map[string]string{"X-Literal": "{env:KEEP_LITERAL}"}}
	f.run(t, adaptor.WithMCP(spec))
	f.run(t, adaptor.WithMCP(spec))
	read := func() []string {
		raw, err := os.ReadFile(filepath.Join(f.dir, "mcp-install.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(raw)), "\n")
	}
	lines := read()
	if len(lines) != 1 || len(f.pids(t)) != 1 {
		t.Fatal("unchanged persistent turn reconnected MCP")
	}
	var request struct {
		Name   string
		Config struct {
			Enabled bool
			Headers map[string]string
		}
	}
	if err := json.Unmarshal([]byte(lines[0]), &request); err != nil {
		t.Fatal(err)
	}
	if request.Name != "tool" || !request.Config.Enabled || request.Config.Headers["X-Literal"] != "{env:KEEP_LITERAL}" {
		t.Fatal("native API changed connection values")
	}
	spec.URL = "http://127.0.0.1:2"
	spec.Headers = nil
	f.run(t, adaptor.WithMCP(spec))
	if len(read()) != 2 || len(f.pids(t)) != 2 {
		t.Fatal("changed connection reused old native client")
	}
	var changed struct{ Config map[string]any }
	if err := json.Unmarshal([]byte(read()[1]), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.Config["headers"] != nil {
		t.Fatal("removed headers were sent to replacement")
	}
}

func TestMCPMalformedInstallationPreventsPrompt(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"tool":{"status":"unknown"}}`, `{"status":{"tool":{"status":"connected"}}}`, `{broken`} {
		t.Run(raw, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
			if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "mcp-install-response.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			resp, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "tool", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1"}}}}, &testutil.EventRecorder{})
			if err == nil || resp.Checkpoint != nil {
				t.Fatal("malformed install succeeded")
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("prompt delivered after malformed MCP response")
			}
			spawns, err := os.ReadFile(filepath.Join(filepath.Dir(capture), "spawns.log"))
			if err != nil || len(strings.Fields(string(spawns))) != 1 {
				t.Fatal("MCP protocol failure replayed startup")
			}
		})
	}
}

func TestOptionalMCPFailureIsObserved(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "mcp-status.json"), []byte(`{"optional":{"status":"failed","error":"private provider detail"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
	sink := &testutil.EventRecorder{}
	resp, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "optional", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1"}}}}, sink)
	if err != nil || resp.Output != "OK" {
		t.Fatalf("optional MCP failed run: %v", err)
	}
	seen := false
	for _, ev := range sink.Snapshot() {
		if ev.Type == driver.RunEventInvocation && ev.Data["mcp_key"] == "optional" && ev.Data["status"] == "failed" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("optional MCP failure disappeared")
	}
}

func TestMCPNativeConfigGuardsPrecedeInstallation(t *testing.T) {
	for _, tc := range []struct{ name, override string }{
		{"re-enabled", `{"managed":{"enabled":true}}`},
		{"implicit-enabled", `{"managed":{}}`},
		{"external-name-alias", `{"managed!":{"enabled":true},"managed?":{"enabled":false}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
			if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "managed-mcp.json"), []byte(tc.override), 0600); err != nil {
				t.Fatal(err)
			}
			key := "managed"
			if tc.name == "external-name-alias" {
				key = "managed?"
			}
			d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			resp, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: key, Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1"}}}}, &testutil.EventRecorder{})
			if err == nil || resp.Checkpoint != nil {
				t.Fatal("native override ignored")
			}
			for _, file := range []string{capture, filepath.Join(filepath.Dir(capture), "mcp-install.jsonl")} {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("native override was not rejected before installation/prompt")
				}
			}
		})
	}
}

func TestMCPNativeNameCollisionsBeforeLaunchOrSync(t *testing.T) {
	for _, keys := range [][2]string{{"a.b", "a_b"}, {"x😀", "x__"}} {
		servers := []driver.MCPServerSpec{{Key: keys[0], Transport: driver.MCPTransportHTTP, URL: "https://example.test"}, {Key: keys[1], Transport: driver.MCPTransportHTTP, URL: "https://example.test"}}
		profile := filepath.Join(t.TempDir(), "absent")
		d := Driver(Config{Command: filepath.Join(t.TempDir(), "absent"), Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: profile}}})
		_, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: servers}}, &testutil.EventRecorder{})
		if err == nil || !strings.Contains(err.Error(), "collide") {
			t.Fatalf("name collision: %v", err)
		}
		_, err = d.(driver.ProfileResourceDriver).SyncProfileResources(context.Background(), nil, driver.AgentIdentity{}, nil, driver.ProfilePayload{MCP: driver.MCPPayload{Servers: servers}, Declared: driver.ProfileResourceDeclarations{MCP: true}}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "collide") {
			t.Fatalf("sync name collision: %v", err)
		}
		if _, err := os.Stat(profile); !os.IsNotExist(err) {
			t.Fatal("invalid names initialized profile")
		}
	}
}
