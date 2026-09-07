// Package alignmentprotocol contains independent T21 protocol fixtures only.
package alignmentprotocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// The test executable can serve as a credential-free provider child. This
// entry is enabled only in the explicitly configured child's environment.
func init() {
	if os.Getenv("AGENT_ADAPTOR_T21_PRIVATE_CHILD") != "fixture-v1" {
		return
	}
	os.Exit(child())
}
func child() int {
	var interactive *json.Decoder
	switch os.Getenv("T21_PROVIDER") {
	case "codex":
		return codex()
	case "cursor": // Cursor's print prompt is argv, with no stdin protocol.
	default:
		bidirectional := false
		for _, arg := range os.Args {
			if arg == "--input-format" || arg == "--input-format=stream-json" {
				bidirectional = true
			}
		}
		if bidirectional {
			interactive = json.NewDecoder(os.Stdin)
			for {
				var input map[string]any
				if interactive.Decode(&input) != nil {
					return 21
				}
				if input["type"] == "user" {
					break
				}
				if input["type"] == "control_request" {
					if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": input["request_id"], "response": map[string]any{}}}); err != nil {
						return 22
					}
				}
			}
		} else if _, e := io.Copy(io.Discard, os.Stdin); e != nil {
			return 22
		}
	}
	b, e := os.ReadFile(os.Getenv("T21_PROTOCOL"))
	if e != nil {
		return 23
	}
	if interactive == nil {
		fmt.Fprint(os.Stdout, string(b))
	} else {
		lines := bufio.NewScanner(bytes.NewReader(b))
		for lines.Scan() {
			line := lines.Text()
			fmt.Fprintln(os.Stdout, line)
			var payload map[string]any
			if json.Unmarshal([]byte(line), &payload) == nil && payload["type"] == "control_request" {
				var answer struct {
					Type     string `json:"type"`
					Response struct {
						RequestID string `json:"request_id"`
						Response  struct {
							Behavior  string `json:"behavior"`
							Allowed   bool   `json:"allowed"`
							ToolUseID string `json:"tool_use_id"`
						} `json:"response"`
					} `json:"response"`
				}
				if interactive.Decode(&answer) != nil || answer.Type != "control_response" || answer.Response.RequestID != payload["request_id"] {
					return 25
				}
				if os.Getenv("T21_PROVIDER") == "codebuddy" {
					if !answer.Response.Response.Allowed || answer.Response.Response.ToolUseID != "control-id" {
						return 25
					}
				} else if answer.Response.Response.Behavior != "allow" {
					return 25
				}
			}
		}
		if lines.Err() != nil {
			return 26
		}
	}
	fmt.Fprint(os.Stderr, "t21 private stderr")
	return 0
}
func codex() int {
	d := json.NewDecoder(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if d.Decode(&r) != nil {
			return 0
		}
		reply := func(v any) { _ = out.Encode(map[string]any{"id": r.ID, "result": v}) }
		notify := func(m string, p any) { _ = out.Encode(map[string]any{"method": m, "params": p}) }
		switch r.Method {
		case "initialize":
			reply(map[string]any{})
		case "thread/start":
			reply(map[string]any{"thread": map[string]any{"id": "t21-thread"}})
		case "turn/start":
			reply(map[string]any{"turn": map[string]any{"id": "t21-turn", "status": "inProgress"}})
			notify("turn/started", map[string]any{"threadId": "t21-thread", "turn": map[string]any{"id": "t21-turn", "status": "inProgress"}})
			b, e := os.ReadFile(os.Getenv("T21_PROTOCOL"))
			if e != nil {
				return 24
			}
			fmt.Fprint(os.Stdout, string(b))
			fmt.Fprint(os.Stderr, "t21 private stderr")
		case "turn/interrupt":
			reply(map[string]any{})
			return 0
		}
	}
}
