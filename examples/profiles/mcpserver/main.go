package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type incomingMessage struct {
	Body   []byte
	Framed bool
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	for {
		incoming, err := readMessage(reader)
		if err != nil {
			if err == io.EOF {
				return
			}
			fmt.Fprintf(os.Stderr, "mcp read error: %v\n", err)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(incoming.Body, &msg); err != nil {
			fmt.Fprintf(os.Stderr, "mcp decode error: %v\n", err)
			continue
		}
		if len(msg.ID) == 0 {
			logMethod(msg.Method, "")
			continue
		}
		logMethod(msg.Method, toolName(msg.Params))
		response := handle(msg)
		if err := writeMessage(os.Stdout, response, incoming.Framed); err != nil {
			fmt.Fprintf(os.Stderr, "mcp write error: %v\n", err)
			return
		}
	}
}

func logMethod(method, detail string) {
	path := strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_PROFILE_DEMO_MCP_LOG"))
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mcp log dir error: %v\n", err)
		return
	}
	line := fmt.Sprintf("%s %s", time.Now().UTC().Format(time.RFC3339Nano), method)
	if detail != "" {
		line += " " + detail
	}
	if method == "tools/call" && detail == "profile_effect_probe" {
		line += " MCP_PROFILE_DEMO_OK"
	}
	line += "\n"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp log open error: %v\n", err)
		return
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "mcp log write error: %v\n", err)
	}
}

func toolName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var params struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return ""
	}
	return strings.TrimSpace(params.Name)
}

func readMessage(reader *bufio.Reader) (incomingMessage, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return incomingMessage{}, err
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return incomingMessage{}, fmt.Errorf("malformed Content-Length header")
		}
		lengthText := strings.TrimSpace(parts[1])
		length, err := strconv.Atoi(lengthText)
		if err != nil {
			return incomingMessage{}, err
		}
		for {
			header, err := reader.ReadString('\n')
			if err != nil {
				return incomingMessage{}, err
			}
			if strings.TrimSpace(header) == "" {
				break
			}
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return incomingMessage{}, err
		}
		return incomingMessage{Body: body, Framed: true}, nil
	}
	if trimmed == "" {
		return readMessage(reader)
	}
	return incomingMessage{Body: []byte(trimmed), Framed: false}, nil
}

func writeMessage(out io.Writer, payload any, framed bool) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if framed {
		_, err = fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
		return err
	}
	_, err = out.Write(append(raw, '\n'))
	return err
}

func handle(msg rpcMessage) map[string]any {
	switch msg.Method {
	case "initialize":
		return result(msg.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "agent-adaptor-profile-demo",
				"version": "0.1.0",
			},
		})
	case "ping":
		return result(msg.ID, map[string]any{})
	case "tools/list":
		return result(msg.ID, map[string]any{
			"tools": []map[string]any{{
				"name":        "profile_effect_probe",
				"description": "Returns proof that the SDK-provided MCP server was loaded from the active agent profile.",
				"inputSchema": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			}},
		})
	case "tools/call":
		var params struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(msg.Params, &params)
		if params.Name != "profile_effect_probe" {
			return rpcError(msg.ID, -32602, "unknown demo tool")
		}
		return result(msg.ID, map[string]any{
			"content": []map[string]any{{
				"type": "text",
				"text": "MCP_PROFILE_DEMO_OK: loaded from SDK-managed profile MCP configuration.",
			}},
			"isError": false,
		})
	default:
		if bytes.HasPrefix([]byte(msg.Method), []byte("notifications/")) {
			return nil
		}
		return rpcError(msg.ID, -32601, "method not found")
	}
}

func result(id json.RawMessage, value any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  value,
	}
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
}
