package engine

import (
	"errors"
	"testing"
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
