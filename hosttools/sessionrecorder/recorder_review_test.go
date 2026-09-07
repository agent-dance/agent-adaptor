package sessionrecorder_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/hosttools/sessionrecorder"
	"github.com/agent-dance/agent-adaptor/todo"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func reviewerObservation() adaptor.TodoUpdated {
	items := make([]todo.Item, 18)
	for i := range items {
		items[i] = todo.Item{ID: fmt.Sprint(i), Content: strings.Repeat("汉", 1364) + "\n\txy", Status: todo.Pending, SyntheticID: i%2 == 0}
	}
	return adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: items, Source: todo.PlanUpdate, Revision: math.MaxUint64, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ScopeID: "child", ParentScopeID: "parent", ParentToolCallID: "p"}}
}

func TestReviewerJSONLLocalRecordAndReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	recorder := sessionrecorder.NewEventRecorder(backend)
	ev := adaptor.WithEventMeta(reviewerObservation(), adaptor.EventMeta{RunID: "one", Sequence: math.MaxUint64, Time: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ThreadKey: "opaque\n/key", TurnID: "t", Source: &adaptor.EventSourceMeta{RunID: "relay", Sequence: math.MaxUint64, ScopeID: "s", ToolCallID: "tool", InvocationID: "inv", DelegationID: "del", Upstream: &adaptor.EventSourceMeta{RunID: "origin", Sequence: 7}}})
	first, err := recorder.Record(ctx, "session", ev)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	capEvent := adaptor.CapabilityInvocation{Invocation: capability.Invocation{InvocationID: "inv", Ref: capability.Ref{Kind: capability.MCP, Key: "host-tool", Operation: "search"}, Phase: capability.Completed, Source: capability.Provider, Evidence: capability.ProviderProtocol, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Duration: &zero}}
	second, err := recorder.Record(ctx, "session", adaptor.WithEventMeta(capEvent, adaptor.EventMeta{RunID: "two", Sequence: 1}))
	if err != nil {
		t.Fatal(err)
	}
	clear := adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 2, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}}
	third, err := recorder.Record(ctx, "session", clear)
	if err != nil {
		t.Fatal(err)
	}
	if first.HostSeq != 1 || second.HostSeq != 2 || third.HostSeq != 3 {
		t.Fatal("HostSeq is not host history")
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.Split(raw, []byte{'\n'})[0]) <= 65536 {
		t.Fatal("fixture must exercise >64KiB legal line")
	}
	t.Logf("first JSONL payload=%d bytes", len(bytes.Split(raw, []byte{'\n'})[0]))
	reopened, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.Load(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || !reflect.DeepEqual(rows[0], first) || !reflect.DeepEqual(rows[1], second) || !reflect.DeepEqual(rows[2], third) {
		t.Fatalf("durable replay differs: %#v", rows)
	}
	if rows[2].Event.(adaptor.TodoUpdated).Snapshot.Items == nil {
		t.Fatal("explicit clear became null")
	}
	rows[0].Event.(adaptor.TodoUpdated).Snapshot.Items[0].Content = "mutated query"
	again, err := reopened.Load(ctx, "session")
	if err != nil || again[0].Event.(adaptor.TodoUpdated).Snapshot.Items[0].Content == "mutated query" {
		t.Fatal("replay aliases history")
	}
	max := first
	max.HostSeq = sessionrecorder.HostSeq(math.MaxUint64)
	data, err := json.Marshal(max)
	if err != nil {
		t.Fatal(err)
	}
	var decoded sessionrecorder.EventRecord
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.HostSeq != max.HostSeq || decoded.Event.Meta().Sequence != math.MaxUint64 {
		t.Fatalf("full uint64 envelope failed: %v", err)
	}
}

func TestReviewerJSONLCorruptionReturnsError(t *testing.T) {
	rec := sessionrecorder.EventRecord{HostSeq: 1, RecordedAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), Event: adaptor.TodoUpdated{Snapshot: todo.Snapshot{Items: []todo.Item{}, Source: todo.PlanUpdate, Revision: 1, OccurredAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)}}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	base := string(b)
	cases := map[string]string{
		"unknown-kind":         strings.Replace(base, "todo.updated", "future.kind", 1),
		"null-event":           strings.Replace(base, `"event":{"Snapshot":{"Items":[],"Source":"plan_update","ScopeID":"","ParentScopeID":"","ParentToolCallID":"","Revision":1,"OccurredAt":"2026-09-07T00:00:00Z"}}`, `"event":null`, 1),
		"unknown-envelope":     strings.Replace(base, `"host_seq":1`, `"bogus":{},"host_seq":1`, 1),
		"unknown-payload":      strings.Replace(base, `"Snapshot":{`, `"Snapshot":{"Bogus":1,`, 1),
		"null-items":           strings.Replace(base, `"Items":[]`, `"Items":null`, 1),
		"missing-revision":     strings.Replace(base, `"Revision":1,`, "", 1),
		"zero-revision":        strings.Replace(base, `"Revision":1`, `"Revision":0`, 1),
		"zero-host-seq":        strings.Replace(base, `"host_seq":1`, `"host_seq":0`, 1),
		"host-seq-gap":         strings.Replace(base, `"host_seq":1`, `"host_seq":2`, 1),
		"bad-timestamp":        strings.Replace(base, `"OccurredAt":"2026-09-07T00:00:00Z"`, `"OccurredAt":"2026-09-07T00:00:00+08:00"`, 1),
		"two-values":           base + ` {}`,
		"truncated-final-line": base,
	}
	// Use a structured envelope edit for null to avoid depending on typed field omission.
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	fields["event"] = json.RawMessage("null")
	nb, _ := json.Marshal(fields)
	cases["null-event"] = string(nb)
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if raw == base && name != "truncated-final-line" {
				t.Fatal("mutation did not apply")
			}
			dir := t.TempDir()
			if name != "truncated-final-line" {
				raw += "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "x.jsonl"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			backend, err := sessionrecorder.NewJSONLEventBackend(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			rows, err := backend.Load(context.Background(), "x")
			if !errors.Is(err, sessionrecorder.ErrJSONLEventLogCorrupt) || rows != nil {
				t.Fatalf("corrupt log returned rows=%#v err=%v", rows, err)
			}
		})
	}
}

type reviewerApprovalDriver struct{ kind driver.HumanDecisionKind }

func (d reviewerApprovalDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "reviewer-fake", RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true}, PlanReview: driver.HumanDecisionSupport{Ask: true}, Question: driver.QuestionSupport{Ask: true}}}
}
func (d reviewerApprovalDriver) ValidateConfig(any) error { return nil }
func (d reviewerApprovalDriver) Run(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
	response, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: d.kind, Prompt: "real pending request", Payload: map[string]any{"nested": map[string]any{"value": "original"}}})
	return driver.Response{Output: string(response.Result)}, err
}
func TestReviewerRecordedLiveApprovalHasNoResponder(t *testing.T) {
	for _, kind := range []driver.HumanDecisionKind{driver.HumanDecisionPermission, driver.HumanDecisionPlanReview, driver.HumanDecisionQuestion} {
		t.Run(string(kind), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			backend, err := sessionrecorder.NewJSONLEventBackend(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			rec := sessionrecorder.NewEventRecorder(backend)
			defer rec.Close()
			agent := adaptor.New(reviewerApprovalDriver{kind: kind}, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk}}))
			defer agent.Close(context.Background())
			s := agent.Stream(ctx, "fixture only")
			defer s.Cancel()
			seen := false
			for ev := range s.Events() {
				req, ok := ev.(*adaptor.ApprovalRequest)
				if !ok {
					continue
				}
				seen = true
				saved, err := rec.Record(ctx, "approval", req)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := backend.Load(ctx, "approval")
				if err != nil {
					t.Fatal(err)
				}
				for _, copy := range []*adaptor.ApprovalRequest{saved.Event.(*adaptor.ApprovalRequest), rows[0].Event.(*adaptor.ApprovalRequest)} {
					for _, answer := range []func() error{func() error { return copy.Approve(ctx) }, func() error { return copy.Deny(ctx, "no") }, func() error { return copy.Answer(ctx, "yes") }} {
						if err := answer(); !errors.Is(err, adaptor.ErrApprovalUnavailable) {
							t.Fatalf("history retained responder: %v", err)
						}
					}
					copy.Details["nested"].(map[string]any)["value"] = "changed"
				}
				if req.Details["nested"].(map[string]any)["value"] != "original" {
					t.Error("copied request aliases live")
				}
				if kind == driver.HumanDecisionQuestion {
					err = req.Answer(ctx, "yes")
				} else {
					err = req.Approve(ctx)
				}
				if err != nil {
					t.Fatalf("recording changed live responder: %v", err)
				}
			}
			if !seen {
				t.Fatal("no real pending request")
			}
			r, err := s.Result()
			if err != nil || r == nil {
				t.Fatalf("actual DecisionSink round trip failed: %v", err)
			}
			t.Logf("actual DecisionSink round trip completed: result=%q", r.Text)
		})
	}
}

func TestReviewerPersistenceFailureDoesNotCache(t *testing.T) {
	dir := t.TempDir()
	backend, err := sessionrecorder.NewJSONLEventBackend(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := sessionrecorder.NewEventRecorder(backend)
	defer rec.Close()
	if err := os.Mkdir(filepath.Join(dir, "blocked.jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Record(context.Background(), "blocked", adaptor.Notice{Text: "must not cache"}); err == nil {
		t.Fatal("persistent failure became successful record")
	}
	if err := os.Remove(filepath.Join(dir, "blocked.jsonl")); err != nil {
		t.Fatal(err)
	}
	saved, err := rec.Record(context.Background(), "blocked", adaptor.Notice{Text: "retry"})
	if err != nil || saved.HostSeq != 1 {
		t.Fatalf("failed persistent record advanced sequence: %d %v", saved.HostSeq, err)
	}
	rows, err := rec.Since(context.Background(), "blocked", 0)
	if err != nil || len(rows) != 1 || rows[0].Event.(adaptor.Notice).Text != "retry" {
		t.Fatal("failed record leaked into memory")
	}
}

func TestReviewerRecorderDescriptiveSnapshots(t *testing.T) {
	for _, storage := range []string{"memory", "jsonl"} {
		t.Run(storage, func(t *testing.T) {
			var backend sessionrecorder.EventBackend = sessionrecorder.NewMemoryEventBackend()
			if storage == "jsonl" {
				b, err := sessionrecorder.NewJSONLEventBackend(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				backend = b
			}
			r := sessionrecorder.NewEventRecorder(backend)
			defer r.Close()
			ctx := context.Background()
			input := &adaptor.ApprovalRequest{ID: "historical", Kind: adaptor.ApprovalQuestion, Choices: []adaptor.Choice{{Key: "key", Label: "Original"}}, Details: map[string]any{"nested": []any{"Original"}}}
			saved, err := r.Record(ctx, "approval", input)
			if err != nil {
				t.Fatal(err)
			}
			copy := saved.Event.(*adaptor.ApprovalRequest)
			copy.Choices[0].Label = "RETURN_CHANGED"
			copy.Details["nested"].([]any)[0] = "RETURN_CHANGED"
			if input.Choices[0].Label != "Original" || input.Details["nested"].([]any)[0] != "Original" {
				t.Error("returned record changed caller input")
			}
			rows, err := r.Since(ctx, "approval", 0)
			if err != nil {
				t.Fatal(err)
			}
			queried := rows[0].Event.(*adaptor.ApprovalRequest)
			if queried.Choices[0].Label != "Original" || queried.Details["nested"].([]any)[0] != "Original" {
				t.Error("returned record changed recorder history")
			}
			queried.Choices[0].Label = "QUERY_CHANGED"
			queried.Details["nested"].([]any)[0] = "QUERY_CHANGED"
			again, err := r.Since(ctx, "approval", 0)
			if err != nil {
				t.Fatal(err)
			}
			againReq := again[0].Event.(*adaptor.ApprovalRequest)
			if againReq.Choices[0].Label != "Original" || againReq.Details["nested"].([]any)[0] != "Original" {
				t.Error("Since result changed future history")
			}
			disk, err := backend.Load(ctx, "approval")
			if err != nil {
				t.Fatal(err)
			}
			stored := disk[0].Event.(*adaptor.ApprovalRequest)
			if stored.Choices[0].Label != "Original" || stored.Details["nested"].([]any)[0] != "Original" {
				t.Error("returned record changed backend history")
			}
		})
	}
}
