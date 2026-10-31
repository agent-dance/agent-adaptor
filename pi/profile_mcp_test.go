package pi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/profilejson"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRPCPersistentMCPProfileDrift(t *testing.T) {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "native-mcp-drift", Version: "1"}, nil)
	s.AddTool(&mcpsdk.Tool{Name: "proof", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{}, nil
	})
	server := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return s }, nil))
	defer server.Close()
	f := persistentRPCFixture(t)
	f.run(t)
	spec := driver.MCPServerSpec{Key: "durable", Transport: driver.MCPTransportHTTP, URL: server.URL}
	syncMCP := func() {
		entries, err := mcpProfileEntries(driver.MCPPayload{Servers: []driver.MCPServerSpec{spec}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := profilejson.Sync(context.Background(), mcpProfileLayout(f.profile), entries, ""); err != nil {
			t.Fatal(err)
		}
	}
	syncMCP()
	f.run(t)
	if len(f.pids(t)) != 2 {
		t.Fatal("new MCP did not replace stale writer")
	}
	spec.Required = true
	syncMCP()
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("changed MCP policy reused stale writer")
	}
	f.run(t)
	if len(f.pids(t)) != 3 {
		t.Fatal("unchanged MCP restarted writer")
	}
	if err := os.Remove(filepath.Join(f.profile, mcpProfileFile)); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if len(f.pids(t)) != 4 {
		t.Fatal("removed MCP retained stale writer")
	}
}
