package adaptor_test

// P3.7 migration of the root MCP baseline (mcp_sdk_test.go) onto the v1
// surface. Mapping (roots stay untouched):
//
//	default → override → clear      → TestMCPDefaultOverrideClear
//	                                  (+ the replace/clear rows in merge_semantics_test.go)
//	changed skills+MCP session split → TestMCPChangeStartsFreshThreadSession
//	transport rejection pre-launch   → TestMCPTransportUnsupportedFailsPreLaunch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/memory"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/skill"
)

func mcpServerKeys(payload driver.MCPPayload) []string {
	keys := make([]string, 0, len(payload.Servers))
	for _, s := range payload.Servers {
		keys = append(keys, s.Key)
	}
	return keys
}

// TestMCPDefaultOverrideClear: the construction-time default set reaches the
// driver, a per-run WithMCP replaces it wholesale, and the zero-arg form is
// an explicit clear — with the default restored on the next bare run.
func TestMCPDefaultOverrideClear(t *testing.T) {
	ctx := context.Background()
	fake := capsFake()
	agent := adaptor.New(fake, adaptor.WithMCP(mcp.Stdio("default-stdio", "npx", mcp.Args("default-server"))))

	if _, err := agent.Run(ctx, "run 1"); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if keys := mcpServerKeys(fake.request(t, 0).MCP); !equalUnordered(keys, []string{"default-stdio"}) {
		t.Errorf("run 1 servers = %v, want the construction default", keys)
	}

	if _, err := agent.Run(ctx, "run 2", adaptor.WithMCP(mcp.HTTP("remote-http", "https://example.com/mcp"))); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if keys := mcpServerKeys(fake.request(t, 1).MCP); !equalUnordered(keys, []string{"remote-http"}) {
		t.Errorf("run 2 servers = %v, want the per-run replacement only", keys)
	}

	if _, err := agent.Run(ctx, "run 3", adaptor.WithMCP()); err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if keys := mcpServerKeys(fake.request(t, 2).MCP); len(keys) != 0 {
		t.Errorf("run 3 servers = %v, want none after the zero-arg clear", keys)
	}

	if _, err := agent.Run(ctx, "run 4"); err != nil {
		t.Fatalf("run 4: %v", err)
	}
	if keys := mcpServerKeys(fake.request(t, 3).MCP); !equalUnordered(keys, []string{"default-stdio"}) {
		t.Errorf("run 4 servers = %v, want the default restored (per-run clear does not persist)", keys)
	}
}

// TestMCPChangeStartsFreshThreadSession: on a continue_or_start thread, a
// run whose skills+MCP payload diverges from the recorded fingerprint must
// NOT resume the provider session — the SDK starts a fresh one (and rebinds
// the thread key), because the provider session was created under the old
// tool/skill surface.
func TestMCPChangeStartsFreshThreadSession(t *testing.T) {
	t.Setenv(skill.SkillCacheRootEnv, t.TempDir())
	ctx := context.Background()

	sf := newSessionFake("mcp")
	sf.descriptor = &driver.Descriptor{
		Type:        "fake",
		DisplayName: "Fake Driver",
		MCP:         driver.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	store := memory.NewStore()
	agent := adaptor.New(sf.fakeDriver,
		adaptor.WithThreadStore(store),
		adaptor.WithSkills(skill.Inline("team/default", "# default\n")),
		adaptor.WithMCP(mcp.Stdio("default-stdio", "npx", mcp.Args("default-server"))),
	)

	const key = "tenant-1/issue-1"
	res1, err := agent.Thread(key).Run(ctx, "first")
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if !strings.HasPrefix(res1.Text, "mcp:created:") {
		t.Fatalf("run 1 output = %q, want a freshly created session", res1.Text)
	}
	rec1 := activeRecord(t, store, key)

	// Same thread key, different skill + MCP surface → fresh session.
	res2, err := agent.Thread(key).Run(ctx, "second",
		adaptor.WithSkills(skill.Inline("team/override", "# override\n")),
		adaptor.WithMCP(mcp.HTTP("remote-http", "https://example.com/mcp")),
	)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if !strings.HasPrefix(res2.Text, "mcp:created:") {
		t.Errorf("run 2 output = %q, want a NEW session, not a resume of the old one", res2.Text)
	}
	req2 := sf.request(t, 1)
	if req2.Session == nil {
		t.Fatal("run 2 carried no session request")
	}
	if req2.Session.State != nil {
		t.Errorf("run 2 resumed state %+v, want a fresh start after the payload changed", req2.Session.State)
	}
	rec2 := activeRecord(t, store, key)
	if rec2.ID == rec1.ID {
		t.Errorf("thread record %s reused across incompatible payloads, want a rebound record", rec2.ID)
	}

	// And an unchanged third run resumes the second session normally.
	res3, err := agent.Thread(key).Run(ctx, "third",
		adaptor.WithSkills(skill.Inline("team/override", "# override\n")),
		adaptor.WithMCP(mcp.HTTP("remote-http", "https://example.com/mcp")),
	)
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if !strings.HasPrefix(res3.Text, "mcp:reused:") {
		t.Errorf("run 3 output = %q, want a resume of the rebound session", res3.Text)
	}
}

// TestMCPTransportUnsupportedFailsPreLaunch: a server whose transport the
// driver does not advertise fails validation before the driver is invoked.
func TestMCPTransportUnsupportedFailsPreLaunch(t *testing.T) {
	fake := newFakeDriver()
	fake.descriptor = &driver.Descriptor{
		Type: "fake",
		MCP:  driver.MCPCapability{Supported: true, Stdio: true}, // no HTTP
	}
	agent := adaptor.New(fake, adaptor.WithMCP(mcp.HTTP("remote-http", "https://example.com/mcp")))

	_, err := agent.Run(context.Background(), "go")
	if !errors.Is(err, adaptor.ErrMCPTransportUnsupported) {
		t.Fatalf("err = %v, want ErrMCPTransportUnsupported", err)
	}
	if fake.runCount() != 0 {
		t.Errorf("driver ran %d time(s), want pre-launch failure", fake.runCount())
	}
}

// Transport-specific builder options deliberately remain visible on the
// declaration when used with the wrong constructor. The invocation pipeline
// then rejects them through the same structured configuration error as a
// malformed Server literal, before a driver or process can start.
func TestMCPTransportScopedOptionsFailPreLaunch(t *testing.T) {
	tests := []struct {
		name   string
		server mcp.Server
	}{
		{
			name:   "zero server",
			server: mcp.Server{},
		},
		{
			name:   "stdio missing key",
			server: mcp.Stdio("", "server"),
		},
		{
			name: "stdio with remote header",
			server: mcp.Stdio("local", "server",
				mcp.WithHeader("X-Team", "platform"),
			),
		},
		{
			name: "stdio with remote bearer token",
			server: mcp.Stdio("local", "server",
				mcp.WithBearerTokenEnv("MCP_TOKEN"),
			),
		},
		{
			name: "http with process args",
			server: mcp.HTTP("remote", "https://example.com/mcp",
				mcp.Args("serve"),
			),
		},
		{
			name: "sse with process env",
			server: mcp.SSE("remote", "https://example.com/sse",
				mcp.Env(map[string]string{"TOKEN": "secret"}),
			),
		},
		{
			name:   "stdio missing command",
			server: mcp.Stdio("local", ""),
		},
		{
			name:   "http missing url",
			server: mcp.HTTP("remote", ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := capsFake()
			agent := adaptor.New(fake, adaptor.WithMCP(tt.server))

			_, err := agent.Run(context.Background(), "go")
			if !errors.Is(err, adaptor.ErrInvalidMCPConfig) {
				t.Fatalf("err = %v, want ErrInvalidMCPConfig", err)
			}
			if fake.runCount() != 0 {
				t.Fatalf("driver ran %d time(s), want pre-launch failure", fake.runCount())
			}
		})
	}
}
