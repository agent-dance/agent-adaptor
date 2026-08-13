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
	_ driver.MCPPayload    = driver.MCPPayload{Servers: []driver.MCPServerSpec{mcp.Stdio("repo-tools", "npx", mcp.Args("repo-mcp"))}}
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
	got := mcp.Stdio("repo-tools", "npx")
	want := driver.MCPServerSpec{
		Key:       "repo-tools",
		Transport: driver.MCPTransportStdio,
		Command:   "npx",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Stdio minimal mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestStdioFullFieldPassthroughViaOptions(t *testing.T) {
	got := mcp.Stdio("repo-tools", "npx",
		mcp.Args("repo-mcp", "--verbose"),
		mcp.Env(map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"}),
		mcp.Required("repo access is mandatory"),
	)

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
	env := map[string]string{"REPO_TOKEN_FILE": "/run/secrets/repo"}
	headers := map[string]string{"X-Team": "platform"}
	argsOpt := mcp.Args(args...)
	envOpt := mcp.Env(env)
	headerOpt := mcp.WithHeaders(headers)

	// Inputs are snapshotted when an option is created, not delayed until the
	// constructor eventually applies the option.
	args[0] = "mutated"
	env["REPO_TOKEN_FILE"] = "mutated"
	headers["X-Team"] = "mutated"

	stdio1 := mcp.Stdio("repo-tools-1", "npx", argsOpt, envOpt)
	stdio2 := mcp.Stdio("repo-tools-2", "npx", argsOpt, envOpt)
	remote1 := mcp.HTTP("docs-1", "https://example.com/mcp", headerOpt)
	remote2 := mcp.HTTP("docs-2", "https://example.com/mcp", headerOpt)

	if got := stdio1.Args; !reflect.DeepEqual(got, []string{"repo-mcp", "--verbose"}) {
		t.Fatalf("Args observed caller mutation: %#v", got)
	}
	if got := stdio1.Env["REPO_TOKEN_FILE"]; got != "/run/secrets/repo" {
		t.Fatalf("Env observed caller mutation: %q", got)
	}
	if got := remote1.Headers["X-Team"]; got != "platform" {
		t.Fatalf("WithHeaders observed caller mutation: %q", got)
	}

	// Reusing an option produces independent aggregate values.
	stdio1.Args[0] = "changed-on-first"
	stdio1.Env["REPO_TOKEN_FILE"] = "changed-on-first"
	remote1.Headers["X-Team"] = "changed-on-first"
	if stdio2.Args[0] != "repo-mcp" {
		t.Fatalf("Args option reuse aliased servers: %#v", stdio2.Args)
	}
	if stdio2.Env["REPO_TOKEN_FILE"] != "/run/secrets/repo" {
		t.Fatalf("Env option reuse aliased servers: %#v", stdio2.Env)
	}
	if remote2.Headers["X-Team"] != "platform" {
		t.Fatalf("WithHeaders option reuse aliased servers: %#v", remote2.Headers)
	}
}

func TestWithToolApprovalSetsExactPolicy(t *testing.T) {
	server := mcp.HTTP("ui", "http://127.0.0.1/mcp",
		mcp.WithToolApproval("ui_open", mcp.ToolApprovalApprove),
		mcp.WithToolApproval("ui_close", mcp.ToolApprovalPrompt),
	)
	want := map[string]driver.MCPToolPolicy{
		"ui_open":  {ApprovalMode: driver.MCPToolApprovalApprove},
		"ui_close": {ApprovalMode: driver.MCPToolApprovalPrompt},
	}
	if !reflect.DeepEqual(server.Tools, want) {
		t.Fatalf("Tools = %#v, want %#v", server.Tools, want)
	}
}

func TestWithToolApprovalConflictingDuplicateFailsClosed(t *testing.T) {
	server := mcp.HTTP("ui", "http://127.0.0.1/mcp",
		mcp.WithToolApproval("ui_open", mcp.ToolApprovalApprove),
		mcp.WithToolApproval("ui_open", mcp.ToolApprovalPrompt),
	)
	if got := server.Tools["ui_open"].ApprovalMode; got == mcp.ToolApprovalApprove || got == mcp.ToolApprovalPrompt {
		t.Fatalf("conflicting duplicate silently selected %q", got)
	}
}

func TestOptionOrdering(t *testing.T) {
	got := mcp.Stdio("repo-tools", "npx",
		mcp.Args("first"),
		mcp.Env(map[string]string{"A": "one", "B": "old"}),
		mcp.Args("second", "third"),
		mcp.Env(map[string]string{"B": "new", "C": "three"}),
	)
	want := driver.MCPServerSpec{
		Key:       "repo-tools",
		Transport: driver.MCPTransportStdio,
		Command:   "npx",
		Args:      []string{"second", "third"},
		Env:       map[string]string{"A": "one", "B": "new", "C": "three"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("option ordering mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestNilAndEmptyOptionsAreSafe(t *testing.T) {
	var zero mcp.Option
	stdio := mcp.Stdio("repo-tools", "npx", nil, zero, mcp.Args(), mcp.Env(nil))
	wantStdio := driver.MCPServerSpec{
		Key:       "repo-tools",
		Transport: driver.MCPTransportStdio,
		Command:   "npx",
	}
	if !reflect.DeepEqual(stdio, wantStdio) {
		t.Fatalf("nil/empty stdio options changed the server:\n got %#v\nwant %#v", stdio, wantStdio)
	}

	remote := mcp.HTTP("docs", "https://example.com/mcp", nil, mcp.WithHeaders(nil))
	wantRemote := driver.MCPServerSpec{
		Key:       "docs",
		Transport: driver.MCPTransportHTTP,
		URL:       "https://example.com/mcp",
	}
	if !reflect.DeepEqual(remote, wantRemote) {
		t.Fatalf("nil/empty remote options changed the server:\n got %#v\nwant %#v", remote, wantRemote)
	}
}

func TestTransportScopedOptionsAreNotSilentlyIgnored(t *testing.T) {
	remote := mcp.HTTP("docs", "https://example.com/mcp",
		mcp.Args("serve"),
		mcp.Env(map[string]string{"TOKEN": "secret"}),
	)
	if !reflect.DeepEqual(remote.Args, []string{"serve"}) || remote.Env["TOKEN"] != "secret" {
		t.Fatalf("remote constructor silently ignored stdio-only options: %#v", remote)
	}

	stdio := mcp.Stdio("repo-tools", "npx",
		mcp.WithHeader("X-Team", "platform"),
		mcp.WithBearerTokenEnv("TOKEN"),
	)
	if stdio.Headers["X-Team"] != "platform" || stdio.BearerTokenEnvVar != "TOKEN" {
		t.Fatalf("stdio constructor silently ignored remote-only options: %#v", stdio)
	}
}
