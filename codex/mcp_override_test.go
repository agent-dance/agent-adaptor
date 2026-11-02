package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/profileinstructions"
	toml "github.com/pelletier/go-toml/v2"
)

func decodeCodexMCPOverride(t *testing.T, args []string) map[string]map[string]any {
	t.Helper()
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("expected one MCP table override, got %d arguments", len(args))
	}
	key, _, ok := strings.Cut(args[1], "=")
	if !ok || strings.TrimSpace(key) != "mcp_servers" || strings.ContainsAny(args[1], "\r\n") {
		t.Fatal("MCP override must use one root key with an inline TOML value")
	}
	var root struct {
		Servers map[string]map[string]any `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(args[1]), &root); err != nil {
		t.Fatalf("decode MCP table override: %v", err)
	}
	return root.Servers
}

func processHasMCPApproval(key, approval string) bool {
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] != "-c" || !strings.HasPrefix(os.Args[i+1], "mcp_servers =") {
			continue
		}
		var root struct {
			Servers map[string]map[string]any `toml:"mcp_servers"`
		}
		if toml.Unmarshal([]byte(os.Args[i+1]), &root) == nil {
			entry := root.Servers[key]
			return entry["default_tools_approval_mode"] == approval && entry["enabled"] == true &&
				(entry["url"] != nil || entry["command"] != nil)
		}
	}
	return false
}

func TestCodexMCPOverrideLiteralKeysAndTransports(t *testing.T) {
	t.Setenv("PR30_MCP_TOKEN", "bearer-secret-must-stay-out-of-argv")
	payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{
		{Key: "team-sdk", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp", BearerTokenEnvVar: "PR30_MCP_TOKEN"},
		{Key: "host.server", Transport: driver.MCPTransportStdio, Command: "server", Args: []string{"--stdio", `C:\space dir\config`}, Env: map[string]string{"SECRET": "stdio-secret-must-stay-out-of-argv"}},
		{Key: "quoted\"slash\\key", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:12346/mcp"},
	}}
	args, err := codexMCPPolicyArgs(payload, driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}})
	if err != nil {
		t.Fatal(err)
	}
	servers := decodeCodexMCPOverride(t, args)
	if len(servers) != len(payload.Servers) {
		t.Fatalf("server count = %d, want %d", len(servers), len(payload.Servers))
	}
	for _, spec := range payload.Servers {
		entry, ok := servers[spec.Key]
		if !ok || entry["default_tools_approval_mode"] != "approve" || entry["enabled"] != true {
			t.Fatalf("server %q lost its literal key or resolved policy", spec.Key)
		}
		if spec.Transport == driver.MCPTransportHTTP && entry["url"] != spec.URL {
			t.Fatalf("server %q lost its HTTP transport", spec.Key)
		}
	}
	if servers["team-sdk"]["bearer_token_env_var"] != "PR30_MCP_TOKEN" || servers["host.server"]["command"] != "server" ||
		!reflect.DeepEqual(servers["host.server"]["args"], []any{"--stdio", `C:\space dir\config`}) {
		t.Fatal("MCP transport configuration changed during encoding")
	}
	if servers["host.server"]["env"] != nil || strings.Contains(args[1], "secret-must-stay-out-of-argv") {
		t.Fatal("literal credentials were copied into process arguments")
	}
	payload.Servers[0], payload.Servers[2] = payload.Servers[2], payload.Servers[0]
	again, err := codexMCPPolicyArgs(payload, driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}})
	if err != nil || !reflect.DeepEqual(args, again) {
		t.Fatal("MCP table encoding depends on server admission order")
	}
}

func TestCodexMCPOverridePrecedesExplicitToolPolicy(t *testing.T) {
	explicit := `mcp_servers.sdk.tools.exact_write.approval_mode="prompt"`
	payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "sdk", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp"}}}
	opts, err := buildAppServerOptions(driver.Request{MCP: payload}, Config{CommonConfig: CommonConfig{ExtraArgs: []string{"-c", explicit}}}, "codex", nil, profileinstructions.Prepared{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.ExtraArgs) != 4 || opts.ExtraArgs[2] != "-c" || opts.ExtraArgs[3] != explicit {
		t.Fatal("root MCP table must precede explicit per-tool overrides")
	}
	decodeCodexMCPOverride(t, opts.ExtraArgs[:2])
}

// This opt-in parser probe needs no authentication and starts neither an MCP
// server nor an inference. An explicit path avoids depending on CI's CLI install.
func TestCodexMCPOverrideCLIParser(t *testing.T) {
	cli := os.Getenv("AGENT_ADAPTOR_CODEX_CONFIG_CLI")
	if cli == "" {
		t.Skip("set AGENT_ADAPTOR_CODEX_CONFIG_CLI for the real CLI config parser probe")
	}
	home := t.TempDir()
	config := []byte("[mcp_servers.native]\ncommand='native-kept'\n[mcp_servers.\"host.server\"]\ncommand='old-command'\nenabled_tools=['exact_write']\n[mcp_servers.\"host.server\".env]\nPROFILE_ONLY='profile-only-test-value'\n[mcp_servers.\"host.server\".tools.exact_write]\napproval_mode='prompt'\n")
	configPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{
		{Key: "team-sdk", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:12345/mcp", BearerTokenEnvVar: "PR30_MCP_TOKEN"},
		{Key: "host.server", Transport: driver.MCPTransportStdio, Command: "new-command"},
	}}
	args, err := codexMCPPolicyArgs(payload, driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, append(args, "mcp", "get", "host.server", "--json")...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("CLI rejected MCP table override: %v: %s", err, stderr.String())
	}
	var server struct {
		Name         string   `json:"name"`
		EnabledTools []string `json:"enabled_tools"`
		Transport    struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(output, &server); err != nil || server.Name != "host.server" || server.Transport.Command != "new-command" || server.Transport.Env["PROFILE_ONLY"] != "profile-only-test-value" || !reflect.DeepEqual(server.EnabledTools, []string{"exact_write"}) {
		t.Fatalf("CLI did not merge the literal-key override with native tool settings: %v", err)
	}
	cmd = exec.CommandContext(ctx, cli, append(args, "mcp", "list", "--json")...)
	cmd.Stderr = &stderr
	output, err = cmd.Output()
	if err != nil {
		t.Fatalf("CLI rejected merged MCP server list: %v: %s", err, stderr.String())
	}
	var listed []struct {
		Name      string `json:"name"`
		Transport struct {
			Command string `json:"command"`
			URL     string `json:"url"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(output, &listed); err != nil || len(listed) != 3 {
		t.Fatalf("CLI did not retain all native and SDK servers: %v", err)
	}
	native, sdk := false, false
	for _, item := range listed {
		native = native || item.Name == "native" && item.Transport.Command == "native-kept"
		sdk = sdk || item.Name == "team-sdk" && item.Transport.URL == payload.Servers[0].URL
	}
	if !native || !sdk {
		t.Fatal("CLI lost the unrelated native server or the SDK HTTP transport")
	}
	unchanged, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(unchanged, config) {
		t.Fatal("CLI parser probe modified the native profile")
	}
}
