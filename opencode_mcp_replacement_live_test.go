package adaptor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/profile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveOpenCodeManagedMCPGuard(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit model is required")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "must not connect", 400)
	}))
	defer server.Close()
	managed := t.TempDir()
	raw, err := json.Marshal(map[string]any{"mcp": map[string]any{"guarded": map[string]any{"type": "remote", "url": server.URL, "oauth": false, "enabled": true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managed, "opencode.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d := opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true, Env: []driver.EnvBinding{{Name: "OPENCODE_TEST_MANAGED_CONFIG_DIR", Value: managed}}})
	a := adaptor.New(d, adaptor.WithWorkspace(t.TempDir()), adaptor.WithMCP(mcp.Server{Key: "guarded", Transport: mcp.TransportHTTP, URL: server.URL, Required: true}))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = a.Run(ctx, "This prompt must never reach the model.")
	if err == nil || !strings.Contains(err.Error(), "did not disable MCP") {
		t.Fatalf("managed MCP override was not rejected: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("managed MCP connected before the startup guard")
	}
}

func TestLiveOpenCodeMCPReplacement(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit model is required")
	}
	makeDriver := func() driver.Driver {
		return opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true})
	}
	t.Setenv("AGENT_ADAPTOR_MCP_LITERAL_TEST", "must-not-expand")
	for _, transport := range []mcp.Transport{mcp.TransportHTTP, mcp.TransportStdio} {
		t.Run(string(transport), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			var staleRequests, calls, badHeaders atomic.Int32
			old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				staleRequests.Add(1)
				http.Error(w, "old endpoint must not connect", 400)
			}))
			defer old.Close()
			d := makeDriver()
			source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "profile")
			a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())), adaptor.WithMCP(mcp.Server{Key: "durable", Transport: mcp.TransportHTTP, URL: old.URL, Headers: map[string]string{"Authorization": "Bearer stale-test-value", "X-Old": "stale", "X-File": "{file:missing-literal-file}"}, Required: true}))
			if _, err := a.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(directory, "opencode.jsonc"))
			if err != nil {
				t.Fatal(err)
			}
			// Native first-load migration adds this field; seed it so byte
			// equality below detects connection changes rather than migration.
			before = bytes.Replace(before, []byte("{"), []byte(`{"$schema":"https://opencode.ai/config.json",`), 1)
			if err := os.WriteFile(filepath.Join(directory, "opencode.jsonc"), before, 0600); err != nil {
				t.Fatal(err)
			}
			spec := mcp.Server{Key: "durable", Transport: transport, Required: true}
			callFile := filepath.Join(t.TempDir(), "called")
			if transport == mcp.TransportStdio {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				spec.Command, spec.Args, spec.Env = executable, []string{"-test.run=^TestDurableMCPStdioProcess$"}, map[string]string{"AGENT_ADAPTOR_DURABLE_MCP_CALL_FILE": callFile}
			} else {
				s := durableProofServer(func() { calls.Add(1) })
				handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
				current := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "" || r.Header.Get("X-Old") != "" || r.Header.Get("X-Literal") != "{env:AGENT_ADAPTOR_MCP_LITERAL_TEST}" || r.Header.Get("X-File") != "{file:missing-literal-file}" {
						badHeaders.Add(1)
						http.Error(w, "incorrect headers", 400)
						return
					}
					handler.ServeHTTP(w, r)
				}))
				defer current.Close()
				spec.URL, spec.Headers = current.URL, map[string]string{"X-Literal": "{env:AGENT_ADAPTOR_MCP_LITERAL_TEST}", "X-File": "{file:missing-literal-file}"}
			}
			fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithWorkspace(t.TempDir()))
			defer fresh.Close(context.Background())
			opts := []adaptor.CallOption{adaptor.WithMCP(spec)}
			if transport == mcp.TransportHTTP {
				// Exercise the native agent-discovery restart with the same
				// escaped MCP values; that restart must not activate templates.
				opts = append(opts, adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{Mode: profile.InstructionModeReplace, Content: "Use the available tools to answer the user's request accurately."}}))
			}
			result, err := fresh.Run(ctx, "Call durable_proof to read the hidden durable MCP proof word. Reply exactly with that word.", opts...)
			if err != nil {
				t.Fatal(err)
			}
			assertLiveResult(t, result, "DURABLE_MCP_847")
			if staleRequests.Load() != 0 || badHeaders.Load() != 0 {
				t.Fatal("old connection or template expansion reached the MCP server")
			}
			if transport == mcp.TransportStdio {
				if _, err := os.Stat(callFile); err != nil {
					t.Fatal("replacement stdio tool did not execute")
				}
			} else if calls.Load() == 0 {
				t.Fatal("replacement HTTP tool did not execute")
			}
			after, err := os.ReadFile(filepath.Join(directory, "opencode.jsonc"))
			if err != nil || string(after) != string(before) {
				t.Fatal("invocation replacement modified durable profile")
			}
		})
	}
}
