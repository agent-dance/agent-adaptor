package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// The existing interactive transport closes stdin at a terminal model stop
// so providers waiting for input EOF can flush their result. Rejected hook
// registration should restore that lifecycle when it degrades to this path.
func TestMessageDegradedHooksRetainInteractiveEOF(t *testing.T) {
	command, dir := messageEOFCommand(t)
	for _, mode := range []string{"ordinary", "degraded-hooks", "degraded-hooks-without-hitl"} {
		t.Run(mode, func(t *testing.T) {
			req := driver.Request{Prompt: "work", Streaming: true,
				Config: Config{CommonConfig: CommonConfig{Command: command, CWD: dir, GracePeriod: 20 * time.Millisecond,
					Env: messageEOFEnv(dir)}},
				Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}
			if mode != "ordinary" {
				req.Messages = newMessageTestPort()
			}
			if mode == "degraded-hooks-without-hitl" {
				req.Policy.HumanDecision.Permission = driver.HumanDecisionAutoApprove
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			res, err := (adapter{}).Run(ctx, req, &messageTestSink{})
			if ctx.Err() != nil {
				t.Fatalf("provider reached message_stop but stdin never closed after hook degradation: %v (run error: %v)", ctx.Err(), err)
			}
			if err != nil || res.Failure != nil || res.Output != "done" {
				t.Fatalf("result=%+v error=%v", res, err)
			}
		})
	}
}

func messageEOFCommand(t *testing.T) (string, string) {
	t.Helper()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return command, t.TempDir()
}

func messageEOFEnv(dir string) []driver.EnvBinding {
	return []driver.EnvBinding{
		{Name: messageHelperEnv, Value: "1"},
		{Name: "CLAUDE_CONFIG_DIR", Value: dir},
		{Name: "MESSAGE_MODE", Value: "eof"},
	}
}

// This subprocess fixture flushes a per-run terminal result only after EOF,
// while its resident mode requires stdin to remain open between results.
func runMessageEOFHelper() int {
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	var frame map[string]any
	if err := dec.Decode(&frame); err != nil {
		return 60
	}
	if claudeTopLevelString(frame, "type") == "control_request" {
		if err := enc.Encode(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": messageInitializeID, "error": "hooks unsupported",
		}}); err != nil {
			return 61
		}
		if err := dec.Decode(&frame); err != nil {
			return 62
		}
	}
	for {
		if claudeTopLevelString(frame, "type") != "user" {
			return 63
		}
		for _, event := range []map[string]any{
			{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}},
			{"type": "stream_event", "event": map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}}},
			{"type": "stream_event", "event": map[string]any{"type": "message_stop"}},
		} {
			if err := enc.Encode(event); err != nil {
				return 64
			}
		}
		resident := os.Getenv("EOF_FIXTURE_RESIDENT") == "1"
		if !resident {
			if err := dec.Decode(&frame); !errors.Is(err, io.EOF) {
				return 65
			}
		}
		if err := enc.Encode(map[string]any{"type": "result", "subtype": "success", "is_error": false,
			"session_id": "session", "result": "done"}); err != nil {
			return 66
		}
		if !resident {
			return 0
		}
		if err := dec.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			return 67
		}
	}
}

func TestMessageDegradedHooksKeepResidentStdinOpen(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "degraded-hooks"}[enabled], func(t *testing.T) {
			command, dir := messageEOFCommand(t)
			env := append(messageEOFEnv(dir), driver.EnvBinding{Name: "EOF_FIXTURE_RESIDENT", Value: "1"})
			d := Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: dir, Env: env}}).(configuredDriver)
			defer d.CloseProcesses(context.Background())
			req := driver.Request{Prompt: "work", Streaming: true,
				Session: &driver.SessionContext{EngineSessionID: "record"},
				Policy:  driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}
			sink := &messageTestSink{}
			for turn := 0; turn < 2; turn++ {
				if enabled {
					req.Messages = newMessageTestPort()
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				response, err := d.Run(ctx, req, sink)
				cancel()
				if err != nil || response.Failure != nil || response.Output != "done" || response.Checkpoint == nil || !response.Checkpoint.Valid {
					t.Fatalf("turn %d response=%+v error=%v", turn, response, err)
				}
				req.Session.State = response.Checkpoint.State
			}
			var spawns int
			for _, event := range sink.events {
				if event.Type == driver.RunEventSpawn {
					spawns++
				}
			}
			if spawns != 1 {
				t.Fatalf("resident spawned %d processes, want one reused across message_stop", spawns)
			}
		})
	}
}
