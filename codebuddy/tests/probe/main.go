// Command probe captures CodeBuddy stream-json control frames from the real
// CLI. It is a protocol collection tool, not a test.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	cli := flag.String("cli", "codebuddy", "CodeBuddy executable")
	prompt := flag.String("prompt", "Reply with OK.", "user prompt")
	out := flag.String("out", "tests/probe/fixtures/control_capture.jsonl", "capture file")
	timeout := flag.Duration("timeout", 4*time.Minute, "overall timeout")
	flag.Parse()
	if err := run(*cli, *prompt, *out, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(1)
	}
}

func run(cli, prompt, output string, timeout time.Duration) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "--input-format=stream-json", "--output-format=stream-json", "--verbose")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Wait() }()

	record := func(direction string, frame map[string]any) error {
		entry := map[string]any{"direction": direction, "frame": frame, "at": time.Now().UTC().Format(time.RFC3339Nano)}
		raw, _ := json.Marshal(entry)
		_, err := file.Write(append(raw, '\n'))
		return err
	}
	send := func(frame map[string]any) error {
		if err := record("send", frame); err != nil {
			return err
		}
		raw, _ := json.Marshal(frame)
		_, err := stdin.Write(append(raw, '\n'))
		return err
	}
	if err := send(map[string]any{
		"type":       "control_request",
		"request_id": "probe-initialize",
		"request": map[string]any{
			"subtype":      "initialize",
			"capabilities": map[string]any{"askUserQuestion": true},
		},
	}); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), 8*1024*1024)
	initialized := false
	for scanner.Scan() {
		raw := scanner.Bytes()
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			continue
		}
		if err := record("receive", frame); err != nil {
			return err
		}
		if !initialized && frame["type"] == "control_response" && stringValue(objectValue(frame["response"])["request_id"]) == "probe-initialize" {
			initialized = true
			if err := send(map[string]any{
				"type": "user", "session_id": "",
				"message":            map[string]any{"role": "user", "content": prompt},
				"parent_tool_use_id": nil,
			}); err != nil {
				return err
			}
		}
		if frame["type"] != "control_request" {
			continue
		}
		request := objectValue(frame["request"])
		if strings.ToLower(stringValue(request["subtype"])) != "can_use_tool" {
			continue
		}
		response := map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": stringValue(frame["request_id"]),
				"response": map[string]any{
					"allowed":      true,
					"updatedInput": request["input"],
					"tool_use_id":  stringValue(request["tool_use_id"]),
				},
			},
		}
		if err := send(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func objectValue(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
