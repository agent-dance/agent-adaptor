package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/adaptertest"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func codexMessageFixture(streaming bool) func(*testing.T, adaptertest.MessageScenario) adaptertest.MessageFixture {
	return func(t *testing.T, scenario adaptertest.MessageScenario) adaptertest.MessageFixture {
		t.Helper()
		_, req, _, _ := newPersistentCodexTest(t, "")
		cfg := req.Config.(Config)
		path := filepath.Join(t.TempDir(), "evidence.json")
		script, err := json.Marshal(scenario)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CODEX_MSG_SCENARIO", Value: string(script)}, driver.EnvBinding{Name: "CODEX_MSG_EVIDENCE", Value: path})
		d := Driver(cfg)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := d.(driver.ProcessLifecycleDriver).CloseProcesses(ctx); err != nil {
				t.Error(err)
			}
		})
		req.Config = nil // Resolver and Run must both observe captured construction config.
		req.Streaming = streaming
		req.Session = nil
		req.Spawn = true
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

func runCodexMessageConformanceHelper() int {
	var scenario adaptertest.MessageScenario
	if json.Unmarshal([]byte(os.Getenv("CODEX_MSG_SCENARIO")), &scenario) != nil {
		return 80
	}
	path := os.Getenv("CODEX_MSG_EVIDENCE")
	var evidence adaptertest.MessageEvidence
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &evidence)
	}
	evidence.Turns = nil
	evidence.Resumed = false
	save := func() {
		b, _ := json.Marshal(evidence)
		_ = os.WriteFile(path+".next", b, 0o600)
		_ = os.Rename(path+".next", path)
	}
	save()
	usage := &driver.Usage{InputTokens: 2, OutputTokens: 1}
	if hasProcessArg("exec") {
		if hasProcessArg("resume") {
			evidence.Resumed = true
			evidence.Retained = append([]string(nil), evidence.Received...)
		}
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 81
		}
		evidence.Received = append(evidence.Received, string(input))
		evidence.Turns = []driver.TurnCompleted{{Usage: &driver.Usage{InputTokens: 1, OutputTokens: 1}}}
		save()
		_, _ = io.WriteString(os.Stdout, "{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n")
		return 0
	}
	reader := bufio.NewScanner(os.Stdin)
	reader.Buffer(make([]byte, 4096), 4<<20)
	writer := bufio.NewWriter(os.Stdout)
	turn := 0
	complete := func(status, text string) {
		evidence.Turns = append(evidence.Turns, driver.TurnCompleted{Text: text, Usage: usage, Interrupted: status == "interrupted"})
		save()
		writeHelperMessageCompletion(writer, turn, status, text)
	}
	initial := false
	for reader.Scan() {
		var req helperRequest
		if json.Unmarshal(reader.Bytes(), &req) != nil {
			return 82
		}
		switch req.Method {
		case "initialize":
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"userAgent": "fake-codex"}})
		case "initialized":
		case "thread/start", "thread/resume":
			if req.Method == "thread/resume" {
				evidence.Resumed = true
				evidence.Retained = append([]string(nil), evidence.Received...)
				save()
			}
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "turn/start":
			turn++
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				return 83
			}
			texts := make([]string, len(params.Input))
			for i, v := range params.Input {
				texts[i] = v.Text
				evidence.Received = append(evidence.Received, v.Text)
			}
			initial = turn == 1 && len(texts) > 0 && texts[0] == "hello"
			save()
			id := "turn-" + strconv.Itoa(turn)
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"turn": map[string]any{"id": id, "status": "inProgress"}}})
			writeHelperNotification(writer, "turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": id, "status": "inProgress"}})
			if initial && scenario.Boundary == driver.BoundaryImmediate {
				continue
			}
			complete("completed", "reply")
		case "turn/steer":
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
				ExpectedTurnID string `json:"expectedTurnId"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				return 85
			}
			for _, v := range params.Input {
				evidence.Received = append(evidence.Received, v.Text)
			}
			save()
			if scenario.Name == "write_failure" {
				writeHelperJSON(writer, map[string]any{"id": req.ID, "error": map[string]any{"code": -32603, "message": "provider input write rejected before acceptance"}})
				evidence.Rejected = append(evidence.Rejected, "msg-1")
				save()
				complete("completed", "reply")
				continue
			}
			if scenario.Name == "process_exit" {
				return 86
			}
			if scenario.Name == "ambiguous" {
				// The peer read the guidance, but its response does not identify
				// the expected accepting turn. A retry must carry ambiguity.
				evidence.Ambiguous = append(evidence.Ambiguous, "msg-1")
				save()
				writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"turnId": "unrelated-turn"}})
				complete("completed", "reply")
				continue
			}
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{"turnId": params.ExpectedTurnID}})
			complete("completed", strings.Join(func() []string {
				var out []string
				for _, v := range params.Input {
					out = append(out, v.Text)
				}
				return out
			}(), "\n\n"))
		case "turn/interrupt":
			writeHelperJSON(writer, helperResponse{ID: req.ID, Result: map[string]any{}})
			if scenario.Name == "interrupt" {
				complete("interrupted", "partial")
			}
		}
	}
	return 0
}
