package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestRPCProtocolContract(t *testing.T) {
	for _, tc := range []struct {
		name, output, state string
		exit                int
		valid               bool
	}{
		{"success", happy, "", 0, true},
		{"nonzero", happy, "", 7, false},
		{"malformed", happy + "{broken\n", "", 0, false},
		{"missing-terminal", strings.Split(happy, `{"type":"agent_end"`)[0], "", 0, false},
		{"missing-checkpoint", happy, `{"sessionId":""}`, 0, false},
		{"queued-session", happy, `{"pendingMessageCount":1}`, 0, false},
		{"business-failure", strings.ReplaceAll(happy, `"stopReason":"stop"`, `"stopReason":"error","errorMessage":"provider failed"`), "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, tc.output, "RPC stderr", tc.exit)
			if tc.state != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "state.json"), []byte(tc.state), 0600); err != nil {
					t.Fatal(err)
				}
			}
			d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: t.TempDir()}}}).(configuredDriver)
			defer d.CloseProcesses(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			sink := &testutil.EventRecorder{}
			r, err := d.Run(ctx, driver.Request{Prompt: "hello", Streaming: true, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart, EngineSessionID: "test"}}, sink)
			if valid := r.Checkpoint != nil && r.Checkpoint.Valid; valid != tc.valid {
				t.Fatalf("checkpoint=%+v failure=%+v err=%v", r.Checkpoint, r.Failure, err)
			}
			if tc.valid && (err != nil || r.Failure != nil || r.Output != "OK" || r.RawStreams.Terminal == nil || r.Usage == nil) {
				t.Fatalf("response=%+v err=%v", r, err)
			}
			if r.RawStreams == nil || !strings.Contains(r.RawStreams.Stderr, "RPC stderr") {
				t.Fatal("raw audit lost")
			}
			for _, v := range adaptertest.VerifyStreamSequence(sink.StreamSnapshot()) {
				t.Error(v)
			}
			if !tc.valid && r.Failure == nil && err == nil {
				t.Fatal("failed protocol succeeded")
			}
			if tc.exit != 0 && r.ExitCode != tc.exit {
				t.Fatalf("unexpected exit lost: %d", r.ExitCode)
			}
		})
	}
}

func TestRPCNativeDialogs(t *testing.T) {
	for _, tc := range []struct {
		method, answer, key string
		value               any
	}{
		{"select", "B", "value", "B"}, {"confirm", "yes", "confirmed", true}, {"confirm", "no", "confirmed", false}, {"input", "free text", "value", "free text"}, {"editor", "edited", "value", "edited"},
	} {
		t.Run(tc.method+"-"+tc.answer, func(t *testing.T) {
			f := persistentRPCFixture(t)
			ui, _ := json.Marshal(map[string]any{"type": "extension_ui_request", "id": "ui-1", "method": tc.method, "title": "Choose", "message": "Native question", "options": []string{"A", "B"}, "prefill": "original"})
			if err := os.WriteFile(filepath.Join(f.dir, "ui.json"), ui, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream := f.thread.Stream(ctx, "ask", adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}))
			seen := 0
			for e := range stream.Events() {
				if q, ok := e.(*adaptor.ApprovalRequest); ok {
					seen++
					if err := q.Answer(ctx, tc.answer); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := stream.Result(); err != nil {
				t.Fatal(err)
			}
			var reply map[string]any
			raw, _ := os.ReadFile(filepath.Join(f.dir, "ui-response.json"))
			_ = json.Unmarshal(raw, &reply)
			if seen != 1 || reply[tc.key] != tc.value || reply["id"] != "ui-1" {
				t.Fatalf("seen=%d reply=%s", seen, raw)
			}
		})
	}
}

func TestRPCDialogCancellationAndSchemaFailure(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		f := persistentRPCFixture(t)
		if err := os.WriteFile(filepath.Join(f.dir, "ui.json"), []byte(`{"type":"extension_ui_request","id":"ui","method":"input","title":"Question"}`), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s := f.thread.Stream(ctx, "ask", adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}))
		for e := range s.Events() {
			if _, ok := e.(*adaptor.ApprovalRequest); ok {
				s.Cancel()
			}
		}
		if _, err := s.Result(); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
	})
	t.Run("schema", func(t *testing.T) {
		f := persistentRPCFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.thread.Run(ctx, "schema", adaptor.WithSchema[struct {
			Value string `json:"value"`
		}]()); err == nil {
			t.Fatal("invalid JSON succeeded")
		}
		for _, pid := range f.pids(t) {
			if testutil.ProcessAlive(pid) {
				t.Fatal("schema failure retained dirty writer")
			}
		}
	})
}
