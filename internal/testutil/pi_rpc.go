package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// PiRPCProcess implements only the pinned official envelopes for hermetic
// tests. It records process and prompt counts without logging environment keys.
func PiRPCProcess(dir string) {
	pid := os.Getpid()
	appendLog := func(name string, value any) {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(91)
		}
		fmt.Fprintln(f, value)
		f.Close()
	}
	if os.Getenv("AGENT_ADAPTOR_PI_EXCLUSIVE_WRITER") == "1" {
		raw, _ := os.ReadFile(filepath.Join(dir, "spawns.log"))
		for _, value := range strings.Fields(string(raw)) {
			old, _ := strconv.Atoi(value)
			if old != pid && ProcessAlive(old) {
				_ = os.WriteFile(filepath.Join(dir, "overlap"), []byte("writer alive"), 0600)
			}
		}
	}
	appendLog("spawns.log", pid)
	if os.Getenv("AGENT_ADAPTOR_PI_FAIL_START_ONCE") == "1" {
		if f, err := os.OpenFile(filepath.Join(dir, "failed-start"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
			f.Close()
			fmt.Fprintln(os.Stderr, "injected pre-delivery failure")
			os.Exit(7)
		}
	}
	session := "pi-session"
	if os.Getenv("AGENT_ADAPTOR_PI_DYNAMIC_SESSIONS") == "1" {
		session = fmt.Sprintf("pi-%d", pid)
	}
	for i, arg := range os.Args {
		if arg == "--session" && i+1 < len(os.Args) {
			session = os.Args[i+1]
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "stderr")); err == nil {
		_, _ = os.Stderr.Write(raw)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	encode := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var command map[string]any
		if json.Unmarshal(scanner.Bytes(), &command) != nil {
			os.Exit(92)
		}
		kind, _ := command["type"].(string)
		switch kind {
		case "get_state":
			state := map[string]any{"sessionId": session, "sessionFile": filepath.Join(dir, session+".jsonl"), "isStreaming": false, "isCompacting": false, "pendingMessageCount": 0}
			if raw, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
				_ = json.Unmarshal(raw, &state)
			}
			_ = encode.Encode(map[string]any{"type": "response", "id": command["id"], "command": kind, "success": true, "data": state})
		case "prompt":
			appendLog("prompts.log", pid)
			cwd, _ := os.Getwd()
			raw, _ := json.Marshal(map[string]any{"Args": os.Args, "Prompt": command["message"], "CWD": cwd})
			_ = os.WriteFile(filepath.Join(dir, "capture.json"), raw, 0600)
			if os.Getenv("AGENT_ADAPTOR_PI_FAIL_AFTER_PROMPT") == "1" {
				os.Exit(7)
			}
			_ = encode.Encode(map[string]any{"type": "response", "id": command["id"], "command": kind, "success": true})
			if ui, err := os.ReadFile(filepath.Join(dir, "ui.json")); err == nil {
				fmt.Fprintln(os.Stdout, string(ui))
				if !scanner.Scan() {
					os.Exit(93)
				}
				_ = os.WriteFile(filepath.Join(dir, "ui-response.json"), scanner.Bytes(), 0600)
			}
			raw, _ = os.ReadFile(filepath.Join(dir, "stdout"))
			for _, line := range strings.Split(string(raw), "\n") {
				var event struct{ Type string }
				_ = json.Unmarshal([]byte(line), &event)
				if event.Type == "session" || strings.TrimSpace(line) == "" {
					continue
				}
				fmt.Fprintln(os.Stdout, line)
			}
			if os.Getenv("AGENT_ADAPTOR_PROTOCOL_HANG") == "1" {
				time.Sleep(5 * time.Minute)
			}
			var code int
			_, _ = fmt.Sscan(os.Getenv("AGENT_ADAPTOR_PROTOCOL_EXIT"), &code)
			if code != 0 {
				os.Exit(code)
			}
		default:
			os.Exit(94)
		}
	}
	os.Exit(0)
}
