package mcp_test

import (
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
)

// Compile-time proof that mcp.Server is the driver spec itself: constructor
// output flows into driver SPI shapes without conversion.
var (
	_ driver.MCPServerSpec = mcp.HTTP("docs", "https://example.com/mcp")
	_ mcp.Server           = driver.MCPServerSpec{}
	_ driver.MCPPayload    = driver.MCPPayload{Servers: []driver.MCPServerSpec{mcp.Stdio("repo-tools", "npx", "repo-mcp")}}
	_ driver.MCPTransport  = mcp.TransportSSE
)

func TestHTTPMinimalEqualsHandwrittenSpec(t *testing.T) {
	got := mcp.HTTP("docs", "https://example.com/mcp")
	want := driver.MCPServerSpec{
		Key:       "docs",
		Transport: driver.MCPTransportHTTP,
		URL:       "https://example.com/mcp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP minimal mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestHTTPFullFieldPassthrough(t *testing.T) {
	got := mcp.HTTP("docs", "https://example.com/mcp",
		mcp.WithHeaders(map[string]string{"X-Team": "placeholder", "X-Zone": "eu"}),
		mcp.WithHeader("X-Team", "platform"), // later option wins for the same key
		mcp.WithBearerTokenEnv("DOCS_MCP_TOKEN"),
		mcp.Required("docs lookups are mandatory"),
	)
	want := driver.MCPServerSpec{
		Key:               "docs",
		Transport:         driver.MCPTransportHTTP,
		URL:               "https://example.com/mcp",
		Headers:           map[string]string{"X-Team": "platform", "X-Zone": "eu"},
		BearerTokenEnvVar: "DOCS_MCP_TOKEN",
		Required:          true,
		RequiredReason:    "docs lookups are mandatory",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP full mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestSSEFullFieldPassthrough(t *testing.T) {
	got := mcp.SSE("events", "https://example.com/sse",
		mcp.WithHeader("X-Env", "staging"),
		mcp.WithBearerTokenEnv("EVENTS_MCP_TOKEN"),
		mcp.Required("event feed is mandatory"),
	)
	want := driver.MCPServerSpec{
		Key:               "events",
		Transport:         driver.MCPTransportSSE,
		URL:               "https://example.com/sse",
		Headers:           map[string]string{"X-Env": "staging"},
		BearerTokenEnvVar: "EVENTS_MCP_TOKEN",
		Required:          true,
		RequiredReason:    "event feed is mandatory",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SSE full mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestStdioMinimalEqualsHandwrittenSpec(t *testing.T) {
	got := mcp.Stdio("repo-tools", "npx", "repo-mcp")
	want := driver.MCPServerSpec{
		Key:       "repo-tools",
		Transport: driver.MCPTransportStdio,
		Command:   "npx",
		Args:      []string{"repo-mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stdio minimal mismatch:\n got %#v\nwant %#v", got, want)
	}

	if got := mcp.Stdio("bare", "server-bin"); got.Args != nil {
		t.Fatalf("Stdio without args should leave Args nil, got %#v", got.Args)
	}
}

func TestStdioFullFieldPassthroughViaExportedFields(t *testing.T) {
	got := mcp.Stdio("repo-tools", "npx", "repo-mcp", "--verbose")
	got.Env = map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"}
	got.Required = true
	got.RequiredReason = "repo access is mandatory"

	want := driver.MCPServerSpec{
		Key:            "repo-tools",
		Transport:      driver.MCPTransportStdio,
		Command:        "npx",
		Args:           []string{"repo-mcp", "--verbose"},
		Env:            map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"},
		Required:       true,
		RequiredReason: "repo access is mandatory",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stdio full mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestTransportConstantsMatchDriver(t *testing.T) {
	if mcp.TransportStdio != driver.MCPTransportStdio {
		t.Fatalf("TransportStdio = %q, want %q", mcp.TransportStdio, driver.MCPTransportStdio)
	}
	if mcp.TransportHTTP != driver.MCPTransportHTTP {
		t.Fatalf("TransportHTTP = %q, want %q", mcp.TransportHTTP, driver.MCPTransportHTTP)
	}
	if mcp.TransportSSE != driver.MCPTransportSSE {
		t.Fatalf("TransportSSE = %q, want %q", mcp.TransportSSE, driver.MCPTransportSSE)
	}
}

func TestConstructorsCopyCallerInputs(t *testing.T) {
	args := []string{"repo-mcp", "--verbose"}
	stdio := mcp.Stdio("repo-tools", "npx", args...)
	args[0] = "mutated"
	if stdio.Args[0] != "repo-mcp" {
		t.Fatalf("Stdio aliased the caller args slice: %#v", stdio.Args)
	}

	headers := map[string]string{"X-Team": "platform"}
	remote := mcp.HTTP("docs", "https://example.com/mcp", mcp.WithHeaders(headers))
	headers["X-Team"] = "mutated"
	if remote.Headers["X-Team"] != "platform" {
		t.Fatalf("WithHeaders aliased the caller map: %#v", remote.Headers)
	}
}

func TestNilAndEmptyOptionsAreSafe(t *testing.T) {
	got := mcp.HTTP("docs", "https://example.com/mcp", nil, mcp.WithHeaders(nil))
	want := driver.MCPServerSpec{
		Key:       "docs",
		Transport: driver.MCPTransportHTTP,
		URL:       "https://example.com/mcp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nil/empty options changed the server:\n got %#v\nwant %#v", got, want)
	}
}
