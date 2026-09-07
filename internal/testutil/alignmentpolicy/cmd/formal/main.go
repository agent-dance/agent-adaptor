// Command formal is an offline CLI fixture speaking only the explicit protocol
// frames needed by T22. It never contacts a provider or reads native profiles.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

func has(v string) bool {
	for _, a := range os.Args[1:] {
		if a == v {
			return true
		}
	}
	return false
}
func val(flag string) string {
	for i, a := range os.Args {
		if a == flag && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}
func emit(v any) { b, _ := json.Marshal(v); fmt.Println(string(b)) }
func main() {
	if os.Getenv("T22_RESIDENT") == "1" {
		resident()
		return
	}
	if has("--version") {
		fmt.Println("2.1.159 (offline T22 fixture)")
		return
	}
	interactive := has("--input-format")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var input any
	var prompt string
	if interactive {
		if !scanner.Scan() {
			return
		}
		if json.Unmarshal(scanner.Bytes(), &input) != nil {
			os.Exit(21)
		}
		b, _ := json.Marshal(input)
		prompt = string(b)
	} else {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(22)
		}
		prompt = string(b)
	}
	receipt := map[string]any{"args": os.Args[1:], "prompt": prompt}
	if file := val("--append-system-prompt-file"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			os.Exit(23)
		}
		receipt["append"] = string(b)
	}
	if interactive {
		emit(map[string]any{"type": "system", "subtype": "init", "session_id": "session-t22-formal"})
		name := "AskUserQuestion"
		payload := map[string]any{"questions": []any{map[string]any{"question": "Pick location", "header": "Location", "multiSelect": false, "options": []any{map[string]any{"label": "docs", "description": "documentation"}, map[string]any{"label": "src", "description": "sources"}}}}}
		if strings.Contains(prompt, "SCENARIO=plan") {
			name = "ExitPlanMode"
			payload = map[string]any{"plan": "review change"}
		}
		if strings.Contains(prompt, "SCENARIO=permission") {
			name = "Bash"
			payload = map[string]any{"command": "echo fixture"}
		}
		emit(map[string]any{"type": "control_request", "request_id": "control-t22", "request": map[string]any{"subtype": "can_use_tool", "tool_name": name, "tool_use_id": "tool-t22", "input": payload}})
		if scanner.Scan() {
			var response any
			if json.Unmarshal(scanner.Bytes(), &response) != nil {
				os.Exit(24)
			}
			receipt["response"] = response
		}
	}
	if p := os.Getenv("T22_RECEIPT"); p != "" {
		b, _ := json.Marshal(receipt)
		if err := os.WriteFile(p, b, 0600); err != nil {
			os.Exit(25)
		}
	}
	value := any(true)
	if strings.Contains(prompt, "SCENARIO=invalid") {
		value = "wrong"
	}
	result := "formal-native"
	if !has("--json-schema") {
		result = `{"ok":true}`
	}
	emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "session-t22-formal", "result": result, "structured_output": map[string]any{"ok": value}, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}})
}

func resident() {
	path := os.Getenv("T22_PROCESS_LOG")
	listener, err := net.Listen("tcp4", os.Getenv("T22_WRITER_ADDRESS"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "second writer started before old exit")
		os.Exit(31)
	}
	var once sync.Once
	log := func(v any) {
		b, _ := json.Marshal(v)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(32)
		}
		file.Write(append(b, '\n'))
		file.Close()
	}
	cleanup := func() { once.Do(func() { log(map[string]any{"kind": "exit", "pid": os.Getpid()}); listener.Close() }) }
	defer cleanup()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() { <-signals; cleanup(); os.Exit(0) }()
	log(map[string]any{"kind": "start", "pid": os.Getpid(), "args": os.Args[1:]})
	send := func(prompt string) {
		log(map[string]any{"kind": "prompt", "pid": os.Getpid(), "prompt": prompt})
		if has("--input-format=stream-json") {
			emit(map[string]any{"type": "system", "subtype": "init", "session_id": "resident-session"})
		}
		emit(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "resident-session", "result": `{"ok":true}`, "structured_output": map[string]any{"ok": true}, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}})
	}
	if has("--input-format=stream-json") {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			var frame map[string]any
			if json.Unmarshal(scanner.Bytes(), &frame) != nil {
				os.Exit(33)
			}
			if frame["type"] == "control_request" {
				emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": frame["request_id"], "response": map[string]any{}}})
				continue
			}
			if frame["type"] == "user" {
				msg, _ := frame["message"].(map[string]any)
				send(fmt.Sprint(msg["content"]))
			}
		}
	} else {
		send(os.Args[len(os.Args)-1])
	}
}
