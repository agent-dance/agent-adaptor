// Package alignmentprotocol contains independent T21 protocol fixtures only.
package alignmentprotocol

import (
	"bufio"
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
	switch os.Getenv("T21_PROVIDER") {
	case "codex":
		return codex()
	case "cursor": // Cursor's print prompt is argv, with no stdin protocol.
	default:
		rich := false
		for _, a := range os.Args {
			if a == "stream-json" {
				rich = true
			}
		}
		if rich && os.Getenv("T21_INPUT_NDJSON") == "1" {
			if _, e := bufio.NewReader(os.Stdin).ReadString('\n'); e != nil {
				return 21
			}
		} else {
			if _, e := io.Copy(io.Discard, os.Stdin); e != nil {
				return 22
			}
		}
	}
	b, e := os.ReadFile(os.Getenv("T21_PROTOCOL"))
	if e != nil {
		return 23
	}
	fmt.Fprint(os.Stdout, string(b))
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
