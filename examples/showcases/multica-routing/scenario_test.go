package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

type observedFixtureStream struct {
	events chan adaptor.Event
	result *adaptor.Result
	err    error
}

func TestFinishRejectsWorkerFailureAfterArtifactDespiteHealthyLeader(t *testing.T) {
	for _, state := range []team.TaskState{team.Failed, team.Interrupted, team.Cancelled} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "restart.txt"), []byte("NEW_GOAL"), 0600); err != nil {
				t.Fatal(err)
			}
			store := team.NewMemoryStore()
			lease, err := store.AcquireScope(ctx, "offline-restart", "offline-fixture", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			partial := &adaptor.Result{RunID: "worker-run", Text: `{"status":"done","detail":"wrote file before failing"}`}
			storeTerminalFixture(t, store, lease, team.Task{Scope: lease.Scope, ID: "beta-task", Member: "beta", State: state, Result: partial, Reason: "fixture_failure_after_write"})
			leader := &adaptor.Result{RunID: "healthy-team", Text: `{"status":"done","detail":"leader claims complete"}`}
			completed := &runRecord{Name: "offline-restart", Scope: lease.Scope, ActualDone: true, DoneMatchesResult: true, result: leader}
			o := &observed{record: &runRecord{}, completed: completed, done: make(chan struct{})}
			close(o.done)
			f := &fixture{cfg: settings{output: t.TempDir()}, report: &report{Mode: "offline-component"}, store: store, workspaces: map[string]string{"beta": workspace, "alpha": t.TempDir()}}
			f.finish(ctx, o, false)
			if !f.fileEquals("beta", "restart.txt", "NEW_GOAL") {
				t.Fatal("regression requires the misleading success artifact")
			}
			rejected := false
			for _, c := range f.report.Checks {
				if strings.HasSuffix(c.Name, "worker beta succeeded") && !c.Passed {
					rejected = true
				}
			}
			if !rejected {
				t.Fatalf("healthy leader hid %s worker: %+v", state, f.report.Checks)
			}
			if validateRestartTasks(o.record.Tasks, nil, false) == nil {
				t.Fatal("restart accepted the failed worker")
			}
			if o.record.Tasks[0].Result == nil {
				t.Fatal("rejected result lost its partial audit")
			}
		})
	}
}

func TestRestartTaskSetRejectsExtraAndReplacementDelegation(t *testing.T) {
	baseline := []team.Task{{Scope: "new-scope", ID: "original-beta", Member: "beta", State: team.Succeeded, Objective: "NEW_GOAL", Conversation: "beta-conversation"}}
	if err := validateRestartTasks(baseline, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := validateRestartTasks(baseline, baseline, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tasks []team.Task
	}{
		{"extra-alpha", append(append([]team.Task(nil), baseline...), team.Task{ID: "extra", Member: "alpha", State: team.Succeeded})},
		{"extra-beta", append(append([]team.Task(nil), baseline...), team.Task{ID: "extra", Member: "beta", State: team.Succeeded})},
		{"replaced-beta", []team.Task{{Scope: "new-scope", ID: "new-beta", Member: "beta", State: team.Succeeded, Objective: "NEW_GOAL", Conversation: "beta-conversation"}}},
		{"wrong-member", []team.Task{{Scope: "new-scope", ID: "original-beta", Member: "alpha", State: team.Succeeded}}},
		{"missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if validateRestartTasks(tc.tasks, baseline, true) == nil {
				t.Fatalf("unexpected task set accepted: %+v", tc.tasks)
			}
		})
	}
}

func TestExpectedCancellationCannotLoseObservedWorkerAudit(t *testing.T) {
	ctx := context.Background()
	store := team.NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "offline-cancel", "offline-fixture", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	partial := &adaptor.Result{RunID: "cancelled-worker", Text: "partial without observed protocol"}
	storeTerminalFixture(t, store, lease, team.Task{Scope: lease.Scope, ID: "beta-task", Member: "beta", State: team.Cancelled, Result: partial})
	completed := &runRecord{Name: "offline-cancel", Scope: lease.Scope, ActualDone: true, DoneMatchesResult: true, result: partial, err: context.Canceled}
	o := &observed{record: &runRecord{}, completed: completed, done: make(chan struct{})}
	close(o.done)
	f := &fixture{cfg: settings{output: t.TempDir()}, report: &report{Mode: "offline-component"}, store: store, workspaces: map[string]string{"alpha": t.TempDir(), "beta": t.TempDir()}}
	f.finish(ctx, o, true)
	rejected := false
	for _, c := range f.report.Checks {
		if strings.HasSuffix(c.Name, "worker beta cancellation audit retained") && !c.Passed {
			rejected = true
		}
	}
	if !rejected {
		t.Fatal("cancelled Result with lost stdout/transcript was accepted")
	}
}

func storeTerminalFixture(t *testing.T, store *team.MemoryStore, lease team.ScopeLease, task team.Task) {
	t.Helper()
	state := task.State
	task.State = team.Queued
	saved, err := store.SaveTask(context.Background(), lease, task)
	if err != nil {
		t.Fatal(err)
	}
	saved.State = state
	if _, err = store.SaveTask(context.Background(), lease, saved); err != nil {
		t.Fatal(err)
	}
}

func (s *observedFixtureStream) Send(context.Context, string, ...adaptor.MessageOption) error {
	return nil
}
func (s *observedFixtureStream) Events() <-chan adaptor.Event     { return s.events }
func (s *observedFixtureStream) Result() (*adaptor.Result, error) { return s.result, s.err }
func (s *observedFixtureStream) RunID() string                    { return "offline-observer" }
func (s *observedFixtureStream) Cancel()                          {}

func TestObserverRequiresCorrelatedToolResultAndAuthoritativeDone(t *testing.T) {
	result := &adaptor.Result{RunID: "offline-observer", Text: `{"status":"done","detail":"fixture"}`}
	s := &observedFixtureStream{events: make(chan adaptor.Event), result: result}
	record := &runRecord{Name: "offline-only", RunID: s.RunID()}
	o := observeStream(s, record)
	s.events <- adaptor.ToolCall{ID: "forward", Name: "team/team_send", Args: map[string]any{"text": commentMarker}}
	s.events <- adaptor.ToolResult{ID: "unrelated"}
	select {
	case <-o.forwarded:
		t.Fatal("unrelated result released host barrier")
	default:
	}
	s.events <- adaptor.ToolResult{ID: "forward"}
	select {
	case <-o.forwarded:
	case <-time.After(time.Second):
		t.Fatal("correlated result not observed")
	}
	if record.ActualDone || len(record.Events) != 0 {
		t.Fatal("collector mutated report before completion")
	}
	s.events <- adaptor.Done{Result: result}
	close(s.events)
	<-o.done
	if !o.completed.ActualDone || !o.completed.DoneMatchesResult || o.completed.result != result || len(o.completed.Events) != 4 {
		t.Fatalf("bad terminal audit: %+v", o.completed)
	}
}

func TestObserverKeepsFailedPartialAndMismatch(t *testing.T) {
	partial := &adaptor.Result{RunID: "failed", Text: "partial result"}
	failure := &adaptor.RunError{Reason: adaptor.ReasonAgentError, Result: partial}
	s := &observedFixtureStream{events: make(chan adaptor.Event, 1), err: failure}
	s.events <- adaptor.Done{Err: failure}
	close(s.events)
	o := observeStream(s, &runRecord{Name: "failed-offline"})
	<-o.done
	if !o.completed.DoneMatchesResult || o.completed.result != partial || !errors.Is(o.completed.err, failure) {
		t.Fatal("partial audit lost or nil public return compared after extraction")
	}
	s = &observedFixtureStream{events: make(chan adaptor.Event, 1), result: partial}
	s.events <- adaptor.Done{Result: &adaptor.Result{RunID: "different-pointer"}}
	close(s.events)
	o = observeStream(s, &runRecord{Name: "mismatch-offline"})
	<-o.done
	if o.completed.DoneMatchesResult {
		t.Fatal("Done mismatch accepted")
	}
}

func TestExplicitLiveOptInAndExactEnvelope(t *testing.T) {
	if validateSettings(settings{}) == nil {
		t.Fatal("bare CLI admitted model calls")
	}
	if validateSettings(settings{agent: "fake", model: "gpt-6.1-sol", effort: "xhigh", report: "report.json", home: t.TempDir(), timeout: time.Minute}) == nil {
		t.Fatal("fake fixture can be mislabeled real")
	}
	valid := settings{agent: "codex", model: "gpt-6.1-sol", effort: "xhigh", report: "report.json", home: t.TempDir(), timeout: time.Minute}
	if err := validateSettings(valid); err != nil {
		t.Fatal(err)
	}
	msg := human(encodeThreadKey("work:space", "discussion/λ"), " opaque id ", "original\n\"text\" @alpha", "alpha")
	other := msg
	other.ID = "different"
	if framed(msg) == framed(other) || msg.ID != " opaque id " || msg.Text != "original\n\"text\" @alpha" {
		t.Fatal("host envelope lost original identity or text")
	}
}
