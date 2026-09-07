package a2adelegation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func alignmentQuestion(id string) clienta2a.TaskStatus {
	return clienta2a.TaskStatus{State: clienta2a.TaskStateInputRequired, Message: &clienta2a.Message{ID: id, Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"schema": "test.status.v1", "result": id}}}}}
}

func TestAlignmentContinuationHistory(t *testing.T) {
	for _, state := range []clienta2a.TaskState{clienta2a.TaskStateInputRequired, clienta2a.TaskStateCompleted, clienta2a.TaskStateFailed} {
		for _, end := range []string{"completed", "question", "EOF", "recover completed", "recover old", "marked old", "recover question"} {
			t.Run(string(state)+"/"+end, func(t *testing.T) {
				stale := clienta2a.Task{ID: "task-1", ContextID: "ctx-1", Status: alignmentQuestion("old"), Artifacts: []clienta2a.Artifact{{ID: "notes", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "kept"}}}}}
				stale.Status.State = state
				stream := &fakeA2AStream{events: make(chan streamRecv, 5), closed: make(chan struct{})}
				stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, Task: &stale, TaskID: stale.ID, ContextID: stale.ContextID}}
				card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
				client := &fakeA2AClient{card: card, stream: stream}
				wantStatus := "completed"
				wantErr := false
				switch end {
				case "completed", "question":
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventStatus, TaskID: stale.ID, ContextID: stale.ContextID, Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}}}
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: stale.ID, ContextID: stale.ContextID, Artifact: &clienta2a.Artifact{ID: "later", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "new output"}}}, LastChunk: true}}
					live := clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}
					if end == "question" {
						live = alignmentQuestion("new")
						wantStatus = "input_required"
					}
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: stale.ID, ContextID: stale.ContextID, Status: &live}}
					// A stale GetTask must never override this live status/question.
					client.getTasks = []clienta2a.Task{stale}
				case "EOF":
					wantStatus = "failed"
					wantErr = true
				case "recover completed", "recover old", "recover question":
					stream.events <- streamRecv{err: errors.New("fixture broken stream")}
					recovered := stale
					if end == "recover completed" {
						recovered.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted, Message: &clienta2a.Message{ID: "recovered", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "fresh completion"}}}}
						recovered.Messages = []clienta2a.Message{{Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "recovered"}}}}
					}
					if end == "recover old" {
						wantErr = true
						wantStatus = "failed"
					}
					if end == "recover question" {
						recovered.Status = alignmentQuestion("new")
						wantStatus = "input_required"
					}
					client.getTasks = []clienta2a.Task{recovered}
				case "marked old":
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, Task: &stale, TaskID: stale.ID, ContextID: stale.ContextID, RecoveredState: true}}
					wantErr = true
					wantStatus = "failed"
				}
				close(stream.events)
				registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: true}})
				if err != nil {
					t.Fatal(err)
				}
				bus := NewEventBus(64)
				d := NewDelegator(registry, bus)
				d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
				d.statusDecoders = []StatusPartDecoder{testStatusPartDecoder{}}
				msg := clienta2a.Message{Role: "user", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "answer old"}}}
				msg.TaskID = stale.ID
				result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Message: &msg, Stream: true})
				if (err != nil) != wantErr || result.Status != wantStatus {
					t.Fatalf("result=%+v err=%v, want %s error=%v", result, err, wantStatus, wantErr)
				}
				if wantErr {
					var de *DelegationError
					if !errors.As(err, &de) || de.Code != "stream_interrupted" {
						t.Fatalf("error=%v", err)
					}
				}
				if !wantErr && client.cancelCalls != 0 {
					t.Fatalf("unexpected cancellation: %d", client.cancelCalls)
				}
				if wantErr && client.cancelCalls != 1 {
					t.Fatalf("cancellation count=%d", client.cancelCalls)
				}
				events := drainAvailableBus(t, bus, "run")
				var newQuestion, artifacts int
				for _, ev := range events {
					if ev.Result == "old" {
						t.Fatalf("old questionnaire replayed: %+v", ev)
					}
					if ev.Result == "new" {
						newQuestion++
					}
					if ev.Kind == DelegationArtifactCreated {
						artifacts++
					}
				}
				if (end == "question" || end == "recover question") && newQuestion != 1 {
					t.Fatalf("new questions=%d events=%+v", newQuestion, events)
				}
				if artifacts == 0 {
					t.Fatal("snapshot artifact lost")
				}
				if end == "completed" || end == "question" {
					if len(result.Artifacts) != 2 {
						t.Fatalf("final artifacts=%+v", result.Artifacts)
					}
				}
			})
		}
	}
}

type alignmentOutcomeRunner struct{ err error }

func (r alignmentOutcomeRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	return nil, r.err
}
func (r alignmentOutcomeRunner) Stream(context.Context, string, ...adaptor.CallOption) adaptor.Stream {
	events := make(chan adaptor.Event)
	close(events)
	return &alignmentOutcomeStream{events: events, err: r.err}
}

type alignmentOutcomeStream struct {
	events chan adaptor.Event
	err    error
}

func (s *alignmentOutcomeStream) Events() <-chan adaptor.Event     { return s.events }
func (s *alignmentOutcomeStream) Result() (*adaptor.Result, error) { return nil, s.err }
func (s *alignmentOutcomeStream) RunID() string                    { return "fixture" }
func (s *alignmentOutcomeStream) Cancel()                          {}

func TestAlignmentLocalPrimaryReasonAndPartialResult(t *testing.T) {
	for _, reason := range []adaptor.FailureReason{adaptor.ReasonApprovalDenied, adaptor.ReasonApprovalTimeout, adaptor.ReasonCancelled, ""} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%v/stream=%v", reason, cause, streaming), func(t *testing.T) {
					err := cause
					want := clienta2a.TaskStateCanceled
					if cause == context.DeadlineExceeded {
						want = clienta2a.TaskStateFailed
					}
					if reason != "" {
						err = errors.Join(&adaptor.RunError{Reason: reason, Result: &adaptor.Result{Text: "partial"}}, cause)
						if reason != adaptor.ReasonCancelled {
							want = clienta2a.TaskStateFailed
						} else {
							want = clienta2a.TaskStateCanceled
						}
					}
					c := newLocalClient("fixture", alignmentOutcomeRunner{err: err})
					req := clienta2a.SendRequest{Message: clienta2a.Message{Role: "user", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "work"}}}}
					var task clienta2a.Task
					if streaming {
						s, e := c.SendStream(context.Background(), req)
						if e != nil {
							t.Fatal(e)
						}
						defer s.Close()
						for {
							ev, e := s.Recv()
							if e != nil {
								t.Fatal(e)
							}
							if ev.Task != nil {
								task = *ev.Task
								break
							}
						}
					} else {
						task, err = c.Send(context.Background(), req)
						if err != nil {
							t.Fatal(err)
						}
					}
					if task.Status.State != want {
						t.Fatalf("state=%s want %s", task.Status.State, want)
					}
					if reason != "" && (len(task.Messages) != 1 || textFromMessage(task.Messages[0]) != "partial") {
						t.Fatalf("partial text lost: %+v", task)
					}
				})
			}
		}
	}
}

func TestAlignmentSameTaskMultipleAnswers(t *testing.T) {
	card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: true}})
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeA2AClient{card: card, getErr: errors.New("query unavailable")}
	bus := NewEventBus(128)
	d := NewDelegator(registry, bus, WithStatusPartDecoder(testStatusPartDecoder{}))
	d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	for turn := 0; turn < 3; turn++ {
		old := fmt.Sprintf("question-%d", turn)
		next := fmt.Sprintf("question-%d", turn+1)
		snapshot := clienta2a.Task{ID: "same-task", ContextID: "same-context", Status: alignmentQuestion(old)}
		live := alignmentQuestion(next)
		if turn == 2 {
			live = clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}
		}
		s := &fakeA2AStream{events: make(chan streamRecv, 4), closed: make(chan struct{})}
		s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTask, Task: &snapshot, TaskID: snapshot.ID, ContextID: snapshot.ContextID}}
		s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Artifact: &clienta2a.Artifact{ID: "answer", Name: "answer.md", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "part 1"}}}}}
		s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Artifact: &clienta2a.Artifact{ID: "answer", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "part 2"}}}, Append: true, LastChunk: true}}
		s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Status: &live}}
		close(s.events)
		client.stream = s
		runID := fmt.Sprintf("turn-%d", turn)
		result, err := d.Delegate(context.Background(), DelegationRequest{RunID: runID, Agent: "fixture", Stream: true, Message: &clienta2a.Message{Role: "user", TaskID: snapshot.ID, ContextID: snapshot.ContextID, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "answer " + old}}}, IncludeRemoteArtifacts: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.RemoteTaskID != snapshot.ID || result.RemoteContextID != snapshot.ContextID {
			t.Fatalf("identity drift: %+v", result)
		}
		if len(result.RemoteArtifacts) != 1 || len(result.RemoteArtifacts[0].Parts) != 2 || result.RemoteArtifacts[0].Name != "answer.md" {
			t.Fatalf("appended artifact lost: %+v", result.RemoteArtifacts)
		}
		newQuestions := 0
		for _, ev := range drainAvailableBus(t, bus, runID) {
			if ev.Result == old {
				t.Fatalf("answered questionnaire replayed: %+v", ev)
			}
			if ev.Result == next {
				newQuestions++
			}
		}
		want := 1
		if turn == 2 {
			want = 0
		}
		if newQuestions != want {
			t.Fatalf("turn %d questions=%d", turn, newQuestions)
		}
	}
	if client.cancelCalls != 0 || client.streamCalls != 3 {
		t.Fatalf("cancel=%d streams=%d", client.cancelCalls, client.streamCalls)
	}
}

func TestAlignmentSyncAndPollingTasksRemainAuthoritative(t *testing.T) {
	for _, poll := range []bool{false, true} {
		t.Run(fmt.Sprintf("poll=%v", poll), func(t *testing.T) {
			card := clienta2a.AgentCard{Name: "fixture"}
			task := clienta2a.Task{ID: "same-task", ContextID: "same-context", Status: alignmentQuestion("current")}
			client := &fakeA2AClient{card: card, sendTask: task}
			if poll {
				client.sendTask.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateWorking}
				client.getTasks = []clienta2a.Task{task}
			}
			registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: true, PollInterval: 1}})
			if err != nil {
				t.Fatal(err)
			}
			bus := NewEventBus(32)
			d := NewDelegator(registry, bus, WithStatusPartDecoder(testStatusPartDecoder{}))
			d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
			result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "query", Agent: "fixture", Objective: "first synchronous task"})
			if err != nil || result.Status != "input_required" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			questions := 0
			for _, ev := range drainAvailableBus(t, bus, "query") {
				if ev.Result == "current" {
					questions++
				}
			}
			if questions != 1 || client.streamCalls != 0 {
				t.Fatalf("questions=%d streamCalls=%d", questions, client.streamCalls)
			}
		})
	}
}

func TestAlignmentRecoverySameTextNewMessageID(t *testing.T) {
	old := alignmentQuestion("same text")
	old.Message.ID = "old-message"
	next := alignmentQuestion("same text")
	next.Message.ID = "new-message"
	card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
	registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card, Policy: DelegationPolicy{AllowInputRequired: true}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := clienta2a.Task{ID: "same-task", Status: old}
	recovered := clienta2a.Task{ID: snapshot.ID, Status: next}
	s := &fakeA2AStream{events: make(chan streamRecv, 2), closed: make(chan struct{})}
	s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTask, Task: &snapshot, TaskID: snapshot.ID}}
	s.events <- streamRecv{err: errors.New("broken stream")}
	close(s.events)
	client := &fakeA2AClient{card: card, stream: s, getTasks: []clienta2a.Task{recovered}}
	bus := NewEventBus(32)
	d := NewDelegator(registry, bus, WithStatusPartDecoder(testStatusPartDecoder{}))
	d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
	result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Message: &clienta2a.Message{Role: "user", TaskID: snapshot.ID, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "answer"}}}})
	if err != nil || result.Status != "input_required" {
		t.Fatalf("new question rejected: result=%+v err=%v", result, err)
	}
	questions := 0
	for _, ev := range drainAvailableBus(t, bus, "run") {
		if ev.Result == "same text" {
			questions++
		}
	}
	if questions != 1 || client.cancelCalls != 0 {
		t.Fatalf("questions=%d cancel=%d", questions, client.cancelCalls)
	}
}

func TestAlignmentLiveArtifactsSurviveHistoryAndRecovery(t *testing.T) {
	for _, end := range []string{"live status", "transport recovery", "marked recovery"} {
		t.Run(end, func(t *testing.T) {
			artifact := clienta2a.Artifact{ID: "notes", Name: "notes.md", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "first"}}}
			snapshot := clienta2a.Task{ID: "task", ContextID: "context", Status: alignmentQuestion("old"), Artifacts: []clienta2a.Artifact{artifact}}
			recovered := snapshot
			recovered.Status = clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}
			s := &fakeA2AStream{events: make(chan streamRecv, 4), closed: make(chan struct{})}
			history := clienta2a.Event{Kind: clienta2a.EventTask, Task: &snapshot, TaskID: snapshot.ID, ContextID: snapshot.ContextID}
			s.events <- streamRecv{event: history}
			s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Artifact: &clienta2a.Artifact{ID: "notes", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "second"}}}, Append: true, LastChunk: true}}
			s.events <- streamRecv{event: history}
			switch end {
			case "live status":
				s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Status: &recovered.Status}}
			case "transport recovery":
				s.events <- streamRecv{err: errors.New("broken")}
			case "marked recovery":
				s.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: snapshot.ID, ContextID: snapshot.ContextID, Task: &recovered, RecoveredState: true}}
			}
			close(s.events)
			card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
			registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card})
			if err != nil {
				t.Fatal(err)
			}
			client := &fakeA2AClient{card: card, stream: s, getTasks: []clienta2a.Task{recovered}}
			bus := NewEventBus(32)
			d := NewDelegator(registry, bus)
			d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
			result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "answer", IncludeRemoteArtifacts: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.RemoteArtifacts) != 1 || len(result.RemoteArtifacts[0].Parts) != 2 || result.RemoteArtifacts[0].Parts[1].Text != "second" {
				t.Fatalf("live appended artifact overwritten: %+v", result.RemoteArtifacts)
			}
			// Snapshot replay must not publish a replacement after the live update.
			events := drainAvailableBus(t, bus, "run")
			artifacts := 0
			for _, ev := range events {
				if ev.Kind == DelegationArtifactCreated {
					artifacts++
				}
			}
			want := 2
			if end != "live status" {
				want = 3
			} // Explicit recovery may publish the merged final artifact.
			if artifacts != want {
				t.Fatalf("historical artifact replayed: count=%d events=%+v", artifacts, events)
			}
		})
	}
}
