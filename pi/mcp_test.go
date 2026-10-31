package pi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPToolBridge(t *testing.T) {
	for _, transport := range []driver.MCPTransport{driver.MCPTransportHTTP, driver.MCPTransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			var calls atomic.Int32
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "pi-test", Version: "1"}, nil)
			server.AddTool(&mcpsdk.Tool{Name: "echo", Description: "Echo input", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				calls.Add(1)
				return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(req.Params.Arguments)}}, StructuredContent: map[string]any{"observed": true}}, nil
			})
			var handler http.Handler = mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
			if transport == driver.MCPTransportSSE {
				handler = mcpsdk.NewSSEHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					http.Error(w, "unauthorized", 401)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer upstream.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req := driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "server", URL: upstream.URL, Transport: transport, BearerTokenEnvVar: "MCP_TEST_TOKEN"}}}}
			allow := []string{"read"}
			extension, bindings, closeBridge, err := prepareExtension(ctx, req, []driver.EnvBinding{{Name: "MCP_TEST_TOKEN", Value: "test-secret"}}, t.TempDir(), &allow, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer closeBridge()
			if len(allow) != 2 || !strings.HasPrefix(allow[1], "mcp_") {
				t.Fatalf("allowlist=%v", allow)
			}
			var endpoint, token string
			for _, binding := range bindings {
				if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_URL_") {
					endpoint = binding.Value
				}
				if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_TOKEN_") {
					token = binding.Value
				}
			}
			source, err := os.ReadFile(extension)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(source), token) || strings.Contains(string(source), "test-secret") {
				t.Fatal("credential written into extension")
			}
			for _, auth := range []bool{false, true} {
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/tool/0", strings.NewReader(`{"text":"hello"}`))
				if auth {
					request.Header.Set("Authorization", "Bearer "+token)
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if !auth {
					if response.StatusCode != 401 {
						t.Fatalf("unauthorized status=%d", response.StatusCode)
					}
					continue
				}
				var result mcpsdk.CallToolResult
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("decode %q: %v", body, err)
				}
				if result.StructuredContent == nil || len(result.Content) != 1 {
					t.Fatalf("result=%+v", result)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			closeBridge()
			if _, err := os.Stat(extension); !os.IsNotExist(err) {
				t.Fatal("extension not cleaned")
			}
			if _, err := http.Get(endpoint); err == nil {
				t.Fatal("bridge listener leaked")
			}
		})
	}
}

func TestMCPFailureIsPrelaunch(t *testing.T) {
	d := Driver(Config{Command: "must-not-launch", NoTools: true})
	_, err := d.Run(context.Background(), driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "missing", Transport: driver.MCPTransportHTTP, URL: "http://127.0.0.1:1"}}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "NoTools") {
		t.Fatalf("err=%v", err)
	}
}

func TestMCPEmptyCatalog(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "empty", Version: "1"}, &mcpsdk.ServerOptions{Capabilities: &mcpsdk.ServerCapabilities{Tools: &mcpsdk.ToolCapabilities{}}})
	upstream := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var allow []string
	extension, _, closeBridge, err := prepareExtension(ctx, driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "empty", URL: upstream.URL, Transport: driver.MCPTransportHTTP}}}}, nil, t.TempDir(), &allow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBridge()
	source, err := os.ReadFile(extension)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(source), "const definitions = [];") || len(allow) != 0 {
		t.Fatal("empty MCP catalog must load without registering tools")
	}
}

func TestMCPStdioProcess(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_MCP_STDIO_HELPER") != "1" {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "stdio-fixture", Version: "1"}, nil)
	server.AddTool(&mcpsdk.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "STDIO_OK"}}}, nil
	})
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPStdioLifecycle(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	command := testutil.WriteCommand(t, t.TempDir(), "mcp", "#!/bin/sh\nexec "+quote(executable)+" -test.run=^TestMCPStdioProcess$\n", "@echo off\r\n\""+executable+"\" -test.run=TestMCPStdioProcess\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req := driver.Request{MCP: driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "stdio", Transport: driver.MCPTransportStdio, Command: command, Env: map[string]string{"AGENT_ADAPTOR_MCP_STDIO_HELPER": "1"}}}}}
	var allow []string
	extension, bindings, closeBridge, err := prepareExtension(ctx, req, nil, t.TempDir(), &allow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBridge()
	var endpoint, token string
	for _, binding := range bindings {
		if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_URL_") {
			endpoint = binding.Value
		}
		if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_PI_MCP_TOKEN_") {
			token = binding.Value
		}
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/tool/0", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "STDIO_OK") {
		t.Fatalf("response=%s", body)
	}
	started := time.Now()
	closeBridge()
	if time.Since(started) > 5*time.Second {
		t.Fatal("MCP subprocess cleanup exceeded bound")
	}
	if _, err := os.Stat(filepath.Dir(extension)); !os.IsNotExist(err) {
		t.Fatal("extension directory leaked")
	}
}
