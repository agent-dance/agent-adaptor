package team

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestForkCanContinueCopiedPerScopeTask(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var parentCalls atomic.Int32
	var childCalls atomic.Int32
	var child atomic.Bool
	var workerFork atomic.Bool
	var previous string
	var continueCode string
	worker := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if child.Load() && req.Session != nil && req.Session.Mode == driver.SessionFork {
			workerFork.Store(true)
		}
		return answer("worker", 1), nil
	}})
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if child.Load() && childCalls.Add(1) == 1 {
			result, err := call(team, req, "delegate", map[string]any{"member": "worker", "objective": "continue on branch", "continue": previous})
			if err != nil {
				continueCode, _ = result["code"].(string)
			}
		} else if !child.Load() && parentCalls.Add(1) == 1 {
			result, err := call(team, req, "delegate", map[string]any{"member": "worker", "objective": "seed"})
			if err != nil {
				return driver.Response{}, err
			}
			previous = result["task"].(string)
		}
		return answer("leader", 1), nil
	}})
	store := NewMemoryStore()
	team = fixture(t, Config{Leader: leader, Store: store, Members: []Member{{Key: "worker", Runner: worker, Continuity: PerScope}}})
	if _, _, err := collect(t, team.Thread("parent").Send(ctx, "seed")); err != nil {
		t.Fatal(err)
	}
	branch, err := team.Thread("parent").Fork("child")
	if err != nil {
		t.Fatal(err)
	}
	child.Store(true)
	if _, _, err := collect(t, branch.Send(ctx, "continue previous task")); err != nil {
		t.Fatal(err)
	}
	if continueCode != "" {
		t.Fatalf("healthy copied PerScope task cannot continue on first branch execution: %s", continueCode)
	}
	if !workerFork.Load() {
		t.Fatal("continued task did not execute in the forked member context")
	}
	tasks, err := store.Tasks(ctx, "child")
	if err != nil || len(tasks) != 2 {
		t.Fatalf("branch tasks=%+v err=%v", tasks, err)
	}
	if tasks[1].Continues != previous || tasks[1].State != Succeeded || tasks[1].Conversation != EncodeKey("child", "member", "worker") {
		t.Fatalf("continued branch task=%+v", tasks[1])
	}
	parent, err := store.Tasks(ctx, "parent")
	if err != nil || len(parent) != 1 || parent[0].ID != previous {
		t.Fatalf("Fork changed parent task history: tasks=%+v err=%v", parent, err)
	}

}

func TestForkRejectsContinueWithoutHealthyParentContext(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := testContext(t)
			var team *Team
			var calls atomic.Int32
			var workerCalls atomic.Int32
			var child atomic.Bool
			var previous, continueCode string
			worker := adaptor.New(&fakeDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
				workerCalls.Add(1)
				response := answer("worker", 1)
				switch mode {
				case "missing":
					response.Checkpoint = nil
				case "invalid":
					response.Checkpoint.Valid = false
				case "failed":
					return response, errors.New("failed with an unusable new checkpoint")
				}
				return response, nil
			}})
			leader := adaptor.New(&fakeDriver{run: func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
				if child.Load() {
					result, err := call(team, req, "delegate", map[string]any{"member": "worker", "objective": "continue on branch", "continue": previous})
					if err != nil {
						continueCode, _ = result["code"].(string)
					}
				} else if calls.Add(1) == 1 {
					result, err := call(team, req, "delegate", map[string]any{"member": "worker", "objective": "seed"})
					if err != nil {
						return driver.Response{}, err
					}
					previous = result["task"].(string)
				}
				return answer("leader", 1), nil
			}})
			team = fixture(t, Config{Leader: leader, Members: []Member{{Key: "worker", Runner: worker, Continuity: PerScope}}})
			if _, _, err := collect(t, team.Thread("parent").Send(ctx, "seed")); err != nil {
				t.Fatal(err)
			}
			branch, err := team.Thread("parent").Fork("child")
			if err != nil {
				t.Fatal(err)
			}
			child.Store(true)
			if _, _, err := collect(t, branch.Send(ctx, "continue previous task")); err != nil {
				t.Fatal(err)
			}
			if continueCode != "invalid_continue" || workerCalls.Load() != 1 {
				t.Fatalf("unhealthy parent continued: code=%q worker calls=%d", continueCode, workerCalls.Load())
			}
		})
	}
}
