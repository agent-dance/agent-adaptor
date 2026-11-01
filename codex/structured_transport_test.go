package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/profileinstructions"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
	"github.com/agent-dance/agent-adaptor/v2/memory"
)

const codexAnswerSchema = `{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`

func TestCodexStructuredTransportUsesResolvedMechanism(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema *driver.OutputSchema
		source driver.StructuredOutputSource
		want   bool
	}{
		{name: "native", schema: &driver.OutputSchema{SchemaJSON: []byte(codexAnswerSchema)}, source: driver.StructuredOutputSourceNative, want: true},
		{name: "prompt validation", schema: &driver.OutputSchema{SchemaJSON: []byte(codexAnswerSchema)}, source: driver.StructuredOutputSourcePromptValidate},
		{name: "unresolved schema", schema: &driver.OutputSchema{SchemaJSON: []byte(codexAnswerSchema)}},
		{name: "missing schema", source: driver.StructuredOutputSourceNative},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := driver.Request{OutputSchema: tc.schema, StructuredOutputSource: tc.source}
			if got := usesCodexAppServer(req); got != tc.want {
				t.Fatalf("app-server=%v, want %v", got, tc.want)
			}
			opts, err := buildAppServerOptions(req, Config{}, "codex", nil, profileinstructions.Prepared{})
			if err != nil || (opts.OutputSchema != nil) != tc.want {
				t.Fatalf("native schema=%+v, err=%v", opts.OutputSchema, err)
			}
		})
	}
}

func TestCodexNativeNonStreamingContinuationPreservesSchemaAndAudit(t *testing.T) {
	for _, spawn := range []bool{false, true} {
		t.Run(fmt.Sprintf("spawn=%v", spawn), func(t *testing.T) {
			a, req, spawns, _ := newPersistentCodexTest(t, "structured")
			defer closeCodexTestAdapter(t, a)
			rpcFile := addCodexRPCLog(&req)
			setCodexNativeSchema(&req)
			req.Spawn = spawn
			port := newCodexMessageTestPort()
			port.ends = [][]driver.MessageItem{{{ID: "one", Text: "continue one", Timing: driver.TimingAfterTurn}}, {{ID: "two", Text: "continue two", Timing: driver.TimingAfterTurn}}}
			req.Messages = port
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := a.Run(ctx, req, &testutil.EventRecorder{})
			if err != nil || result.Failure != nil {
				t.Fatalf("Run: %v, failure=%+v", err, result.Failure)
			}
			assertCodexNativeResponse(t, result, req, 3)
			for _, id := range []string{"one", "two"} {
				acks := port.acks[id]
				if len(acks) != 1 || !acks[0].Delivered {
					t.Fatalf("Ack(%s)=%+v", id, acks)
				}
			}
			assertCodexSchemaRPCs(t, rpcFile, req.OutputSchema.SchemaJSON, 3)
			if got := helperSpawnCount(t, spawns); got != 1 {
				t.Fatalf("continuations spawned %d processes", got)
			}
			if !spawn {
				next := resumedCodexRequest(req, result)
				next.Messages = nil
				// A schema is a turn field, so changing it must reuse the writer.
				next.OutputSchema = &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: []byte(`{"type":"object","properties":{"answer":{"const":42}},"required":["answer"],"additionalProperties":false}`)}
				resumed := runCodexTurn(t, a, next)
				assertCodexNativeResponse(t, resumed, next, 1)
				if got := helperSpawnCount(t, spawns); got != 1 {
					t.Fatalf("schema change restarted resident process; spawns=%d", got)
				}
				requests := readCodexRPCs(t, rpcFile)
				var lastSchema json.RawMessage
				for _, rpc := range requests {
					if rpc.Method == "turn/start" {
						var params struct {
							OutputSchema json.RawMessage `json:"outputSchema"`
						}
						_ = json.Unmarshal(rpc.Params, &params)
						lastSchema = params.OutputSchema
					}
				}
				if !jsonEqual(lastSchema, next.OutputSchema.SchemaJSON) {
					t.Fatalf("resumed turn schema=%s", lastSchema)
				}
			}
		})
	}
}

func TestCodexNativeResultAndEventsPreserveThreadAndSpawn(t *testing.T) {
	for _, consumeEvents := range []bool{false, true} {
		t.Run(fmt.Sprintf("events=%v", consumeEvents), func(t *testing.T) {
			low, req, spawns, activeFile := newPersistentCodexTest(t, "structured")
			configured := configuredDriver{adapter: low.(adapter), cfg: req.Config.(Config)}
			agent := adaptor.New(configured, adaptor.WithThreadStore(memory.NewStore()), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := agent.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			thread := agent.Thread("native-schema")
			for i, spawn := range []bool{false, false, true, false, false} {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				opts := []adaptor.CallOption{adaptor.WithSchemaJSON([]byte(codexAnswerSchema))}
				if spawn {
					opts = append(opts, adaptor.WithSpawn())
				}
				stream := thread.Send(ctx, "extract", opts...)
				var done *adaptor.Done
				if consumeEvents {
					for event := range stream.Events() {
						if value, ok := event.(adaptor.Done); ok {
							done = &value
						}
					}
				}
				result, err := stream.Result()
				cancel()
				if err != nil || result == nil {
					t.Fatalf("turn %d: result=%+v err=%v", i, result, err)
				}
				if consumeEvents && (done == nil || done.Result != result || done.Err != err) {
					t.Fatalf("Done differs from Result: %+v", done)
				}
				var decoded struct {
					Answer int `json:"answer"`
				}
				if err := result.Decode(&decoded); err != nil || decoded.Answer != 42 {
					t.Fatalf("Decode=%+v err=%v", decoded, err)
				}
				raw := result.Raw()
				if result.Text != `{"answer":42}` || result.Summary != "" || result.Metadata["transport"] != "app-server" || result.Usage == nil || result.Usage.InputTokens != 2 || result.Usage.OutputTokens != 1 || raw.Terminal == nil || raw.Terminal.Event != "turn/completed" || !strings.Contains(raw.Stdout, `"method":"item/completed"`) || !strings.Contains(raw.Stderr, "structured diagnostic") {
					t.Fatalf("turn %d audit: %+v raw=%+v", i, result, raw)
				}
				transcript := result.Transcript()
				if len(transcript) < 3 || transcript[len(transcript)-2].Text != result.Text || transcript[len(transcript)-1].Kind != driver.TranscriptResult {
					t.Fatalf("transcript=%+v", transcript)
				}
				if _, err := thread.Checkpoint(context.Background()); err != nil {
					t.Fatalf("checkpoint: %v", err)
				}
				wantSpawns := []int{1, 1, 2, 3, 3}[i]
				if got := helperSpawnCount(t, spawns); got != wantSpawns {
					t.Fatalf("turn %d spawns=%d, want %d", i, got, wantSpawns)
				}
				if spawn {
					assertHelperInactive(t, activeFile)
				}
			}
		})
	}
}

func TestCodexNativeNonStreamingInvalidOutputRetainsAudit(t *testing.T) {
	for _, resident := range []bool{false, true} {
		for _, mode := range []string{"structured_missing", "structured_invalid", "structured_mismatch", "structured_malformed"} {
			t.Run(fmt.Sprintf("%s/resident=%v", mode, resident), func(t *testing.T) {
				a, req, _, _ := newPersistentCodexTest(t, mode)
				defer closeCodexTestAdapter(t, a)
				setCodexNativeSchema(&req)
				req.Spawn = !resident
				req.Runtime.Ensured = []driver.RuntimeServiceRef{{ID: "observed-service", Lifecycle: driver.RuntimeLifecycleShared, Status: driver.RuntimeServiceRunning, Health: driver.RuntimeHealthHealthy}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := a.Run(ctx, req, &testutil.EventRecorder{})
				if result.Checkpoint != nil || result.Metadata["transport"] != "app-server" || len(result.RuntimeServices) != 1 || result.RawStreams == nil || !strings.Contains(result.RawStreams.Stderr, "structured diagnostic") {
					t.Fatalf("partial audit: result=%+v err=%v", result, err)
				}
				if mode == "structured_malformed" {
					if err == nil || !strings.Contains(result.RawStreams.Stdout, "{broken") {
						t.Fatalf("protocol outcome: %+v err=%v", result, err)
					}
					return
				}
				if err != nil || result.Failure == nil || result.Failure.Code != driver.FailurePolicyError || result.StructuredOutput == nil || result.StructuredOutput.Valid || len(result.StructuredOutput.ValidationErrors) == 0 || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != "turn/completed" {
					t.Fatalf("invalid native result=%+v err=%v", result, err)
				}
				if mode == "structured_invalid" && (result.Output != "not json" || result.Usage == nil || result.Usage.InputTokens != 2 || len(result.Transcript) < 3 || result.Transcript[len(result.Transcript)-2].Text != "not json") {
					t.Fatalf("invalid candidate audit=%+v", result)
				}
				if mode == "structured_mismatch" && (string(result.StructuredOutput.RawJSON) != `{"answer":"wrong"}` || result.Output != `{"answer":"wrong"}` || result.Usage == nil || result.Usage.InputTokens != 2) {
					t.Fatalf("schema mismatch audit=%+v", result)
				}
				if mode == "structured_missing" && (result.Output != "" || len(result.StructuredOutput.RawJSON) != 0 || result.Usage != nil) {
					t.Fatalf("missing candidate audit=%+v", result)
				}
			})
		}
	}
}

func setCodexNativeSchema(req *driver.Request) {
	req.Streaming = false
	req.StructuredOutputSource = driver.StructuredOutputSourceNative
	req.OutputSchema = &driver.OutputSchema{Format: driver.OutputFormatJSONSchema, SchemaJSON: []byte(codexAnswerSchema), OnInvalid: driver.StructuredOutputFailRun}
}

func addCodexRPCLog(req *driver.Request) string {
	cfg := req.Config.(Config)
	path := filepath.Join(cfg.CWD, "rpc.jsonl")
	cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CODEX_HELPER_RPC_FILE", Value: path})
	req.Config = cfg
	return path
}

func readCodexRPCs(t *testing.T, path string) []helperRequest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var requests []helperRequest
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var req helperRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, req)
	}
	return requests
}

func assertCodexSchemaRPCs(t *testing.T, path string, schema json.RawMessage, wantTurns int) {
	t.Helper()
	turns := 0
	for _, req := range readCodexRPCs(t, path) {
		if req.Method != "turn/start" {
			continue
		}
		var params struct {
			OutputSchema json.RawMessage `json:"outputSchema"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || !jsonEqual(params.OutputSchema, schema) {
			t.Fatalf("turn/start schema=%s, err=%v", params.OutputSchema, err)
		}
		turns++
	}
	if turns != wantTurns {
		t.Fatalf("turn/start count=%d, want %d", turns, wantTurns)
	}
}

func jsonEqual(left, right json.RawMessage) bool {
	var l, r any
	return json.Unmarshal(left, &l) == nil && json.Unmarshal(right, &r) == nil && reflect.DeepEqual(l, r)
}

func assertCodexNativeResponse(t *testing.T, result driver.Response, req driver.Request, turns int) {
	t.Helper()
	value := result.StructuredOutput
	if result.Output != `{"answer":42}` || result.Summary != "" || value == nil || value.Source != driver.StructuredOutputSourceNative || value.Format != driver.OutputFormatJSONSchema || !value.Valid || value.SchemaHash == "" || len(value.ValidationErrors) != 0 || string(value.RawJSON) != result.Output || !reflect.DeepEqual(value.Value, map[string]any{"answer": json.Number("42")}) {
		t.Fatalf("native result=%+v structured=%+v", result, value)
	}
	if result.Usage == nil || result.Usage.InputTokens != 2*turns || result.Usage.OutputTokens != turns || result.Metadata["transport"] != "app-server" || result.Model != "gpt-test" {
		t.Fatalf("accounting=%+v", result)
	}
	if result.RawStreams == nil || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != "turn/completed" || strings.Count(result.RawStreams.Stdout, `"method":"turn/completed"`) != turns || !strings.Contains(result.RawStreams.Stderr, "structured diagnostic") {
		t.Fatalf("raw=%+v", result.RawStreams)
	}
	if len(result.Transcript) < 3*turns || result.Transcript[len(result.Transcript)-2].Text != result.Output || result.Transcript[len(result.Transcript)-1].Kind != driver.TranscriptResult {
		t.Fatalf("transcript=%+v", result.Transcript)
	}
	terminal, err := json.Marshal(result.Transcript[len(result.Transcript)-1].Data["payload"])
	if err != nil || !jsonEqual(result.RawStreams.Terminal.JSON, terminal) {
		t.Fatalf("terminal differs from final transcript payload: raw=%s transcript=%s err=%v", result.RawStreams.Terminal.JSON, terminal, err)
	}
	if result.Checkpoint == nil || !result.Checkpoint.Valid || result.Checkpoint.State == nil || result.Checkpoint.State.ResumeID != "thread-1" || result.Checkpoint.State.Data[driver.SessionParamCWD] != req.Workspace.CWD || result.Checkpoint.State.Data[driver.SessionParamWorkspaceID] != req.Workspace.ID {
		t.Fatalf("checkpoint=%+v", result.Checkpoint)
	}
}
