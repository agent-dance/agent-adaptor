package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type fakeDriver struct {
	run func(context.Context, driver.Request, driver.EventSink) (driver.Response, error)
	cap driver.MessageCapability
}

func (fake *fakeDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "team-fake", Sessions: driver.SessionCapability{SupportsResume: true}, MCP: driver.MCPCapability{Supported: true, HTTP: true}, Messages: fake.cap, RunPolicyCaps: driver.RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true, AutoApprove: true, AutoReject: true}}}
}
func (*fakeDriver) ValidateConfig(any) error                  { return nil }
func (*fakeDriver) SessionConfigFingerprint() (string, error) { return "team-fake-config", nil }
func (*fakeDriver) SessionCodec() driver.SessionCodec         { return fakeCodec{} }
func (*fakeDriver) StreamCapability() driver.StreamCapability {
	return driver.StreamCapability{Native: true, TokenLevel: true}
}
func (fake *fakeDriver) Run(ctx context.Context, request driver.Request, sink driver.EventSink) (driver.Response, error) {
	return fake.run(ctx, request, sink)
}

type fakeCodec struct{}

func (fakeCodec) Name() string { return "team-fake-codec" }
func (fakeCodec) ToParams(state *driver.SessionState) driver.SessionParams {
	if state == nil {
		return driver.SessionParams{}
	}
	return driver.SessionParams{ResumeID: state.ResumeID, DisplayID: state.DisplayID, Values: maps.Clone(state.Data)}
}
func (fakeCodec) FromParams(params driver.SessionParams) *driver.SessionState {
	if params.ResumeID == "" && params.DisplayID == "" && len(params.Values) == 0 {
		return nil
	}
	return &driver.SessionState{ResumeID: params.ResumeID, DisplayID: params.DisplayID, Data: maps.Clone(params.Values)}
}
func (fakeCodec) GuardFingerprint(params driver.SessionParams) string { return params.ResumeID }

func answer(text string, tokens int) driver.Response {
	return driver.Response{Output: text, Summary: "summary " + text, Usage: &driver.Usage{InputTokens: tokens}, Checkpoint: &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "healthy"}}, RawStreams: &driver.RawStreams{Stdout: "raw " + text}, Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: text}}}
}

func fixture(t *testing.T, config Config) *Team {
	t.Helper()
	if config.Limits.WakeWindow == 0 {
		config.Limits.WakeWindow = time.Nanosecond
	}
	if config.Limits.RetryBackoff == 0 {
		config.Limits.RetryBackoff = time.Nanosecond
	}
	team, err := newTeam(config, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := team.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return team
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func tokenFor(request driver.Request, team *Team) string {
	for _, binding := range request.Runtime.SecretEnv {
		if strings.HasSuffix(binding.Name, strings.ToUpper(team.id)) {
			return binding.Value
		}
	}
	return ""
}

func rpc(team *Team, token, method string, params any) (map[string]any, int, error) {
	encoded, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, 0, err
	}
	request := httptest.NewRequest(http.MethodPost, team.url, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	team.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		return nil, recorder.Code, nil
	}
	var response map[string]any
	err = json.Unmarshal(recorder.Body.Bytes(), &response)
	return response, recorder.Code, err
}

func call(team *Team, request driver.Request, name string, args any) (map[string]any, error) {
	response, status, err := rpc(team, tokenFor(request, team), "tools/call", map[string]any{"name": team.config.Surface.Prefix + name, "arguments": args})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: runtime=%+v", status, request.Runtime)
	}
	if response["error"] != nil {
		return nil, fmt.Errorf("RPC error: %v", response["error"])
	}
	result := response["result"].(map[string]any)
	if result["isError"] == true {
		return result["structuredContent"].(map[string]any), errors.New("tool error")
	}
	return result["structuredContent"].(map[string]any), nil
}

func collect(t *testing.T, stream adaptor.Stream) ([]adaptor.Event, *adaptor.Result, error) {
	t.Helper()
	var events []adaptor.Event
	for event := range stream.Events() {
		events = append(events, event)
	}
	result, err := stream.Result()
	if len(events) == 0 {
		t.Fatal("missing events")
	}
	done, ok := events[len(events)-1].(adaptor.Done)
	if !ok || done.Result != result || done.Err != err {
		t.Fatalf("Done mismatch: %#v, %p %v", events[len(events)-1], result, err)
	}
	for index, event := range events {
		if event.Meta().RunID != stream.RunID() || event.Meta().Sequence != uint64(index+1) {
			t.Fatalf("bad event envelope: %+v", event.Meta())
		}
	}
	again, againErr := stream.Result()
	if again != result || againErr != err {
		t.Fatal("unstable Result")
	}
	return events, result, err
}

func TestDelegateWakeSettleAndResult(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var calls atomic.Int32
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		turn := calls.Add(1)
		if turn == 1 {
			for _, fragment := range []string{"DELEGATION FIRST", "MUST be delegated", "never block waiting", "review", "skills", "Wake rules", "team_delegate"} {
				if !strings.Contains(request.Prompt, fragment) {
					t.Errorf("preamble missing %q", fragment)
				}
			}
			_, err := call(team, request, "delegate", map[string]any{"member": "review", "objective": "review code"})
			if err != nil {
				return driver.Response{}, err
			}
			return answer("delegated", 2), nil
		}
		if !strings.Contains(request.Prompt, "worker finished") {
			t.Errorf("wake omitted worker output: %s", request.Prompt)
		}
		return answer("integrated", 3), nil
	}}
	worker := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if !strings.Contains(request.Prompt, "team_send") {
			t.Error("missing member preamble")
		}
		return answer("worker finished", 5), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "review", Name: "Review", Description: "review code", Skills: []string{"Go"}, Runner: adaptor.New(worker)}}})
	events, result, err := collect(t, team.Send(ctx, "objective"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "integrated" || result.Turns != 2 || result.Usage.InputTokens != 10 || calls.Load() != 2 {
		t.Fatalf("result=%+v usage=%+v calls=%d", result, result.Usage, calls.Load())
	}
	if tasks := result.Subagents(); len(tasks) != 1 || tasks[0].State != string(Succeeded) || tasks[0].Usage.InputTokens != 5 {
		t.Fatalf("subagents=%+v", tasks)
	}
	turns := 0
	for _, event := range events {
		if turn, ok := event.(adaptor.TurnEnd); ok {
			turns++
			if turn.Turn != turns {
				t.Error("turn numbering")
			}
		}
	}
	if turns != 2 {
		t.Fatalf("turns=%d", turns)
	}
}

func TestHostMessageWhileOnlyWorkerRuns(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	workerStarted := make(chan struct{})
	workerRelease := make(chan struct{})
	leaderIdle := make(chan struct{})
	hostTurn := make(chan struct{})
	var turns atomic.Int32
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		switch turns.Add(1) {
		case 1:
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
			return answer("idle", 1), nil
		case 2:
			if !strings.Contains(request.Prompt, "host steering") {
				t.Error("host did not target leader")
			}
			close(hostTurn)
			return answer("host response", 1), nil
		default:
			return answer("settled", 1), nil
		}
	}}
	worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		close(workerStarted)
		select {
		case <-workerRelease:
			return answer("finished", 1), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}})
	stream := team.Thread("host/scope").Send(ctx, "start")
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for event := range stream.Events() {
			if turn, ok := event.(adaptor.TurnEnd); ok && turn.Turn == 1 {
				close(leaderIdle)
			}
		}
	}()
	<-workerStarted
	<-leaderIdle
	if err := stream.Send(ctx, "host steering", adaptor.When(adaptor.AfterTurn)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hostTurn:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(workerRelease)
	<-eventsDone
	if result, err := stream.Result(); err != nil || result.Turns != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestToolsAndCapabilityIsolation(t *testing.T) {
	for _, size := range []int{1, 15} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := testContext(t)
			var team *Team
			var turns atomic.Int32
			worker := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
				response, _, err := rpc(team, tokenFor(request, team), "tools/list", map[string]any{})
				if err != nil {
					return driver.Response{}, err
				}
				if tools := response["result"].(map[string]any)["tools"].([]any); len(tools) != 2 {
					t.Errorf("worker tools=%v", tools)
				}
				for name, args := range map[string]any{"delegate": map[string]any{"member": "m0", "objective": "bad"}, "cancel": map[string]any{"task": "bad", "reason": "bad"}, "send": map[string]any{"text": "bad", "from": "leader"}} {
					if _, err := call(team, request, name, args); err == nil {
						t.Errorf("worker allowed %s", name)
					}
				}
				if _, status, _ := rpc(team, tokenFor(request, team)+"tamper", "tools/list", nil); status != http.StatusUnauthorized {
					t.Error("tampered token accepted")
				}
				return answer("done", 1), nil
			}}
			leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
				if turns.Add(1) == 1 {
					response, _, err := rpc(team, tokenFor(request, team), "tools/list", nil)
					if err != nil {
						return driver.Response{}, err
					}
					if tools := response["result"].(map[string]any)["tools"].([]any); len(tools) != 4 {
						t.Errorf("leader tools=%v", tools)
					}
					_, err = call(team, request, "delegate", map[string]any{"member": "m0", "objective": "test"})
					if err != nil {
						return driver.Response{}, err
					}
				}
				return answer("leader", 1), nil
			}}
			members := make([]Member, size)
			for index := range members {
				members[index] = Member{Key: fmt.Sprintf("m%d", index), Runner: adaptor.New(worker)}
			}
			team = fixture(t, Config{Leader: adaptor.New(leader), Members: members})
			if _, _, err := collect(t, team.Send(ctx, "start")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSchemaValidationAndContinue(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	var turns atomic.Int32
	var taskID string
	var workerCalls atomic.Int32
	worker := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if workerCalls.Add(1) == 2 && (request.Session == nil || request.Session.State.ResumeID != "healthy") {
			t.Error("continue lost context")
		}
		return answer("work", 1), nil
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		switch turns.Add(1) {
		case 1:
			value, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "bad", "input": map[string]any{"count": "bad"}})
			if err == nil || value["code"] != "invalid_member_input" || value["schema"] == nil {
				t.Errorf("validation=%v %v", value, err)
			}
			value, err = call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work", "input": map[string]any{"count": 2}})
			if err != nil {
				return driver.Response{}, err
			}
			taskID = value["task"].(string)
		case 2:
			value, err := call(team, request, "send", map[string]any{"task": taskID, "text": "too late"})
			if err == nil || value["code"] != "task_finished" || !strings.Contains(value["message"].(string), "continue") {
				t.Errorf("finished send=%v %v", value, err)
			}
			_, err = call(team, request, "delegate", map[string]any{"member": "worker", "objective": "continue", "continue": taskID, "input": map[string]any{"count": 3}})
			if err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	schema := map[string]any{"type": "object", "required": []string{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer"}}, "additionalProperties": false}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker), Input: schema}}})
	_, result, err := collect(t, team.Send(ctx, "start"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Subagents()) != 2 || workerCalls.Load() != 2 {
		t.Fatalf("tasks=%+v calls=%d", result.Subagents(), workerCalls.Load())
	}
}

func TestMaxActiveQueueing(t *testing.T) {
	ctx := testContext(t)
	var team *Team
	starts := make(chan string, 2)
	release := make(chan struct{})
	registered := make(chan struct{})
	var turns atomic.Int32
	worker := &fakeDriver{run: func(ctx context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		starts <- request.Prompt
		select {
		case <-release:
			return answer("done", 1), nil
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}}
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			for index := 0; index < 2; index++ {
				if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": fmt.Sprint(index)}); err != nil {
					return driver.Response{}, err
				}
			}
			status, err := call(team, request, "status", map[string]any{})
			if err != nil {
				return driver.Response{}, err
			}
			queued := 0
			running := 0
			for _, item := range status["tasks"].([]any) {
				state := item.(map[string]any)["state"]
				if state == "queued" {
					queued++
				}
				if state == "running" {
					running++
				}
			}
			if queued != 1 || running != 1 {
				t.Errorf("status=%v", status)
			}
			close(registered)
		}
		return answer("leader", 1), nil
	}}
	team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker), MaxActive: 1}}})
	stream := team.Send(ctx, "start")
	<-registered
	<-starts
	select {
	case <-starts:
		t.Error("second worker started before slot released")
	default:
	}
	close(release)
	if _, result, err := collect(t, stream); err != nil || len(result.Subagents()) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestKeysAndStoreFencing(t *testing.T) {
	if EncodeKey("a:b", "c") == EncodeKey("a", "b:c") || EncodeKey("", "x") == EncodeKey("x", "") {
		t.Fatal("key collision")
	}
	store := NewMemoryStore()
	ctx := context.Background()
	lease, err := store.AcquireScope(ctx, "scope", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireScope(ctx, "scope", "other", time.Minute); !errors.Is(err, ErrThreadBusy) {
		t.Fatal(err)
	}
	task, err := store.SaveTask(ctx, lease, Task{Scope: "scope", ID: "task", State: Queued, Input: map[string]any{"nested": map[string]any{"value": "original"}}})
	if err != nil {
		t.Fatal(err)
	}
	stale := task
	task.State = Running
	if _, err := store.SaveTask(ctx, lease, task); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTask(ctx, lease, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted")
	}
	if err := store.PutMessage(ctx, lease, InboxMessage{Scope: "scope", ID: "message", Text: "note", Wake: true}); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ClaimMessages(ctx, lease, true, time.Minute)
	if err != nil || len(messages) != 1 {
		t.Fatalf("%v %v", messages, err)
	}
	if err := store.FinishMessage(ctx, lease, "message", "wrong", Delivered, ""); !errors.Is(err, ErrConflict) {
		t.Fatal("stale claim accepted")
	}
	if err := store.FinishMessage(ctx, lease, "message", messages[0].ClaimToken, Delivered, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseScope(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveTask(ctx, lease, task); !errors.Is(err, ErrConflict) {
		t.Fatal("released owner wrote")
	}
}

func TestStandbyGetOrCreateCancelAndRouting(t *testing.T) {
	ctx := testContext(t)
	entered := make(chan struct{}, 1)
	var calls atomic.Int32
	leader := &fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-ctx.Done()
			return driver.Response{}, ctx.Err()
		}
		return answer("next execution", 1), nil
	}}
	team := fixture(t, Config{Leader: adaptor.New(leader)})
	thread := team.Thread("chat")
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	waiting, err := thread.Standby(lifetime)
	if err != nil {
		t.Fatal(err)
	}
	again, err := team.Thread("chat").Standby(ctx)
	if err != nil || waiting != again {
		t.Fatal("standby not reused")
	}
	events := waiting.Events()
	if err := waiting.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	<-entered
	waiting.Cancel()
	for event := range events {
		if _, ok := event.(adaptor.Done); ok {
			break
		}
	}
	if err := waiting.Send(ctx, "next"); err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if done, ok := event.(adaptor.Done); ok {
			if done.Err != nil || done.Result.Text != "next execution" {
				t.Fatalf("done=%+v", done)
			}
			break
		}
	}
	cancel()
	for range events {
	}
}

type temporaryFailure struct{}

func (temporaryFailure) Error() string             { return "transient" }
func (temporaryFailure) RetryAfter() time.Duration { return 0 }

func TestLeaderRetryAndLimitPartialResult(t *testing.T) {
	t.Run("retry", func(t *testing.T) {
		ctx := testContext(t)
		var calls atomic.Int32
		leader := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
			if calls.Add(1) < 3 {
				return driver.Response{}, temporaryFailure{}
			}
			return answer("recovered", 1), nil
		}}
		team := fixture(t, Config{Leader: adaptor.New(leader)})
		_, result, err := collect(t, team.Send(ctx, "start"))
		if err != nil || result.Text != "recovered" || calls.Load() != 3 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
		}
	})
	t.Run("hard limit", func(t *testing.T) {
		ctx := testContext(t)
		var team *Team
		started := make(chan struct{})
		worker := &fakeDriver{run: func(ctx context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
			close(started)
			<-ctx.Done()
			return answer("partial worker", 2), ctx.Err()
		}}
		leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
			if _, err := call(team, request, "delegate", map[string]any{"member": "worker", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
			<-started
			return answer("partial leader", 1), nil
		}}
		team = fixture(t, Config{Leader: adaptor.New(leader), Members: []Member{{Key: "worker", Runner: adaptor.New(worker)}}, Limits: Limits{MaxLeaderTurns: 1}})
		events, result, err := collect(t, team.Send(ctx, "start"))
		var failure *adaptor.RunError
		if !errors.Is(err, ErrLimitExceeded) || !errors.As(err, &failure) || failure.Result != result || failure.Reason != adaptor.ReasonLimitExceeded {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		noticed := false
		for _, event := range events {
			if notice, ok := event.(adaptor.Notice); ok && notice.Kind == "limit.approaching" {
				noticed = true
			}
		}
		if !noticed {
			t.Error("missing limit notice")
		}
	})
}

func TestNestedTeamPerScope(t *testing.T) {
	ctx := testContext(t)
	var outer *Team
	var outerCalls atomic.Int32
	var innerCalls atomic.Int32
	innerLeader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if innerCalls.Add(1) == 2 && request.Session == nil {
			t.Error("nested PerScope lost context")
		}
		return answer("inner answer", 2), nil
	}}
	inner := fixture(t, Config{Leader: adaptor.New(innerLeader), Surface: Surface{Prefix: "inner_"}})
	outerLeader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if outerCalls.Add(1)%2 == 1 {
			if _, err := call(outer, request, "delegate", map[string]any{"member": "nested", "objective": "inner objective"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("outer answer", 1), nil
	}}
	outer = fixture(t, Config{Leader: adaptor.New(outerLeader), Members: []Member{{Key: "nested", Runner: inner, Continuity: PerScope}}})
	for index := 0; index < 2; index++ {
		if _, result, err := collect(t, outer.Thread("scope").Send(ctx, "start")); err != nil || result.Usage.InputTokens != 4 {
			t.Fatalf("result=%+v usage=%+v err=%v", result, result.Usage, err)
		}
	}
}

func TestEventLogNoConsumerAndConcurrentResult(t *testing.T) {
	ctx := testContext(t)
	leader := &fakeDriver{run: func(_ context.Context, _ driver.Request, _ driver.EventSink) (driver.Response, error) {
		return answer("ok", 0), nil
	}}
	team := fixture(t, Config{Leader: adaptor.New(leader)})
	stream := team.Send(ctx, "start")
	var group sync.WaitGroup
	results := make(chan *adaptor.Result, 20)
	for index := 0; index < 20; index++ {
		group.Go(func() { result, _ := stream.Result(); results <- result })
	}
	group.Wait()
	close(results)
	var first *adaptor.Result
	for result := range results {
		if first == nil {
			first = result
		}
		if !reflect.DeepEqual(first, result) || first != result {
			t.Fatal("Result not stable")
		}
	}
	collect(t, stream)
	if err := stream.Send(ctx, "too late"); !errors.Is(err, adaptor.ErrRunFinished) {
		t.Fatal(err)
	}
}
