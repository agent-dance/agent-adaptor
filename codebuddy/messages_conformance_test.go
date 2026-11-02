package codebuddy

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/adaptertest"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func codeBuddyMessageFixture(transport string) func(*testing.T, adaptertest.MessageScenario) adaptertest.MessageFixture {
	return func(t *testing.T, _ adaptertest.MessageScenario) adaptertest.MessageFixture {
		d, _ := codeBuddyTurnDriver(t)
		cfg := d.(configuredDriver).cfg
		path := filepath.Join(t.TempDir(), "evidence.json")
		cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CODEBUDDY_MSG_EVIDENCE", Value: path})
		req := driver.Request{Prompt: "initial", Config: cfg}
		if transport == "control" {
			req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
		}
		if transport == "resident" {
			req.Session = &driver.SessionContext{EngineSessionID: "adaptertest-thread"}
		}
		return adaptertest.MessageFixture{Driver: d, Request: req, Evidence: func() adaptertest.MessageEvidence {
			var e adaptertest.MessageEvidence
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &e); err != nil {
				t.Fatal(err)
			}
			return e
		}}
	}
}

func runCodeBuddyMessageConformanceHelper() int {
	path := os.Getenv("CODEBUDDY_MSG_EVIDENCE")
	var e adaptertest.MessageEvidence
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &e)
	}
	resumed := false
	for i, arg := range os.Args {
		if arg == "--resume" && i+1 < len(os.Args) && os.Args[i+1] == "codebuddy-persistent-session" {
			resumed = true
		}
	}
	seenPrompt := false
	enc := json.NewEncoder(os.Stdout)
	emit := func(input string) {
		e.Resumed = resumed || seenPrompt
		seenPrompt = true
		e.Received = append(e.Received, input)
		e.Turns = []driver.TurnCompleted{{Text: "ok", Usage: &driver.Usage{InputTokens: 4, OutputTokens: 3}}}
		b, _ := json.Marshal(e)
		_ = os.WriteFile(path+".next", b, 0o600)
		_ = os.Rename(path+".next", path)
		_ = enc.Encode(map[string]any{"type": "system", "subtype": "init", "session_id": "codebuddy-persistent-session", "model": "fake"})
		_ = enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": "codebuddy-persistent-session", "result": "ok", "usage": map[string]int{"input_tokens": 4, "output_tokens": 3}})
	}
	control := false
	for _, arg := range os.Args[1:] {
		control = control || arg == "--input-format=stream-json"
	}
	if !control {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 80
		}
		_ = b
		emit(os.Args[len(os.Args)-1])
		return 0
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "agent-adaptor-initialize") {
			_ = enc.Encode(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "agent-adaptor-initialize", "response": map[string]any{}}})
			continue
		}
		var frame struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &frame) != nil || frame.Type != "user" {
			return 81
		}
		emit(frame.Message.Content)
	}
	return 0
}
