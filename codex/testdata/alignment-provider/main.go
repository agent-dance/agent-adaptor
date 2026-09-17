// A deterministic local protocol fixture. It never loads a provider or profile.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func record(v any) {
	f, err := os.OpenFile(os.Getenv("ALIGNMENT_CAPTURE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		defer f.Close()
		_ = json.NewEncoder(f).Encode(v)
	}
}
func output(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }

// admissionControl is used only by the stderr receipt tests. Its acknowledgments
// describe child actions; only the test's actual Process.stderr buffer proves
// parent receipt. Connection deadlines bound failures, never establish readiness.
type admissionControl struct {
	conn    net.Conn
	decoder *json.Decoder
	encoder *json.Encoder
}

func (c *admissionControl) phase(stage string, turn int, n int, err error) bool {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	return c.encoder.Encode(map[string]any{"stage": stage, "turn": turn, "n": n, "error": detail}) == nil
}
func (c *admissionControl) await(command string) bool {
	var got string
	return c.decoder.Decode(&got) == nil && got == command
}

func main() {
	// Require a working native liveness probe before asserting no overlap.
	if !testutil.ProcessAlive(os.Getpid()) {
		fmt.Fprintln(os.Stderr, "fixture process liveness probe failed")
		os.Exit(2)
	}
	alive := 0
	if b, err := os.ReadFile(os.Getenv("ALIGNMENT_CAPTURE")); err == nil {
		for _, line := range bytes.Split(b, []byte("\n")) {
			var old struct {
				Event string
				PID   int
			}
			if json.Unmarshal(line, &old) == nil && old.Event == "start" {
				if testutil.ProcessAlive(old.PID) {
					alive++
				}
			}
		}
	}
	record(map[string]any{"event": "start", "pid": os.Getpid(), "args": os.Args[1:], "previous_alive": alive})
	defer record(map[string]any{"event": "exit", "pid": os.Getpid()})
	scenario := os.Getenv("ALIGNMENT_SCENARIO")
	app := false
	for _, arg := range os.Args[1:] {
		if arg == "app-server" {
			app = true
		}
	}
	if !app {
		if scenario == "stdin-error" {
			output(map[string]any{"type": "thread.started", "thread_id": "thread-exec"})
			output(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "partial"}})
			output(map[string]any{"type": "turn.completed"})
			fmt.Fprint(os.Stderr, "partial-stderr")
			return
		}
		b, _ := io.ReadAll(os.Stdin)
		record(map[string]any{"stdin": string(b)})
		output(map[string]any{"type": "thread.started", "thread_id": "thread-exec"})
		output(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "answer"}})
		fmt.Fprint(os.Stderr, "fixture-stderr")
		if scenario == "malformed" {
			fmt.Println("{broken")
			return
		}
		if scenario == "nonzero" {
			os.Exit(9)
		}
		if scenario == "cancel" {
			time.Sleep(time.Hour)
		}
		output(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 0, "output_tokens": 2}})
		return
	}
	var admission *admissionControl
	if scenario == "stderr-admission" {
		conn, err := net.DialTimeout("tcp", os.Getenv("ALIGNMENT_ADMISSION"), 5*time.Second)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		admission = &admissionControl{conn: conn, decoder: json.NewDecoder(conn), encoder: json.NewEncoder(conn)}
	}
	decoder := json.NewDecoder(bufio.NewReader(os.Stdin))
	thread := ""
	turnN := 0
	availabilityReads := make(map[string]int)
	var lateMetadataID json.RawMessage
	lateMetadataThread := ""
	metadata := func(id, role string) map[string]any {
		return map[string]any{"thread": map[string]any{"id": id, "agentRole": role, "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": thread, "agent_role": role}}}}}
	}
	notify := func(method string, params any) { output(map[string]any{"method": method, "params": params}) }
	for {
		var req struct {
			ID     json.RawMessage            `json:"id"`
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if decoder.Decode(&req) != nil {
			return
		}
		record(req)
		reply := func(value any) { output(map[string]any{"id": req.ID, "result": value}) }
		switch req.Method {
		case "initialize":
			reply(map[string]any{})
		case "thread/start":
			thread = "thread-" + strconv.Itoa(os.Getpid())
			reply(map[string]any{"thread": map[string]any{"id": thread}})
		case "thread/resume":
			_ = json.Unmarshal(req.Params["threadId"], &thread)
			reply(map[string]any{"thread": map[string]any{"id": thread}})
		case "thread/fork":
			thread = "child-" + strconv.Itoa(os.Getpid())
			reply(map[string]any{"thread": map[string]any{"id": thread}})
		case "thread/read":
			if strings.HasPrefix(scenario, "metadata-availability") {
				var id string
				_ = json.Unmarshal(req.Params["threadId"], &id)
				availabilityReads[id]++
				if availabilityReads[id] == 1 {
					output(map[string]any{"id": req.ID, "error": map[string]any{"code": -32603, "message": "failed to read thread: thread-store internal error: failed to read session metadata /private/fixture: rollout at /private/fixture is empty"}})
					notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn-" + strconv.Itoa(turnN), "status": "completed", "usage": map[string]any{"inputTokens": 0, "outputTokens": 2}}})
				} else if scenario == "metadata-availability-late" {
					lateMetadataID = append(json.RawMessage(nil), req.ID...)
					lateMetadataThread = id
				} else {
					reply(metadata(id, "reviewer"))
				}
				continue
			}
			if strings.HasPrefix(scenario, "metadata-wait-") {
				turn := "turn-" + strconv.Itoa(turnN)
				if scenario == "metadata-wait-protocol-exit" {
					notify("item/agentMessage/delta", map[string]any{"threadId": thread, "turnId": "wrong", "itemId": "x", "delta": "must-not-publish"})
				}
				notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed", "usage": map[string]any{"inputTokens": 0, "outputTokens": 2}}})
				if scenario == "metadata-wait-decode-exit" {
					fmt.Print(`{"method":`)
				}
				os.Exit(17)
			}
			var id string
			_ = json.Unmarshal(req.Params["threadId"], &id)
			if scenario == "metadata-late" {
				lateMetadataID = append(json.RawMessage(nil), req.ID...)
				lateMetadataThread = id
				continue
			}
			if scenario == "metadata" || scenario == "metadata-provider-failure" {
				reply(metadata(id, "reviewer"))
			} else {
				output(map[string]any{"id": req.ID, "error": map[string]any{"code": -32601, "message": "metadata unavailable"}})
			}
		case "turn/interrupt":
			reply(map[string]any{})
			return
		case "turn/start":
			turnN++
			if strings.HasPrefix(scenario, "metadata-availability") {
				if len(lateMetadataID) != 0 {
					output(map[string]any{"id": lateMetadataID, "result": metadata(lateMetadataThread, "late-wrong-role")})
					lateMetadataID = nil
				}
				turn := "turn-" + strconv.Itoa(turnN)
				reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
				scoped := func(item any) map[string]any { return map[string]any{"threadId": thread, "turnId": turn, "item": item} }
				notify("item/completed", scoped(map[string]any{"id": "answer", "type": "agentMessage", "text": "answer"}))
				collab := map[string]any{"id": "spawn", "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress", "senderThreadId": thread, "receiverThreadIds": []string{}}
				notify("item/started", scoped(collab))
				collab["status"] = "completed"
				collab["receiverThreadIds"] = []string{"agent-child"}
				notify("item/completed", scoped(collab))
				if turnN > 1 {
					notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "completed", "usage": map[string]any{"inputTokens": 0, "outputTokens": 2}}})
				}
				continue
			}
			if len(lateMetadataID) != 0 {
				output(map[string]any{"id": lateMetadataID, "result": metadata(lateMetadataThread, "late-wrong-role")})
				lateMetadataID = nil
			}
			turn := "turn-" + strconv.Itoa(turnN)
			scoped := func(k string, v any) map[string]any {
				return map[string]any{"threadId": thread, "turnId": turn, "itemId": "answer", k: v}
			}
			if scenario == "reject-start" {
				output(map[string]any{"id": req.ID, "error": map[string]any{"code": -32000, "message": "fixture rejected"}})
				continue
			}
			if scenario == "terminal-eof" {
				// A terminal can be queued before the start response, while EOF
				// is already visible and the real process exit remains pending.
				notify("item/completed", scoped("item", map[string]any{"id": "answer", "type": "agentMessage", "text": "answer"}))
				status := os.Getenv("ALIGNMENT_EOF_TERMINAL")
				if status == "" {
					status = "completed"
				}
				if status != "missing" {
					notify("turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": status}})
				}
				reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
				if os.Getenv("ALIGNMENT_EOF_MALFORMED") == "1" {
					fmt.Println("{broken")
				}
				_ = os.Stdout.Close()
				deadline := time.Now().Add(4 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(os.Getenv("ALIGNMENT_RELEASE")); err == nil {
						fmt.Fprint(os.Stderr, "post-eof-stderr")
						if os.Getenv("ALIGNMENT_EOF_EXIT") == "0" {
							return
						}
						os.Exit(7)
					}
					time.Sleep(time.Millisecond)
				}
				os.Exit(9)
			}
			if scenario == "unrelated-status" || scenario == "unrelated-status-cancel" {
				notify("thread/status/changed", map[string]any{"threadId": "agent-child", "status": map[string]any{"type": "active", "activeFlags": []string{}}})
			}
			if scenario == "child-status" || scenario == "unknown-child-status" || scenario == "early-child-status" {
				collab := map[string]any{"id": "spawn-child-status", "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress", "senderThreadId": thread, "receiverThreadIds": []string{}}
				notify("item/started", scoped("item", collab))
				announce := func() {
					notify("thread/started", map[string]any{"thread": map[string]any{"id": "agent-child", "agentRole": "reviewer", "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": thread, "agent_role": "reviewer", "depth": 1}}}}})
				}
				if scenario == "child-status" {
					announce()
				}
				notify("thread/status/changed", map[string]any{"threadId": "agent-child", "status": map[string]any{"type": "active", "activeFlags": []string{}}})
				if scenario == "early-child-status" {
					announce()
				}
				collab["status"] = "completed"
				collab["receiverThreadIds"] = []string{"agent-child"}
				notify("item/completed", scoped("item", collab))
				notify("thread/status/changed", map[string]any{"threadId": "agent-child", "status": map[string]any{"type": "idle"}})
			}
			if strings.HasPrefix(scenario, "metadata") {
				collab := map[string]any{"id": "lookup-spawn", "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress", "senderThreadId": thread, "receiverThreadIds": []string{}}
				notify("item/started", scoped("item", collab))
				for _, foreign := range []string{"agent-child", "grandchild", "unrelated"} {
					notify("turn/started", map[string]any{"threadId": foreign, "turn": map[string]any{"id": "foreign-turn", "status": "inProgress"}})
					notify("item/completed", map[string]any{"threadId": foreign, "turnId": "foreign-turn", "item": map[string]any{"id": "foreign-item", "type": "future-opaque", "text": "must-not-publish"}})
				}
				receiver := "lookup-child-" + strconv.Itoa(turnN)
				if scenario == "metadata-block-write" {
					receiver = strings.Repeat("x", 1024*1024)
				}
				collab["status"] = "completed"
				collab["receiverThreadIds"] = []string{receiver}
				notify("item/completed", scoped("item", collab))
			}
			// Exercise notification-before-response ordering on the real RPC path.
			notify("turn/plan/updated", scoped("plan", []any{map[string]any{"step": "中文 plan", "status": "pending"}}))
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
			notify("turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn, "status": "inProgress"}})
			n, writeErr := fmt.Fprint(os.Stderr, "fixture-stderr")
			if admission != nil {
				if !admission.phase("turn-written", turnN, n, writeErr) || !admission.await("terminal") {
					return
				}
			}
			if scenario == "facts" {
				item := map[string]any{"id": "mcp-1", "type": "mcpToolCall", "server": "中文__server", "tool": "_read", "arguments": map[string]any{"token": "dummy-secret"}, "status": "inProgress"}
				notify("item/started", scoped("item", item))
				notify("item/started", scoped("item", item))
				item["status"] = "completed"
				item["result"] = map[string]any{"secret": "dummy-result"}
				item["durationMs"] = 0
				notify("item/completed", scoped("item", item))
				notify("item/completed", scoped("item", item))
				collab := map[string]any{"id": "spawn-1", "type": "collabAgentToolCall", "tool": "spawnAgent", "status": "inProgress", "senderThreadId": thread, "receiverThreadIds": []string{"agent-child"}}
				notify("item/started", scoped("item", collab))
				notify("thread/started", map[string]any{"thread": map[string]any{"id": "agent-child", "agentRole": "reviewer", "source": map[string]any{"subAgent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": thread, "agent_role": "reviewer", "depth": 1}}}}})
				collab["status"] = "completed"
				notify("item/completed", scoped("item", collab))
				notify("turn/plan/updated", scoped("plan", []any{}))
			}
			notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": turn, "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 0, "outputTokens": 2, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 2}, "last": map[string]any{"inputTokens": 0, "outputTokens": 2, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 2}}})
			notify("item/agentMessage/delta", scoped("delta", "answer"))
			notify("item/completed", scoped("item", map[string]any{"id": "answer", "type": "agentMessage", "text": "answer"}))
			if scenario == "malformed" {
				fmt.Println("{broken")
				return
			}
			if scenario == "nonzero" {
				os.Exit(9)
			}
			if scenario == "cancel" || scenario == "unrelated-status-cancel" || strings.HasPrefix(scenario, "metadata-wait-") {
				continue
			}
			if turnN > 1 {
				notify("thread/tokenUsage/updated", map[string]any{"threadId": thread, "turnId": "turn-1", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 999, "outputTokens": 999, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 1998}, "last": map[string]any{"inputTokens": 999, "outputTokens": 999, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 1998}}})
			}
			terminal := map[string]any{"id": turn, "status": "completed", "usage": map[string]any{"inputTokens": 0, "outputTokens": 2}}
			if scenario == "metadata-provider-failure" {
				terminal["status"] = "failed"
				terminal["error"] = map[string]any{"message": "parent provider failure"}
			}
			notify("turn/completed", map[string]any{"threadId": thread, "turn": terminal})
			if strings.HasPrefix(scenario, "metadata") {
				notify("turn/completed", map[string]any{"threadId": "grandchild", "turn": map[string]any{"id": "foreign-turn", "status": "completed"}})
				// Late announcements must not become capability facts after the
				// parent terminal; admitted thread/read is the only new proof path.
				notify("thread/started", metadata("late-announcement", "reviewer"))
			}
			if scenario == "metadata-block-write" {
				time.Sleep(time.Hour)
			}
			if admission != nil {
				// The future idle write cannot occur until after the test has inspected
				// the published result and explicitly releases it.
				if !admission.phase("idle-held", turnN, 0, nil) || !admission.await("idle") {
					return
				}
				n, writeErr := fmt.Fprint(os.Stderr, "future-idle-stderr")
				if !admission.phase("idle-written", turnN, n, writeErr) || !admission.await("next") {
					return
				}
			}
		}
	}
}
