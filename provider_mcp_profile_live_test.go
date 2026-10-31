package adaptor_test

import (
	"context"
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
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/profile"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func durableProofServer(called func()) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "durable-profile-proof", Version: "1"}, nil)
	s.AddTool(&mcpsdk.Tool{Name: "durable_proof", Description: "Read the hidden durable MCP proof word. Always call this tool when asked for that word.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		called()
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "DURABLE_MCP_847"}}}, nil
	})
	return s
}

func TestDurableMCPStdioProcess(t *testing.T) {
	path := os.Getenv("AGENT_ADAPTOR_DURABLE_MCP_CALL_FILE")
	if path == "" {
		return
	}
	s := durableProofServer(func() { _ = os.WriteFile(path, []byte("called"), 0600) })
	if err := s.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLiveProviderDurableMCP(t *testing.T) {
	liveProviderDurableMCP(t, false)
}

func TestLiveProviderClonedMCP(t *testing.T) {
	liveProviderDurableMCP(t, true)
}

func liveProviderDurableMCP(t *testing.T, clone bool) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("live conformance gate is disabled")
	}
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			prefix := "AGENT_ADAPTOR_" + strings.ToUpper(name) + "_"
			if os.Getenv(prefix+"LIVE") != "1" {
				t.Skip("provider live gate is disabled")
			}
			model := os.Getenv(prefix + "MODEL")
			if model == "" {
				t.Fatal("an explicit model is required")
			}
			makeDriver := func() driver.Driver {
				if name == "pi" {
					return pi.Driver(pi.Config{Command: os.Getenv(prefix + "COMMAND"), Provider: os.Getenv(prefix + "PROVIDER"), Model: model, NoExtensions: true, NoSkills: true})
				}
				return opencode.Driver(opencode.Config{Command: os.Getenv(prefix + "COMMAND"), Model: model, Pure: true})
			}
			for _, transport := range []mcp.Transport{mcp.TransportHTTP, mcp.TransportSSE, mcp.TransportStdio} {
				t.Run(string(transport), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					var calls, requests atomic.Int32
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
						var handler http.Handler = mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
						if transport == mcp.TransportSSE {
							handler = mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return s }, nil)
						}
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							requests.Add(1)
							if r.Header.Get("Authorization") != "Bearer durable-test-secret-638" {
								http.Error(w, "unauthorized", 401)
								return
							}
							handler.ServeHTTP(w, r)
						}))
						defer upstream.Close()
						t.Setenv("AGENT_ADAPTOR_DURABLE_MCP_TEST_TOKEN", "durable-test-secret-638")
						spec.URL, spec.BearerTokenEnvVar = upstream.URL, "AGENT_ADAPTOR_DURABLE_MCP_TEST_TOKEN"
					}
					d := makeDriver()
					source, err := d.(driver.ProfileReporter).GetProfile(ctx, nil, driver.AgentIdentity{}, nil)
					if err != nil {
						t.Fatal(err)
					}
					directory := filepath.Join(t.TempDir(), "profile")
					a := adaptor.New(d, adaptor.WithProfile(profile.CloneFrom(source.Dir, directory, profile.CopySettings())), adaptor.WithMCP(spec))
					if _, err := a.SyncProfile(ctx); err != nil {
						t.Fatal(err)
					}
					if err := a.Close(ctx); err != nil {
						t.Fatal(err)
					}
					if clone {
						destination := filepath.Join(t.TempDir(), "cloned-profile")
						seed := adaptor.New(makeDriver(), adaptor.WithProfile(profile.CloneFrom(directory, destination, profile.CopySettings(), profile.CopyMCP())))
						if _, err := seed.SyncProfile(ctx); err != nil {
							t.Fatal(err)
						}
						if err := seed.Close(ctx); err != nil {
							t.Fatal(err)
						}
						directory = destination
					}
					for _, filename := range []string{"opencode.jsonc", "agent-adaptor-mcp.json", ".agent-adaptor-profile-manifest.json"} {
						if raw, err := os.ReadFile(filepath.Join(directory, filename)); err == nil && strings.Contains(string(raw), "durable-test-secret-638") {
							t.Fatal("bearer credential was persisted instead of its environment reference")
						}
					}
					fresh := adaptor.New(makeDriver(), adaptor.WithProfile(profile.Dedicated(directory)), adaptor.WithWorkspace(t.TempDir()))
					defer fresh.Close(context.Background())
					result, err := fresh.Run(ctx, "Call durable_proof to read the hidden durable MCP proof word. Reply with exactly the returned word.")
					if err != nil {
						t.Fatal(err)
					}
					assertLiveResult(t, result, "DURABLE_MCP_847")
					if transport == mcp.TransportStdio {
						if _, err := os.Stat(callFile); err != nil {
							t.Fatal("stdio MCP tool did not execute")
						}
					} else if calls.Load() == 0 {
						t.Fatal("remote MCP tool did not execute")
					}
					prior := requests.Load()
					cleared, err := fresh.Run(ctx, "Reply exactly CLEARED_805.", adaptor.WithMCP())
					if err != nil {
						t.Fatal(err)
					}
					assertLiveResult(t, cleared, "CLEARED_805")
					if requests.Load() != prior {
						t.Fatal("explicit per-call clear still connected to durable MCP")
					}
					t.Logf("%s %s durable MCP loaded by fresh Agent; call clear isolated; model=%s", name, transport, model)
				})
			}
		})
	}
}
