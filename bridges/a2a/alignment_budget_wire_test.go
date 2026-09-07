package a2a_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	a2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
	"github.com/agent-dance/agent-adaptor/driver"
)

// These fixtures exercise only public Runner -> HTTP -> existing Client APIs.
// Expected control objects are literal C02 oracles, never mapper output.
type budgetWireRunner struct {
	err     error
	events  []adaptor.Event
	runs    atomic.Int32
	streams atomic.Int32
}

func (r *budgetWireRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	r.runs.Add(1)
	return nil, errors.New("fixture Run must never be called")
}
func (r *budgetWireRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	r.streams.Add(1)
	events := make(chan adaptor.Event, len(r.events)+1)
	for _, event := range r.events {
		events <- event
	}
	events <- adaptor.RunFinished{Failed: true, Reason: adaptor.ReasonAgentError, Message: "finished-private-marker"}
	close(events)
	return &alignmentOutcomeStream{events: events, err: r.err}
}

type budgetWireCapture struct {
	base http.RoundTripper
	mu   sync.Mutex
	data bytes.Buffer
}

func (c *budgetWireCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := c.base.RoundTrip(req)
	if err == nil {
		res.Body = &budgetWireBody{ReadCloser: res.Body, capture: c}
	}
	return res, err
}

type budgetWireBody struct {
	io.ReadCloser
	capture *budgetWireCapture
}

func (b *budgetWireBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.capture.mu.Lock()
	b.capture.data.Write(p[:n])
	b.capture.mu.Unlock()
	return n, err
}
func (c *budgetWireCapture) text() string { c.mu.Lock(); defer c.mu.Unlock(); return c.data.String() }

type budgetWireOutcome struct {
	task   clienta2a.Task
	events []clienta2a.Event
	wire   string
}

func budgetWireRoundTrip(t *testing.T, err error, exposure a2a.ExposurePolicy, events []adaptor.Event, streaming bool) budgetWireOutcome {
	t.Helper()
	runner := &budgetWireRunner{err: err, events: events}
	mux := http.NewServeMux()
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	card := testCard()
	card.URL = httpServer.URL + "/rpc"
	card.Capabilities.Streaming = a2a.CapabilityEnabled
	server := a2a.NewServer(runner, a2a.ServerOptions{AgentCard: card, Exposure: exposure})
	mux.Handle("/rpc", server.Handler())
	mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
	capture := &budgetWireCapture{base: httpServer.Client().Transport}
	httpClient := *httpServer.Client()
	httpClient.Transport = capture
	client := clienta2a.New(clienta2a.Options{AgentCardURL: httpServer.URL, HTTPClient: &httpClient})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := clienta2a.SendRequest{ContextID: "budget-context", Message: clienta2a.Message{ID: "budget-message", Role: "user", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "safe prompt"}}}}
	var out budgetWireOutcome
	if !streaming {
		out.task, err = client.Send(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		stream, openErr := client.SendStream(ctx, req)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer stream.Close()
		finalCount, artifactAt, finalAt := 0, -1, -1
		taskID, contextID := "", ""
		for {
			event, recvErr := stream.RecvContext(ctx)
			if errors.Is(recvErr, io.EOF) {
				break
			}
			if recvErr != nil {
				t.Fatal(recvErr)
			}
			out.events = append(out.events, event)
			if event.TaskID != "" {
				if taskID != "" && taskID != event.TaskID {
					t.Fatal("task identity changed")
				}
				taskID = event.TaskID
			}
			if event.ContextID != "" {
				if contextID != "" && contextID != event.ContextID {
					t.Fatal("context identity changed")
				}
				contextID = event.ContextID
			}
			if event.RecoveredState {
				t.Fatal("expected live wire, got recovery")
			}
			if event.Artifact != nil {
				artifactAt = len(out.events) - 1
				if event.Append || !event.LastChunk {
					t.Fatal("terminal artifact update boundary changed")
				}
			}
			if event.Status != nil && event.Status.State.Terminal() {
				finalCount++
				finalAt = len(out.events) - 1
				out.task.Status = *event.Status
			}
		}
		if finalCount != 1 || finalAt != len(out.events)-1 {
			t.Fatalf("final count=%d order=%+v", finalCount, out.events)
		}
		if len(out.events) < 3 || out.events[0].Kind != clienta2a.EventTask || out.events[1].Status == nil || out.events[1].Status.State != clienta2a.TaskStateWorking {
			t.Fatalf("missing task/working prefix: %+v", out.events)
		}
		var re *adaptor.RunError
		if errors.As(runner.err, &re) && (artifactAt < 0 || artifactAt >= finalAt) {
			t.Fatalf("partial artifact must precede live terminal: %d/%d", artifactAt, finalAt)
		}
		stored, getErr := client.GetTask(ctx, clienta2a.GetTaskRequest{TaskID: taskID})
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored.ID != taskID || stored.ContextID != contextID || stored.Status.State != out.task.Status.State {
			t.Fatalf("GetTask/live mismatch: %+v", stored)
		}
		assertBudgetJSON(t, stored.Status.Message.Parts, out.task.Status.Message.Parts)
		out.task = stored
	}
	if runner.runs.Load() != 0 || runner.streams.Load() != 1 {
		t.Fatalf("dispatch Run=%d Stream=%d", runner.runs.Load(), runner.streams.Load())
	}
	out.wire = capture.text()
	return out
}

func assertBudgetJSON(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var av, bv any
	if err = json.Unmarshal(a, &av); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &bv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("got %s, want %s", a, b)
	}
}
func budgetControl(t *testing.T, task clienta2a.Task) (string, map[string]any) {
	t.Helper()
	msg := task.Status.Message
	if msg == nil || len(msg.Parts) != 1 || msg.Parts[0].Kind != clienta2a.PartText {
		t.Fatalf("failure must be one Text Part: %+v", msg)
	}
	if len(msg.Metadata) != 0 || len(task.Metadata) != 0 || msg.Parts[0].Data != nil {
		t.Fatal("failure control at wrong layer")
	}
	part := msg.Parts[0]
	if len(part.Metadata) == 0 {
		return part.Text, nil
	}
	if len(part.Metadata) != 1 {
		t.Fatalf("unexpected Part metadata: %+v", part.Metadata)
	}
	control, ok := part.Metadata["agentadaptor.failure"].(map[string]any)
	if !ok {
		t.Fatalf("failure control not object: %+v", part.Metadata)
	}
	for key := range control {
		if key != "code" && key != "limit_ms" && key != "metadata" {
			t.Fatalf("unrecognized control field %q", key)
		}
	}
	return part.Text, control
}
func budgetPartial(t *testing.T, observedUsage bool) *adaptor.Result {
	t.Helper()
	var usage *driver.Usage
	if observedUsage {
		usage = &driver.Usage{}
	}
	fake := &scriptedDriver{run: func(int, driver.Request, driver.EventSink) (driver.Response, error) {
		return driver.Response{
			Output: "safe assistant text", Summary: "safe partial summary", Usage: usage, Metadata: map[string]string{"note": "metadata-marker", "api_key": "result-secret-marker"},
			RawStreams: &driver.RawStreams{Stdout: "stdout-marker authorization=raw-secret-marker", Stderr: "stderr-marker", Terminal: &driver.TerminalPayload{Event: "terminal-marker", JSON: json.RawMessage(`0`)}},
			Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "transcript-marker"}},
		}, nil
	}}
	agent := adaptor.New(fake)
	defer agent.Close(context.Background())
	result, err := agent.Run(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if result.Raw().Terminal == nil || result.Raw().Stdout == "" || len(result.Transcript()) != 1 || (result.Usage != nil) != observedUsage {
		t.Fatal("incomplete public Result fixture")
	}
	return result
}
func budgetDetails() map[string]any {
	return map[string]any{"note": "detail-marker", "authorization": "Bearer detail-secret-marker", "nested": map[string]any{"api_key": "nested-secret-marker", "code": "cancelled", "limit_ms": 1}, "limit_ms": 999, "code": "cancelled"}
}

func TestAlignmentBudgetWirePrimaryReason(t *testing.T) {
	partial := budgetPartial(t, true)
	for _, test := range []struct {
		reason     adaptor.FailureReason
		code, text string
		state      clienta2a.TaskState
	}{
		{adaptor.ReasonActiveExecutionTimeout, "active_execution_timeout", "active execution budget exhausted", clienta2a.TaskStateFailed},
		{adaptor.ReasonApprovalDenied, "approval_denied", "approval denied", clienta2a.TaskStateFailed},
		{adaptor.ReasonApprovalTimeout, "approval_timeout", "approval timed out", clienta2a.TaskStateFailed},
		{adaptor.ReasonCancelled, "cancelled", "task cancelled", clienta2a.TaskStateCanceled},
		{adaptor.ReasonDeadlineExceeded, "deadline_exceeded", "execution deadline exceeded", clienta2a.TaskStateFailed},
		{adaptor.ReasonAgentError, "agent_error", "agent run failed", clienta2a.TaskStateFailed},
		{adaptor.ReasonPolicyViolation, "policy_violation", "execution policy violated", clienta2a.TaskStateFailed},
		{adaptor.ReasonInfrastructure, "infrastructure_error", "execution infrastructure failed", clienta2a.TaskStateFailed},
		{"private-extension-marker", "", "agent run failed", clienta2a.TaskStateFailed},
		{"decision_timeout", "", "agent run failed", clienta2a.TaskStateFailed},
		{"decision_rejected", "", "agent run failed", clienta2a.TaskStateFailed},
	} {
		for _, wrap := range []string{"direct", "wrapped", "join-first", "join-last"} {
			t.Run(test.code+string(test.reason)+"/"+wrap, func(t *testing.T) {
				re := &adaptor.RunError{Reason: test.reason, Message: "provider-secret-marker", Details: budgetDetails(), Result: partial, Cause: errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}, &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, context.Canceled, context.DeadlineExceeded, errors.New("cause-secret-marker"))}
				var err error = re
				switch wrap {
				case "wrapped":
					err = fmt.Errorf("wrapper-secret-marker: %w", err)
				case "join-first":
					err = errors.Join(err, context.Canceled)
				case "join-last":
					err = errors.Join(context.DeadlineExceeded, err)
				}
				expected := map[string]any(nil)
				if test.code != "" {
					expected = map[string]any{"code": test.code}
				}
				if test.code == "active_execution_timeout" {
					expected["limit_ms"] = 100
				}
				for _, streaming := range []bool{false, true} {
					out := budgetWireRoundTrip(t, err, a2a.ExposurePolicy{}, nil, streaming)
					text, control := budgetControl(t, out.task)
					if out.task.Status.State != test.state || text != test.text {
						t.Fatalf("state/text=%s/%q", out.task.Status.State, text)
					}
					assertBudgetJSON(t, control, expected)
					if strings.Contains(out.wire, "secret-marker") || strings.Contains(out.wire, "private-extension-marker") || strings.Contains(out.wire, "detail-marker") || strings.Contains(out.wire, "stdout-marker") {
						t.Fatal("default wire leaked private error/diagnostics")
					}
				}
			})
		}
	}
}

func TestAlignmentBudgetWireBareAndLimitBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		control map[string]any
		state   clienta2a.TaskState
	}{
		{"wrapped-active", errors.Join(fmt.Errorf("private: %w", &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}), context.Canceled), map[string]any{"code": "active_execution_timeout", "limit_ms": 100}, clienta2a.TaskStateFailed},
		{"active-sentinel", adaptor.ErrActiveExecutionTimeout, map[string]any{"code": "active_execution_timeout"}, clienta2a.TaskStateFailed},
		{"cancel", fmt.Errorf("private: %w", context.Canceled), map[string]any{"code": "cancelled"}, clienta2a.TaskStateCanceled},
		{"deadline", fmt.Errorf("private: %w", context.DeadlineExceeded), map[string]any{"code": "deadline_exceeded"}, clienta2a.TaskStateFailed},
		{"unknown", errors.New("private"), nil, clienta2a.TaskStateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				out := budgetWireRoundTrip(t, test.err, a2a.ExposurePolicy{}, nil, streaming)
				_, control := budgetControl(t, out.task)
				assertBudgetJSON(t, control, test.control)
				if out.task.Status.State != test.state || len(out.task.Artifacts) != 0 || strings.Contains(out.wire, "private") {
					t.Fatalf("bare error created fake partial or leaked: %+v", out.task)
				}
			}
		})
	}
	for _, test := range []struct {
		ns time.Duration
		ms int64
	}{{1, 1}, {999999, 1}, {1000000, 1}, {1000001, 2}, {100000001, 101}, {math.MaxInt64, 9223372036855}, {0, 0}, {-1, 0}} {
		t.Run(fmt.Sprint(test.ns), func(t *testing.T) {
			err := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Cause: &adaptor.ActiveExecutionTimeoutError{Limit: test.ns}, Details: budgetDetails()}
			out := budgetWireRoundTrip(t, err, a2a.ExposurePolicy{}, nil, true)
			_, control := budgetControl(t, out.task)
			expected := map[string]any{"code": "active_execution_timeout"}
			if test.ms > 0 {
				expected["limit_ms"] = test.ms
			}
			assertBudgetJSON(t, control, expected)
		})
	}
	t.Run("carrier-without-typed-limit", func(t *testing.T) {
		err := errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}, &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Details: budgetDetails()})
		out := budgetWireRoundTrip(t, err, a2a.ExposurePolicy{}, nil, true)
		_, control := budgetControl(t, out.task)
		assertBudgetJSON(t, control, map[string]any{"code": "active_execution_timeout"})
	})
}

func TestAlignmentBudgetWireExposureAndPartialArtifacts(t *testing.T) {
	partial := budgetPartial(t, true)
	textEvent := adaptor.TextDelta{Text: "live text retained", MessageID: "assistant", Phase: adaptor.PhaseContent}
	all := a2a.ExposurePolicy{IncludeReasoning: true, IncludeToolCalls: true, IncludeHITL: true, IncludeCapabilityInvocations: true, IncludeTodos: true, Diagnostics: a2a.DiagnosticsPolicy{IncludeMetadata: true, IncludeUsage: true, IncludeRawStreams: true, IncludeTranscript: true, IncludeProviderResult: true, IncludeHITLPayloads: true, IncludeHITLRaw: true}}
	for _, test := range []struct {
		name     string
		exposure a2a.ExposurePolicy
	}{
		{"default", a2a.ExposurePolicy{}}, {"metadata", a2a.ExposurePolicy{Diagnostics: a2a.DiagnosticsPolicy{IncludeMetadata: true}}},
		{"raw", a2a.ExposurePolicy{Diagnostics: a2a.DiagnosticsPolicy{IncludeRawStreams: true}}}, {"usage", a2a.ExposurePolicy{Diagnostics: a2a.DiagnosticsPolicy{IncludeUsage: true}}},
		{"transcript", a2a.ExposurePolicy{Diagnostics: a2a.DiagnosticsPolicy{IncludeTranscript: true}}}, {"terminal", a2a.ExposurePolicy{Diagnostics: a2a.DiagnosticsPolicy{IncludeProviderResult: true}}},
		{"capability", a2a.ExposurePolicy{IncludeCapabilityInvocations: true}}, {"todo", a2a.ExposurePolicy{IncludeTodos: true}},
		{"tool", a2a.ExposurePolicy{IncludeToolCalls: true}}, {"hitl", a2a.ExposurePolicy{IncludeHITL: true}}, {"all", all},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Message: "provider-secret-marker", Cause: errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}, errors.New("cause-secret-marker")), Details: budgetDetails(), Result: partial}
			var prior []clienta2a.Artifact
			for _, streaming := range []bool{false, true} {
				out := budgetWireRoundTrip(t, err, test.exposure, []adaptor.Event{textEvent}, streaming)
				text, control := budgetControl(t, out.task)
				if text != "active execution budget exhausted" {
					t.Fatal(text)
				}
				expected := map[string]any{"code": "active_execution_timeout", "limit_ms": 100}
				if test.exposure.Diagnostics.IncludeMetadata {
					expected["metadata"] = map[string]any{"note": "detail-marker", "authorization": "[REDACTED]", "nested": map[string]any{"api_key": "[REDACTED]", "code": "cancelled", "limit_ms": 1}, "limit_ms": 999, "code": "cancelled"}
				}
				assertBudgetJSON(t, control, expected)
				if len(out.task.Artifacts) != 1 || len(out.task.Artifacts[0].Parts) != 1 {
					t.Fatalf("partial artifacts=%+v", out.task.Artifacts)
				}
				data, ok := out.task.Artifacts[0].Parts[0].Data.(map[string]any)
				if !ok {
					t.Fatal("partial is not Data Part")
				}
				if data["summary"] != "safe partial summary" {
					t.Fatalf("lost summary: %+v", data)
				}
				for key, want := range map[string]bool{"metadata": test.exposure.Diagnostics.IncludeMetadata, "usage": test.exposure.Diagnostics.IncludeUsage, "raw_streams": test.exposure.Diagnostics.IncludeRawStreams, "transcript": test.exposure.Diagnostics.IncludeTranscript, "provider_result": test.exposure.Diagnostics.IncludeProviderResult} {
					_, got := data[key]
					if got != want {
						t.Fatalf("%s present=%v want=%v", key, got, want)
					}
				}
				if test.exposure.Diagnostics.IncludeProviderResult {
					assertBudgetJSON(t, data["provider_result"], map[string]any{"event": "terminal-marker", "payload": 0})
				}
				if test.exposure.Diagnostics.IncludeUsage {
					assertBudgetJSON(t, data["usage"], adaptor.Usage{})
				}
				if strings.Contains(out.wire, "secret-marker") || strings.Contains(out.wire, "finished-private-marker") {
					t.Fatal("private error or sensitive value leaked")
				}
				if streaming {
					assertBudgetJSON(t, out.task.Artifacts, prior)
					seenText := false
					for _, event := range out.events {
						if event.Status != nil && event.Status.Message != nil {
							for _, part := range event.Status.Message.Parts {
								if part.Kind == clienta2a.PartData {
									encoded, _ := json.Marshal(part.Data)
									seenText = seenText || strings.Contains(string(encoded), "live text retained")
								}
							}
						}
					}
					if !seenText {
						t.Fatal("lost previously emitted text")
					}
				} else {
					prior = out.task.Artifacts
				}
			}
		})
	}
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonCancelled, adaptor.ReasonApprovalDenied, adaptor.ReasonInfrastructure} {
		for _, exposure := range []a2a.ExposurePolicy{{}, all} {
			t.Run(string(reason)+fmt.Sprint(exposure.Diagnostics.IncludeUsage), func(t *testing.T) {
				err := &adaptor.RunError{Reason: reason, Result: partial, Cause: &adaptor.ActiveExecutionTimeoutError{Limit: time.Second}}
				out := budgetWireRoundTrip(t, err, exposure, []adaptor.Event{textEvent}, true)
				_, control := budgetControl(t, out.task)
				if _, ok := control["limit_ms"]; ok {
					t.Fatal("secondary active limit promoted")
				}
				if len(out.task.Artifacts) != 1 {
					t.Fatal("lost partial")
				}
			})
		}
	}
	t.Run("unobserved-usage", func(t *testing.T) {
		out := budgetWireRoundTrip(t, &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Result: budgetPartial(t, false)}, all, nil, true)
		data := out.task.Artifacts[0].Parts[0].Data.(map[string]any)
		if _, ok := data["usage"]; ok {
			t.Fatal("fabricated observed usage")
		}
	})
}

func TestAlignmentBudgetWireKeepsObservationFixtures(t *testing.T) {
	var events []adaptor.Event
	for _, name := range []string{"capability-start", "capability-terminal-zero-duration", "todo-clear", "tool-parent"} {
		raw, err := os.ReadFile("testdata/alignment/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, matched, err := a2a.DecodeAdapterEventV1(json.RawMessage(raw))
		if err != nil || !matched {
			t.Fatalf("fixture: %v", err)
		}
		events = append(events, event)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			exposure := a2a.ExposurePolicy{IncludeCapabilityInvocations: enabled, IncludeTodos: enabled, IncludeToolCalls: enabled, Diagnostics: a2a.DiagnosticsPolicy{IncludeMetadata: enabled}}
			err := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Cause: &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}}
			out := budgetWireRoundTrip(t, err, exposure, events, true)
			var got []adaptor.Event
			for _, event := range out.events {
				if event.Status == nil || event.Status.State.Terminal() || event.Status.Message == nil {
					continue
				}
				for _, part := range event.Status.Message.Parts {
					if _, ok := part.Metadata["agentadaptor.failure"]; ok {
						t.Fatal("budget contaminated intermediate Data Part")
					}
					if part.Kind != clienta2a.PartData {
						continue
					}
					decoded, matched, err := a2a.DecodeAdapterEventV1(part.Data)
					if err != nil || !matched {
						t.Fatalf("observation wire: %v", err)
					}
					got = append(got, decoded)
				}
			}
			if enabled {
				if !reflect.DeepEqual(got, events) {
					t.Fatalf("observation roundtrip changed: got %#v want %#v", got, events)
				}
			} else if len(got) != 0 {
				t.Fatal("opt-out exposed facts")
			}
		})
	}
}

func TestAlignmentBudgetWireCapturedHTTP(t *testing.T) {
	// Preserve actual binding spelling and Text Part metadata alongside the
	// independently written C02 oracle in the final JSON test evidence.
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			err := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Result: &adaptor.Result{Summary: "safe partial summary"}, Cause: &adaptor.ActiveExecutionTimeoutError{Limit: 100 * time.Millisecond}}
			out := budgetWireRoundTrip(t, err, a2a.ExposurePolicy{}, nil, streaming)
			_, control := budgetControl(t, out.task)
			assertBudgetJSON(t, control, map[string]any{"code": "active_execution_timeout", "limit_ms": 100})
			t.Logf("actual local HTTP response bodies (streaming=%v):\n%s", streaming, out.wire)
		})
	}
}

func TestAlignmentBudgetWireInvalidFirstLimitDoesNotBorrowParent(t *testing.T) {
	for _, limit := range []time.Duration{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			err := &adaptor.RunError{Reason: adaptor.ReasonActiveExecutionTimeout, Cause: errors.Join(&adaptor.ActiveExecutionTimeoutError{Limit: limit}, &adaptor.ActiveExecutionTimeoutError{Limit: 777 * time.Second}), Details: budgetDetails()}
			out := budgetWireRoundTrip(t, err, a2a.ExposurePolicy{}, nil, true)
			_, control := budgetControl(t, out.task)
			assertBudgetJSON(t, control, map[string]any{"code": "active_execution_timeout"})
		})
	}
}

func TestAlignmentBudgetWireResultBuilderErrorUsesSafeText(t *testing.T) {
	runner := &scriptedDriver{}
	server := a2a.NewServer(adaptor.New(runner), a2a.ServerOptions{AgentCard: testCard(), ResultBuilder: func(context.Context, a2a.InboundRequest, *adaptor.Result) (a2a.BuiltResult, error) {
		return a2a.BuiltResult{}, errors.New("private-builder-marker")
	}})
	response := postRPC(t, server.Handler(), `{"jsonrpc":"2.0","id":"1","method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"fixture"}]}}}`)
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-builder-marker") || strings.Contains(string(raw), `"layer"`) || !strings.Contains(string(raw), "execution infrastructure failed") || !strings.Contains(string(raw), `"code":"infrastructure_error"`) {
		t.Fatalf("unsafe builder failure: %s", raw)
	}
}
