package a2adelegation

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func TestAlignmentPollingPartialArtifactLimit(t *testing.T) {
	for _, reason := range []string{"cancelled", "deadline_exceeded", "active_execution_timeout"} {
		for _, max := range []int{-1, 0, 1} {
			for _, full := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/max_%d/full_%t", reason, max, full), func(t *testing.T) {
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					cancelCause := errors.New("caller stopped delegation")
					clock := &alignmentClock{now: time.Now()}
					task := clienta2a.Task{
						ID: "partial-task", ContextID: "partial-context",
						Status:   clienta2a.TaskStatus{State: clienta2a.TaskStateWorking},
						Messages: []clienta2a.Message{{Role: "agent", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "partial answer"}}}},
						Artifacts: []clienta2a.Artifact{
							{ID: "first", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "one"}}},
							{ID: "second", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "two"}}},
						},
					}
					var cancelCalls atomic.Int32
					polled := false
					client := &alignmentClient{
						send: func(ctx context.Context, req clienta2a.SendRequest) (clienta2a.Task, error) {
							if !req.ReturnImmediately {
								t.Fatal("fixture did not enter polling transport")
							}
							if reason == "active_execution_timeout" {
								clock.advance(99 * time.Millisecond)
								if ctx.Err() != nil {
									t.Fatalf("100ms budget expired at 99ms: %v", ctx.Err())
								}
							}
							return task, nil
						},
						get: func(ctx context.Context, req clienta2a.GetTaskRequest) (clienta2a.Task, error) {
							if req.TaskID != task.ID {
								t.Fatalf("poll task=%q", req.TaskID)
							}
							if !polled {
								polled = true
								switch reason {
								case "cancelled":
									cancel(cancelCause)
								case "active_execution_timeout":
									clock.advance(time.Millisecond)
									clock.timer(0).fire()
								}
							}
							<-ctx.Done()
							return clienta2a.Task{}, ctx.Err()
						},
						cancel: func(ctx context.Context, req clienta2a.CancelTaskRequest) (clienta2a.Task, error) {
							cancelCalls.Add(1)
							deadline, bounded := ctx.Deadline()
							if req.TaskID != task.ID || ctx.Err() != nil || !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
								t.Errorf("remote cleanup must have the known task and an independent five-second bound: task=%q err=%v deadline=%v", req.TaskID, ctx.Err(), deadline)
							}
							return clienta2a.Task{}, nil
						},
					}
					d := alignmentDelegator(t, DelegationPolicy{PollInterval: time.Millisecond}, client)
					req := DelegationRequest{RunID: "leader", Agent: "member", Objective: "partial artifacts", IncludeRemoteArtifacts: full}
					wantArtifacts := 2
					if max >= 0 {
						req.MaxArtifacts = &max
						wantArtifacts = max
					}
					switch reason {
					case "deadline_exceeded":
						req.Timeout = 100 * time.Millisecond
					case "active_execution_timeout":
						d.budgetClock = clock
						req.ActiveExecutionTimeout = 100 * time.Millisecond
					}
					result, err := d.Delegate(ctx, req)
					var failure *DelegationError
					if !errors.As(err, &failure) || failure.Code != reason || result.Error != failure {
						t.Fatalf("partial result lost primary failure: result=%+v err=%v", result, err)
					}
					wantStatus, wantTerminal := "failed", DelegationFailed
					switch reason {
					case "cancelled":
						wantStatus, wantTerminal = "cancelled", DelegationCancelled
						if !errors.Is(err, cancelCause) || !errors.Is(err, context.Canceled) {
							t.Fatalf("caller cause lost: %v", err)
						}
					case "deadline_exceeded":
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("deadline cause lost: %v", err)
						}
					case "active_execution_timeout":
						var limit *adaptor.ActiveExecutionTimeoutError
						if !errors.Is(err, adaptor.ErrActiveExecutionTimeout) || !errors.As(err, &limit) || limit.Limit != req.ActiveExecutionTimeout {
							t.Fatalf("own active limit lost: %v", err)
						}
					}
					if result.Status != wantStatus || result.RemoteTaskID != task.ID || result.RemoteContextID != task.ContextID || result.Summary != "partial answer" || len(result.Messages) != 1 || result.Messages[0].Text != "partial answer" {
						t.Fatalf("partial result lost: %+v", result)
					}
					if cancelCalls.Load() != 1 {
						t.Fatalf("remote cancel calls=%d, want exactly one", cancelCalls.Load())
					}
					if len(result.Artifacts) != wantArtifacts {
						t.Errorf("compact artifacts=%d, want %d", len(result.Artifacts), wantArtifacts)
					}
					for i, artifact := range result.Artifacts {
						if i >= len(task.Artifacts) || artifact.ID != task.Artifacts[i].ID || len(artifact.Parts) != 0 {
							t.Errorf("compact artifact order/projection changed: %+v", artifact)
						}
					}
					if full {
						if len(result.RemoteArtifacts) != 2 || result.RemoteArtifacts[0].Parts[0].Text != "one" || result.RemoteArtifacts[1].Parts[0].Text != "two" {
							t.Fatalf("opt-in full artifacts were capped: %+v", result.RemoteArtifacts)
						}
					} else if len(result.RemoteArtifacts) != 0 {
						t.Fatalf("full artifacts exposed without opt-in: %+v", result.RemoteArtifacts)
					}
					updates, drops, terminals := 0, 0, 0
					for _, ev := range drainAvailableBus(t, d.Bus, "leader") {
						switch ev.Kind {
						case DelegationArtifactCreated:
							if drops != 0 || terminals != 0 || ev.Artifact == nil || updates >= 2 || ev.Artifact.ID != task.Artifacts[updates].ID {
								t.Fatalf("artifact event lost or reordered: %+v", ev)
							}
							if full && (len(ev.Artifact.Parts) != 1 || ev.Artifact.Parts[0].Text != task.Artifacts[updates].Parts[0].Text) {
								t.Fatalf("live artifact parts were capped: %+v", ev.Artifact)
							}
							updates++
						case DelegationStreamDropped:
							if ev.Raw["reason"] == "artifact_result_limit" {
								drops++
								if updates != 2 || terminals != 0 || ev.Raw["omitted_count"] != 2-wantArtifacts || ev.Raw["max_artifacts"] != max {
									t.Errorf("count-limit event lost its counts/order: %+v", ev)
								}
							}
						case DelegationFailed, DelegationCancelled, DelegationFinished:
							terminals++
							if ev.Kind != wantTerminal || ev.Error == nil || ev.Error.Code != reason {
								t.Errorf("terminal changed primary failure: %+v", ev)
							}
						}
					}
					wantDrops := 0
					if wantArtifacts < 2 {
						wantDrops = 1
					}
					if updates != 2 || drops != wantDrops || terminals != 1 {
						t.Errorf("events: artifacts=%d drops=%d terminals=%d, want 2/%d/1", updates, drops, terminals, wantDrops)
					}
				})
			}
		}
	}
}
