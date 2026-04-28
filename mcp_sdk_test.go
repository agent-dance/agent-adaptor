package agentadaptor_test

import (
	"context"
	"errors"
	"testing"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
)

func TestSDKRunMergesDefaultMCPAndPerRunOverride(t *testing.T) {
	driver := &fakeDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	sdk := newSDK(nil, fakeBinding("default", driver,
		agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{
			Servers: []agentadaptor.MCPServerSpec{
				{
					Key:       "default-stdio",
					Transport: agentadaptor.MCPTransportStdio,
					Command:   "npx",
					Args:      []string{"default-server"},
				},
			},
		}),
	), nil)

	if _, err := sdk.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run with default MCP: %v", err)
	}
	if len(driver.lastMCP.Servers) != 1 || driver.lastMCP.Servers[0].Key != "default-stdio" {
		t.Fatalf("unexpected default MCP payload: %#v", driver.lastMCP)
	}

	if _, err := sdk.Run(context.Background(), "override",
		agentadaptor.WithMCP(agentadaptor.MCPConfig{
			Servers: []agentadaptor.MCPServerSpec{
				{
					Key:       "remote-http",
					Transport: agentadaptor.MCPTransportHTTP,
					URL:       "https://example.com/mcp",
				},
			},
		}),
	); err != nil {
		t.Fatalf("run with override MCP: %v", err)
	}
	if len(driver.lastMCP.Servers) != 1 || driver.lastMCP.Servers[0].Key != "remote-http" {
		t.Fatalf("expected per-run override to replace default MCP payload, got %#v", driver.lastMCP)
	}

	if _, err := sdk.Run(context.Background(), "clear", agentadaptor.WithMCP(agentadaptor.MCPConfig{})); err != nil {
		t.Fatalf("run with empty MCP override: %v", err)
	}
	if len(driver.lastMCP.Servers) != 0 {
		t.Fatalf("expected empty MCP override to clear inherited defaults, got %#v", driver.lastMCP)
	}
}

func TestSessionCompatibilitySeparatesProfileResourceChanges(t *testing.T) {
	driver := &fakeDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, Stdio: true, HTTP: true, SSE: true},
	}
	sdk := newSDK(memory.NewSessionStore(), fakeBinding("default", driver,
		agentadaptor.WithDefaultSkills(agentadaptor.InlineSkill("team/default", "# default")),
		agentadaptor.WithDefaultMCP(agentadaptor.MCPConfig{
			Servers: []agentadaptor.MCPServerSpec{
				{
					Key:       "default-stdio",
					Transport: agentadaptor.MCPTransportStdio,
					Command:   "npx",
					Args:      []string{"default-server"},
				},
			},
		}),
	), nil)

	first, err := sdk.Run(context.Background(), "hello", agentadaptor.WithSessionKey("company", "issue-1"))
	if err != nil {
		t.Fatalf("first run: %v", err)
	}

	second, err := sdk.Run(
		context.Background(),
		"again",
		agentadaptor.WithSessionKey("company", "issue-1"),
		agentadaptor.WithSkills(agentadaptor.InlineSkill("team/override", "# override")),
		agentadaptor.WithMCP(agentadaptor.MCPConfig{
			Servers: []agentadaptor.MCPServerSpec{
				{
					Key:       "remote-http",
					Transport: agentadaptor.MCPTransportHTTP,
					URL:       "https://example.com/mcp",
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	if first.Session == nil || second.Session == nil {
		t.Fatalf("expected sessions on both runs, first=%#v second=%#v", first.Session, second.Session)
	}
	if first.Session.ID == second.Session.ID {
		t.Fatalf("expected changed skills/MCP to start a fresh compatible session, got %q twice", first.Session.ID)
	}
	if got := second.Output; got != "default:created:default-driver-session-2" {
		t.Fatalf("expected second run to start a fresh provider session, got %q", got)
	}
}

func TestSDKRunRejectsUnsupportedMCPTransportBeforeDriverRun(t *testing.T) {
	driver := &fakeDriver{
		mcpCapability: agentadaptor.MCPCapability{Supported: true, Stdio: true},
	}
	sdk := newSDK(nil, fakeBinding("default", driver), nil)

	_, err := sdk.Run(context.Background(), "hello", agentadaptor.WithMCP(agentadaptor.MCPConfig{
		Servers: []agentadaptor.MCPServerSpec{
			{
				Key:       "remote-http",
				Transport: agentadaptor.MCPTransportHTTP,
				URL:       "https://example.com/mcp",
			},
		},
	}))
	if !errors.Is(err, agentadaptor.ErrMCPTransportUnsupported) {
		t.Fatalf("expected ErrMCPTransportUnsupported, got %v", err)
	}
	if driver.runCalls != 0 {
		t.Fatalf("expected unsupported MCP transport to fail before driver.Run, got %d calls", driver.runCalls)
	}
}
