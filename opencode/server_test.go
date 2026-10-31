package opencode

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

//go:embed testdata/server.jsonl
var serverHappy string

func TestServerProtocolContract(t *testing.T) {
	for _, tc := range []struct {
		name, events, terminal string
		infra, failed          bool
	}{
		{name: "success", events: serverHappy},
		{name: "malformed", events: "{broken\n" + serverHappy, failed: true},
		{name: "missing-session", events: strings.Replace(serverHappy, `"sessionID":"ses_child",`, "", 1), failed: true},
		{name: "missing-terminal", events: serverHappy, terminal: `{}`, failed: true},
		{name: "mixed-terminal", events: serverHappy, terminal: `{"info":{"id":"message","sessionID":"foreign","role":"assistant","time":{"completed":2}},"parts":[]}`, failed: true},
		{name: "terminal-error", events: strings.Replace(serverHappy, `"finish":"stop"`, `"finish":"stop","error":{"name":"APIError","data":{"message":"failed"}}`, 1), failed: true},
		{name: "incomplete-tool-loop", events: strings.ReplaceAll(serverHappy, `"finish":"stop"`, `"finish":"tool-calls"`), failed: true},
		{name: "missing-text-end", events: strings.Replace(serverHappy, `,"end":2`, "", 1), failed: true},
		{name: "missing-step-end", events: strings.Replace(serverHappy, `"type":"step-finish"`, `"type":"unknown"`, 1), failed: true},
		{name: "process-exit", events: strings.Split(serverHappy, `{"type":"session.status","properties":{"sessionID":"ses_child","status":{"type":"idle"}}}`)[0] + "{\"type\":\"test.exit\",\"properties\":{}}\n", infra: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, tc.events, "server stderr", 0)
			if tc.terminal != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "terminal.json"), []byte(tc.terminal), 0600); err != nil {
					t.Fatal(err)
				}
			}
			d := Driver(Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sink := &testutil.EventRecorder{}
			resp, err := d.Run(ctx, driver.Request{Prompt: "hello", Streaming: true, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}}, sink)
			if (err != nil) != tc.infra || (resp.Failure != nil) != tc.failed {
				t.Fatalf("err=%v failure=%+v", err, resp.Failure)
			}
			if got := resp.Checkpoint != nil && resp.Checkpoint.Valid; got == (tc.failed || tc.infra) {
				t.Fatalf("checkpoint=%+v", resp.Checkpoint)
			}
			if resp.RawStreams == nil || !strings.HasPrefix(resp.RawStreams.Stdout, "opencode server listening on http://127.0.0.1:") || resp.RawStreams.Stderr != "server stderr" || !strings.Contains(resp.Metadata["opencode.event_stream"], "server.connected") {
				t.Fatalf("audit missing: %+v", resp)
			}
			for _, v := range adaptertest.VerifyStreamSequence(sink.StreamSnapshot()) {
				t.Error(v)
			}
			for _, v := range adaptertest.VerifyResponse(&resp) {
				t.Error(v)
			}
			if !tc.infra && !tc.failed {
				if resp.Output != "OK" || resp.Model != "model" || resp.Provider != "test" || resp.Usage == nil || resp.Usage.InputTokens != 5 || resp.RawStreams.Terminal == nil {
					t.Fatalf("response=%+v", resp)
				}
			}
		})
	}
}

func TestServerNativeSchemaAndPrePromptResumeReject(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, strings.Replace(serverHappy, `"finish":"stop"`, `"finish":"tool-calls","structured":{"value":"OK"}`, 1), "", 0)
	d := Driver(Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
	req := driver.Request{Prompt: "schema", Streaming: true, OutputSchema: &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: json.RawMessage(`{"type":"object"}`)}, StructuredOutputSource: driver.StructuredOutputSourceNative}
	resp, err := d.Run(context.Background(), req, &testutil.EventRecorder{})
	if err != nil || resp.Failure != nil || resp.StructuredOutput == nil || string(resp.StructuredOutput.RawJSON) != `{"value":"OK"}` {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	raw, _ := os.ReadFile(capture)
	var call struct{ Input map[string]any }
	if json.Unmarshal(raw, &call) != nil {
		t.Fatal("capture malformed")
	}
	if call.Input["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatal("native schema not delivered")
	}
	if err := os.Remove(capture); err != nil {
		t.Fatal(err)
	}
	req.Session = &driver.SessionContext{Mode: driver.SessionContinueOnly, State: &driver.SessionState{ResumeID: "missing"}}
	_, err = d.Run(context.Background(), req, &testutil.EventRecorder{})
	var rejected *engine.ResumeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("resume=%v", err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("prompt delivered before resume validation")
	}
}

func TestServerCancelAndSSEFraming(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	command, _ := testutil.ProtocolCommand(t, serverHappy, "cancel stderr", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sink := testutil.NewChunkCancelRecorder(cancel, "stdout", "stderr")
	resp, err := Driver(Config{Command: command}).Run(ctx, driver.Request{Streaming: true}, sink)
	if !errors.Is(err, context.Canceled) || resp.Checkpoint != nil || resp.RawStreams == nil {
		t.Fatalf("response=%+v err=%v", resp, err)
	}
	frames := make(chan sseFrame, 4)
	readSSE(context.Background(), strings.NewReader(": comment\r\ndata: {\r\ndata: \"type\":\"test\"}\r\n\r\n"), frames)
	frame := <-frames
	if !json.Valid(frame.data) || !bytes.Contains(frame.data, []byte("\n")) {
		t.Fatalf("frame=%s", frame.data)
	}
	if last := <-frames; !errors.Is(last.err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF=%v", last.err)
	}
}

func FuzzServerParser(f *testing.F) {
	f.Add([]byte(serverHappy))
	f.Add([]byte("{bad\n"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		p := newServerParser(&testutil.EventRecorder{}, true)
		p.sessionID = "ses_child"
		for _, line := range bytes.Split(raw, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			if _, err := p.consume(line); err != nil {
				p.protocolError = err.Error()
				break
			}
		}
		resp := p.finish(driver.Response{}, nil, nil, driver.Request{Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart}})
		if resp.Checkpoint != nil {
			t.Fatal("no official terminal but checkpoint exists")
		}
	})
}
