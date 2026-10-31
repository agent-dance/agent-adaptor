package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func delegatedFixture(t *testing.T, kind string) string {
	t.Helper()
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	task := map[string]any{"id": "parent_task", "sessionID": "ses_child", "messageID": "message", "type": "tool", "tool": "task", "callID": "parent_call", "state": map[string]any{"status": "running", "input": map[string]any{"subagent_type": "worker"}, "metadata": map[string]any{"sessionId": "ses_worker"}}}
	frame := func(part map[string]any) string {
		return encode(map[string]any{"type": "message.part.updated", "properties": map[string]any{"sessionID": part["sessionID"], "part": part}})
	}
	start := frame(task)
	tool := "read"
	if kind == "question" {
		tool = "question"
	}
	if kind == "plan" {
		tool = "plan_exit"
	}
	part := map[string]any{"id": "tool", "sessionID": "ses_worker", "messageID": "message", "type": "tool", "tool": tool, "callID": "call", "state": map[string]any{"status": "running", "input": map[string]any{"path": "target"}}}
	toolStart := frame(part)
	props := map[string]any{"id": "request", "sessionID": "ses_worker", "tool": map[string]any{"messageID": "message", "callID": "call"}}
	eventType := "permission.asked"
	if kind == "question" || kind == "plan" {
		eventType = "question.asked"
		props["questions"] = []any{map[string]any{"question": "Choose", "options": []any{map[string]any{"label": "Yes", "description": "accept"}}}}
	} else {
		props["permission"] = "read"
	}
	decision := encode(map[string]any{"type": eventType, "properties": props})
	part["state"] = map[string]any{"status": "completed", "input": map[string]any{}, "output": "child tool result"}
	child := strings.ReplaceAll(serverHappy, "ses_child", "ses_worker")
	marker := `{"type":"message.part.updated","properties":{"sessionID":"ses_worker","part":{"id":"start"`
	child = strings.Replace(child, marker, toolStart+decision+frame(part)+marker, 1)
	task["state"] = map[string]any{"status": "completed", "input": map[string]any{}, "output": "child completed", "metadata": map[string]any{"sessionId": "ses_worker"}}
	rootMarker := `{"type":"message.part.updated","properties":{"sessionID":"ses_child","part":{"id":"start"`
	return strings.Replace(serverHappy, rootMarker, start+child+frame(task)+rootMarker, 1)
}

func TestDelegatedTreeLifecycle(t *testing.T) {
	for _, mode := range []string{"nested-error", "nested-success-incomplete", "parallel-reuse", "incomplete-step"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := strings.TrimPrefix(r.URL.Path, "/session/")
				parent := "root"
				if id == "grandchild" {
					parent = "child"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "parentID": parent})
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			sink := &testutil.EventRecorder{}
			p := newServerParser(sink, true)
			p.sessionID = "root"
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted})
			consume := func(raw string) error {
				_, _, err := p.consumeDelegated(context.Background(), client, []byte(raw))
				return err
			}
			message := func(id string) {
				t.Helper()
				if err := consume(fmt.Sprintf(`{"type":"message.updated","properties":{"sessionID":%q,"info":{"id":"message","sessionID":%q,"role":"assistant"}}}`, id, id)); err != nil {
					t.Fatal(err)
				}
			}
			task := func(parent, part, child, status string) error {
				return consume(fmt.Sprintf(`{"type":"message.part.updated","properties":{"sessionID":%q,"part":{"id":%q,"sessionID":%q,"messageID":"message","type":"tool","tool":"task","callID":%q,"state":{"status":%q,"input":{},"output":"done","error":"interrupted","metadata":{"sessionId":%q}}}}}`, parent, part, parent, part, status, child))
			}
			message("root")
			if err := task("root", "task1", "child", "running"); err != nil {
				t.Fatal(err)
			}
			if mode == "parallel-reuse" {
				if err := task("root", "task2", "sibling", "running"); err != nil {
					t.Fatal(err)
				}
				for index, id := range []string{"child", "sibling", "child"} {
					partID := "task1"
					if index == 1 {
						partID = "task2"
					}
					if index == 2 {
						partID = "task3"
						if err := task("root", partID, id, "running"); err != nil {
							t.Fatal(err)
						}
					}
					trace := strings.ReplaceAll(serverHappy, "ses_child", id)
					if index == 2 {
						trace = strings.ReplaceAll(trace, `"message"`, `"message2"`)
						trace = strings.ReplaceAll(trace, `"text"`, `"text2"`)
						trace = strings.ReplaceAll(trace, `"type":"text2"`, `"type":"text"`)
						trace = strings.ReplaceAll(trace, `"field":"text2"`, `"field":"text"`)
						trace = strings.ReplaceAll(trace, `"text2":`, `"text":`)
					}
					for _, line := range strings.Split(strings.TrimSpace(trace), "\n") {
						if err := consume(line); err != nil {
							t.Fatal(err)
						}
					}
					if err := task("root", partID, id, "completed"); err != nil {
						t.Fatal(err)
					}
				}
				if p.usage == nil || p.usage.InputTokens != 15 {
					t.Fatalf("parallel/resumed child usage=%+v", p.usage)
				}
			} else {
				message("child")
				if mode == "incomplete-step" {
					if err := consume(`{"type":"message.part.updated","properties":{"sessionID":"child","part":{"id":"step","sessionID":"child","messageID":"message","type":"step-start"}}}`); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := task("child", "nested", "grandchild", "running"); err != nil {
						t.Fatal(err)
					}
					message("grandchild")
					if err := consume(`{"type":"message.part.updated","properties":{"sessionID":"grandchild","part":{"id":"text","sessionID":"grandchild","messageID":"message","type":"text","text":"partial"}}}`); err != nil {
						t.Fatal(err)
					}
				}
				status := "completed"
				if mode == "nested-error" {
					status = "error"
				}
				err := task("root", "task1", "child", status)
				if (err != nil) != (mode != "nested-error") {
					t.Fatalf("child completion: %v", err)
				}
			}
			for id, child := range p.delegated {
				if child.active {
					t.Fatalf("task left active child %s", id)
				}
				_, event, err := p.consumeDelegated(context.Background(), client, []byte(fmt.Sprintf(`{"type":"permission.asked","properties":{"sessionID":%q,"id":"late","permission":"edit"}}`, id)))
				if err != nil || event.Type != "" {
					t.Fatal("late child approval was accepted")
				}
			}
			p.closeOpenParts()
			p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunFinished})
			for _, violation := range adaptertest.VerifyStreamSequence(sink.StreamSnapshot()) {
				t.Error(violation)
			}
			for _, violation := range adaptertest.VerifyStreamCapability((configuredDriver{}).StreamCapability(), sink.StreamSnapshot()) {
				t.Error(violation)
			}
		})
	}
}

type delegatedDecisionSink struct{ testutil.EventRecorder }

func (s *delegatedDecisionSink) RequestDecision(context.Context, driver.DecisionRequest) (driver.DecisionResponse, error) {
	return driver.DecisionResponse{Result: driver.DecisionApproved}, nil
}

func TestDelegatedResumeBeforeNativeApproval(t *testing.T) {
	for _, mode := range []string{"new", "resume", "foreign", "session-allow", "malformed", "uncorrelated"} {
		t.Run(mode, func(t *testing.T) {
			var replies atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/permission/") {
					replies.Add(1)
					_, _ = w.Write([]byte(`true`))
					return
				}
				parent := "root"
				if mode == "foreign" {
					parent = "unrelated"
				}
				action := "deny"
				if mode == "session-allow" {
					action = "allow"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "child", "parentID": parent, "permission": []permissionRule{{"edit", "*", action}}})
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			p := newServerParser(&delegatedDecisionSink{}, true)
			p.sessionID = "root"
			input := map[string]any{}
			if mode != "new" {
				input["task_id"] = "child"
			}
			if mode == "malformed" {
				input["task_id"] = 42
			}
			part := &partState{}
			part.part.CallID, part.part.MessageID, part.part.Tool, part.part.State.Input = "call", "message", "task", input
			if mode != "uncorrelated" {
				p.parts["task"] = part
			}
			req := driver.Request{}
			req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
			event := serverEvent{Type: "permission.asked", Properties: json.RawMessage(`{"id":"req","sessionID":"root","permission":"task","tool":{"messageID":"message","callID":"call"}}`)}
			err := p.decide(context.Background(), client, req, event)
			success := mode == "new" || mode == "resume"
			if (err == nil) != success || (replies.Load() == 1) != success {
				t.Fatalf("resume guard err=%v native replies=%d", err, replies.Load())
			}
			if err == nil && p.decide(context.Background(), client, req, event) == nil {
				t.Fatal("duplicate task approval accepted")
			}
		})
	}
}

func TestDelegatedApprovalPipeline(t *testing.T) {
	for _, mode := range []string{"approve", "question", "plan", "deny", "continue", "timeout", "timeout-continue", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, delegatedFixture(t, mode), "audit", 0)
			if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "session-info.json"), []byte(`{"ses_worker":{"id":"ses_worker","parentID":"ses_child"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			workspace := t.TempDir()
			a := adaptor.New(Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}}), adaptor.WithWorkspace(workspace))
			defer a.Close(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			policy := adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk}
			if strings.HasPrefix(mode, "timeout") {
				policy.Timeout = 15 * time.Millisecond
			}
			if mode == "timeout-continue" {
				policy.OnTimeout = adaptor.FallbackContinue
			}
			if mode == "continue" {
				policy.OnReject = adaptor.FallbackContinue
			}
			s := a.Stream(ctx, "delegate", adaptor.WithPolicy(adaptor.Policy{Approvals: policy}))
			seen, childText, terminals := 0, false, 0
			for event := range s.Events() {
				if e, ok := event.(adaptor.TextDelta); ok && e.Meta().Source != nil && e.Meta().Source.ThreadID == "ses_worker" {
					childText = true
				}
				if _, ok := event.(adaptor.RunFinished); ok {
					terminals++
				}
				if request, ok := event.(*adaptor.ApprovalRequest); ok {
					seen++
					if request.ToolCallID != childIdentifier("ses_worker", "call") {
						t.Fatalf("child correlation=%s", request.ToolCallID)
					}
					var err error
					switch mode {
					case "question":
						if request.Kind != adaptor.ApprovalQuestion {
							t.Fatal("wrong question kind")
						}
						err = request.Answer(ctx, "answer")
					case "plan":
						if request.Kind != adaptor.ApprovalPlanReview {
							t.Fatal("wrong plan kind")
						}
						err = request.Approve(ctx)
					case "deny", "continue":
						err = request.Deny(ctx, "denied")
					case "cancel":
						cancel()
					case "timeout", "timeout-continue":
					default:
						err = request.Approve(ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := s.Result()
			if seen != 1 || terminals != 1 {
				t.Fatalf("approvals=%d terminal runs=%d err=%v", seen, terminals, err)
			}
			switch mode {
			case "deny", "timeout":
				var failed *adaptor.RunError
				if !errors.As(err, &failed) {
					t.Fatalf("failure=%v", err)
				}
				want := adaptor.ReasonApprovalDenied
				if mode == "timeout" {
					want = adaptor.ReasonApprovalTimeout
				}
				if failed.Reason != want {
					t.Fatalf("reason=%s", failed.Reason)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel=%v", err)
				}
			default:
				if err != nil || result.Text != "OK" || !childText {
					t.Fatalf("child/root output: %+v %v childText=%v", result, err, childText)
				}
				if result.Usage == nil || result.Usage.InputTokens != 10 {
					t.Fatalf("delegated usage=%+v", result.Usage)
				}
				found := false
				for _, item := range result.Transcript() {
					if item.Kind == driver.TranscriptToolResult && item.SessionID == "ses_worker" && item.ToolUseID == childIdentifier("ses_worker", "call") {
						found = true
					}
				}
				if !found {
					t.Fatal("child transcript missing")
				}
			}
			_, statErr := os.Stat(filepath.Join(workspace, "executed"))
			if (statErr == nil) != (mode == "approve") {
				t.Fatalf("effect after child decision: %v", statErr)
			}
		})
	}
}

func TestDelegatedSessionAuthority(t *testing.T) {
	for _, mode := range []string{"foreign-parent", "root-cycle", "sibling-writer", "unknown-event"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				parent := "root"
				if mode == "foreign-parent" {
					parent = "foreign"
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "child", "parentID": parent})
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			p := newServerParser(&testutil.EventRecorder{}, true)
			p.sessionID = "root"
			if mode == "unknown-event" {
				_, event, err := p.consumeDelegated(context.Background(), client, []byte(`{"type":"permission.asked","properties":{"sessionID":"foreign","id":"req","permission":"edit"}}`))
				if err != nil || event.Type != "" {
					t.Fatal("unrelated approval entered this run")
				}
				return
			}
			part := &serverPart{}
			part.ID, part.CallID, part.State.Status = "task", "call", "running"
			id := "child"
			if mode == "root-cycle" {
				id = "root"
			}
			err := p.bindDelegated(context.Background(), client, p, part, id)
			if mode == "sibling-writer" {
				if err != nil {
					t.Fatal(err)
				}
				part.ID = "other"
				err = p.bindDelegated(context.Background(), client, p, part, id)
			}
			if err == nil {
				t.Fatal("invalid delegation authority accepted")
			}
		})
	}
}
