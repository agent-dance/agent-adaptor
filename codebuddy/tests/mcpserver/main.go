// Command mcpserver is a controlled stdio MCP fixture used only by the
// CodeBuddy driver verification suite. It exposes one tool, echo_marker,
// whose deterministic response proves that CodeBuddy loaded and called the
// host-provided MCP server.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if request.ID == nil {
			continue
		}
		switch request.Method {
		case "initialize":
			respond(request.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "codebuddy-driver-test-mcp", "version": "1.0.0"},
			})
		case "tools/list":
			respond(request.ID, map[string]any{
				"tools": []map[string]any{{
					"name":        "echo_marker",
					"description": "Return the exact CodeBuddy driver verification marker.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{},
					},
				}},
			})
		case "tools/call":
			respond(request.ID, map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": "CODEBUDDY_DRIVER_MCP_MARKER",
				}},
			})
		default:
			respond(request.ID, map[string]any{})
		}
	}
}

func respond(id, result any) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(raw))
}
