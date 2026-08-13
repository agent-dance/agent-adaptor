package engine

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestRuntimeServiceMetadataDoesNotDeclareMCP(t *testing.T) {
	refs := []RuntimeServiceRef{{
		ID:   "sidecar",
		Name: "sidecar",
		URL:  "https://metadata.example/mcp",
		Metadata: map[string]string{
			"agentadaptor.mcp.enabled":      "true",
			"agentadaptor.mcp.key":          "removed-metadata-key",
			"agentadaptor.mcp.transport":    "http",
			"agentadaptor.mcp.url":          "https://removed-metadata.example/mcp",
			"agentadaptor.mcp.headers_json": "{not-json",
		},
	}}

	payload, err := resolveMCPPayloadWithRuntime(nil, nil, refs, MCPCapability{})
	if err != nil {
		t.Fatalf("resolveMCPPayloadWithRuntime: %v; metadata must remain opaque", err)
	}
	if len(payload.Servers) != 0 {
		t.Fatalf("Servers = %+v, want none without RuntimeServiceRef.MCP", payload.Servers)
	}
}

func TestRuntimeServiceToolApprovalNormalizesAndFingerprintsAuthority(t *testing.T) {
	server := MCPServerSpec{Key: "ui", Transport: MCPTransportHTTP, URL: "http://127.0.0.1/mcp", Tools: map[string]driver.MCPToolPolicy{
		" ui_patch ": {ApprovalMode: driver.MCPToolApprovalApprove},
		"ui_open":    {ApprovalMode: driver.MCPToolApprovalApprove},
	}}
	refs := []RuntimeServiceRef{{ID: "ui", MCP: &server}}
	caps := MCPCapability{Supported: true, HTTP: true, ToolApprovals: true}

	payload, err := resolveMCPPayloadWithRuntime(nil, nil, refs, caps)
	if err != nil {
		t.Fatalf("resolveMCPPayloadWithRuntime: %v", err)
	}
	want := map[string]driver.MCPToolPolicy{"ui_open": {ApprovalMode: driver.MCPToolApprovalApprove}, "ui_patch": {ApprovalMode: driver.MCPToolApprovalApprove}}
	if got := payload.Servers[0].Tools; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tools = %#v, want %#v", got, want)
	}
	server.Tools["shell"] = driver.MCPToolPolicy{ApprovalMode: driver.MCPToolApprovalApprove}
	if got := payload.Servers[0].Tools; !reflect.DeepEqual(got, want) {
		t.Fatalf("payload aliases caller: %#v", got)
	}

	reordered := MCPServerSpec{Key: "ui", Transport: MCPTransportHTTP, URL: "http://127.0.0.1/mcp", Tools: map[string]driver.MCPToolPolicy{
		"ui_open": {ApprovalMode: driver.MCPToolApprovalApprove}, "ui_patch": {ApprovalMode: driver.MCPToolApprovalApprove},
	}}
	equivalent, err := resolveMCPPayloadWithRuntime(nil, nil, []RuntimeServiceRef{{MCP: &reordered}}, caps)
	if err != nil {
		t.Fatalf("resolve reordered policy: %v", err)
	}
	if equivalent.Fingerprint != payload.Fingerprint {
		t.Fatalf("equivalent order changed fingerprint: %q != %q", equivalent.Fingerprint, payload.Fingerprint)
	}

	changedServer := reordered
	changedServer.Tools = map[string]driver.MCPToolPolicy{"ui_open": {ApprovalMode: driver.MCPToolApprovalApprove}}
	changed, err := resolveMCPPayloadWithRuntime(nil, nil, []RuntimeServiceRef{{MCP: &changedServer}}, caps)
	if err != nil {
		t.Fatalf("resolve changed policy: %v", err)
	}
	if changed.Fingerprint == payload.Fingerprint {
		t.Fatal("approval authority change did not change MCP fingerprint")
	}
}

func TestRuntimeServiceToolApprovalFailsClosed(t *testing.T) {
	server := MCPServerSpec{Key: "ui", Transport: MCPTransportHTTP, URL: "http://127.0.0.1/mcp"}
	tests := []struct {
		name  string
		tools map[string]driver.MCPToolPolicy
		caps  MCPCapability
	}{
		{name: "blank tool", tools: map[string]driver.MCPToolPolicy{" ": {ApprovalMode: driver.MCPToolApprovalApprove}}, caps: MCPCapability{Supported: true, HTTP: true, ToolApprovals: true}},
		{name: "invalid mode", tools: map[string]driver.MCPToolPolicy{"ui_open": {ApprovalMode: "always"}}, caps: MCPCapability{Supported: true, HTTP: true, ToolApprovals: true}},
		{name: "unsupported driver", tools: map[string]driver.MCPToolPolicy{"ui_open": {ApprovalMode: driver.MCPToolApprovalApprove}}, caps: MCPCapability{Supported: true, HTTP: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.Tools = tt.tools
			_, err := resolveMCPPayloadWithRuntime(nil, nil, []RuntimeServiceRef{{MCP: &server}}, tt.caps)
			if !errors.Is(err, ErrInvalidMCPConfig) && !errors.Is(err, ErrMCPUnsupported) {
				t.Fatalf("error = %v, want fail-closed MCP sentinel", err)
			}
		})
	}
}

func TestRuntimeServiceTypedMCPIsMaterializedAndValidated(t *testing.T) {
	t.Run("defaults from runtime ref", func(t *testing.T) {
		server := MCPServerSpec{}
		refs := []RuntimeServiceRef{{
			ID:   "sidecar-id",
			Name: "sidecar",
			URL:  "https://typed.example/mcp",
			MCP:  &server,
		}}

		payload, err := resolveMCPPayloadWithRuntime(nil, nil, refs, MCPCapability{Supported: true, HTTP: true})
		if err != nil {
			t.Fatalf("resolveMCPPayloadWithRuntime: %v", err)
		}
		if len(payload.Servers) != 1 {
			t.Fatalf("Servers = %+v, want one typed server", payload.Servers)
		}
		got := payload.Servers[0]
		if got.Key != "sidecar" || got.Transport != MCPTransportHTTP || got.URL != "https://typed.example/mcp" {
			t.Fatalf("typed server = %+v, want defaults from runtime ref", got)
		}
	})

	t.Run("invalid typed declaration remains structured", func(t *testing.T) {
		server := MCPServerSpec{Key: "sidecar", Transport: MCPTransportHTTP}
		_, err := resolveMCPPayloadWithRuntime(nil, nil, []RuntimeServiceRef{{MCP: &server}}, MCPCapability{Supported: true, HTTP: true})
		if !errors.Is(err, ErrInvalidMCPConfig) {
			t.Fatalf("error = %v, want errors.Is(ErrInvalidMCPConfig)", err)
		}
	})
}
