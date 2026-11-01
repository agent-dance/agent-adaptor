package team

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	clienta2a "github.com/agent-dance/agent-adaptor/v2/clients/a2a"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type handlerTransport struct{ handler http.Handler }

func (h handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	var request map[string]any
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		_ = json.Unmarshal(raw, &request)
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, req)
	if request != nil && recorder.Header().Get("Content-Type") == "application/json" {
		var response map[string]any
		if json.Unmarshal(recorder.Body.Bytes(), &response) == nil {
			response["id"] = request["id"]
			if request["method"] == "SendMessage" {
				response["result"] = map[string]any{"task": response["result"]}
			}
			recorder.Body.Reset()
			_ = json.NewEncoder(recorder.Body).Encode(response)
		}
	}
	return recorder.Result(), nil
}
func remoteFixture(t *testing.T, handler http.HandlerFunc) *RemoteSpec {
	t.Helper()
	transport := handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"name":"Remote","description":"test","version":"1","supportedInterfaces":[{"url":"https://remote.test/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{"streaming":false},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"work","name":"Work","description":"work","tags":["test"]}]}`)
			return
		}
		handler(w, r)
	})}
	return &RemoteSpec{AgentCardURL: "https://remote.test", Auth: clienta2a.BearerToken("secret"), HTTPClient: &http.Client{Transport: transport}, Policy: RemotePolicy{PollInterval: time.Nanosecond}}
}
func remoteRPC(r *http.Request) (string, map[string]any) {
	var req struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	return req.Method, req.Params
}
func remoteReply(w http.ResponseWriter, state string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"id":"remote-task","contextId":"remote-context","status":{"state":%q},"artifacts":[{"artifactId":"a","parts":[{"text":"remote answer"}]}]}}`, state)
}
func TestRemoteTeamSteerAndCompletion(t *testing.T) {
	ctx := testContext(t)
	steered := make(chan struct{})
	var once sync.Once
	spec := remoteFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing remote auth")
		}
		method, params := remoteRPC(r)
		switch method {
		case "SendMessage":
			msg := params["message"].(map[string]any)
			if task, _ := msg["taskId"].(string); task != "" {
				if task != "remote-task" || msg["contextId"] != "remote-context" {
					t.Errorf("wrong remote identity: %v", msg)
				}
				parts, _ := json.Marshal(msg["parts"])
				if !strings.Contains(string(parts), "no human authority") {
					t.Error("missing peer authority framing")
				}
				once.Do(func() { close(steered) })
			}
			remoteReply(w, "TASK_STATE_WORKING")
		case "GetTask":
			select {
			case <-steered:
				remoteReply(w, "TASK_STATE_COMPLETED")
			case <-r.Context().Done():
				http.Error(w, "cancelled", 499)
			}
		default:
			t.Errorf("unexpected %s", method)
			http.Error(w, "unsupported", 400)
		}
	})
	var team *Team
	var turns atomic.Int32
	leader := &fakeDriver{run: func(_ context.Context, request driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turns.Add(1) == 1 {
			task, err := call(team, request, "delegate", map[string]any{"member": "remote", "objective": "work"})
			if err != nil {
				return driver.Response{}, err
			}
			if _, err := call(team, request, "send", map[string]any{"task": task["task"], "text": "adjust"}); err != nil {
				return driver.Response{}, err
			}
		}
		return answer("leader", 1), nil
	}}
	store := NewMemoryStore()
	team = fixture(t, Config{Leader: adaptor.New(leader), Store: store, Members: []Member{{Key: "remote", Remote: spec}}})
	events, result, err := collect(t, team.Thread("scope").Send(ctx, "start"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Subagents()) != 1 || result.Subagents()[0].State != "succeeded" {
		t.Fatalf("subagents=%v", result.Subagents())
	}
	tasks, err := store.Tasks(ctx, "scope")
	if err != nil || len(tasks) != 1 || tasks[0].RemoteTask != "remote-task" || tasks[0].RemoteContext != "remote-context" || tasks[0].RemoteSnapshot == nil || len(tasks[0].RemoteSnapshot.Artifacts) != 1 {
		t.Fatal(tasks, err)
	}
	seen := false
	for _, e := range events {
		if u, ok := e.(adaptor.SubagentUpdate); ok && u.Member == "remote" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("remote progress missing")
	}
}
func TestRemoteRejectionAndUnknownState(t *testing.T) {
	for _, reject := range []bool{true, false} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			ctx := testContext(t)
			attempted := make(chan struct{})
			var attemptOnce sync.Once
			spec := remoteFixture(t, func(w http.ResponseWriter, r *http.Request) {
				method, params := remoteRPC(r)
				if method == "SendMessage" {
					if params["message"].(map[string]any)["taskId"] != nil && reject {
						attemptOnce.Do(func() { close(attempted) })
						remoteReply(w, "TASK_STATE_REJECTED")
						return
					}
					remoteReply(w, "TASK_STATE_WORKING")
					return
				}
				if reject {
					select {
					case <-attempted:
					case <-r.Context().Done():
						return
					}
					remoteReply(w, "TASK_STATE_COMPLETED")
				} else {
					http.Error(w, "disconnected", 503)
				}
			})
			remote, err := prepareRemote(*spec)
			if err != nil {
				t.Fatal(err)
			}
			if !reject {
				remote.observationTimeout = time.Nanosecond
			}
			run := remote.Send(ctx, "work")
			if reject {
				// Hold polling until the explicit message is attempted, rather than racing
				// a completed task; the returned server rejection is never fake success.
				err = run.Send(ctx, "steer", adaptor.MessageID("exact"))
				var message *adaptor.MessageError
				if err == nil || !errors.As(err, &message) || message.ID != "exact" || !strings.Contains(err.Error(), "rejected message/send") {
					t.Fatalf("Send=%v", err)
				}
			}
			_, err = run.Result()
			if !reject && !errors.Is(err, errRemoteUnknown) {
				t.Fatalf("Result=%v", err)
			}
			for range run.Events() {
			}
		})
	}
}

func TestRemoteUnknownStateWakesLeader(t *testing.T) {
	ctx := testContext(t)
	spec := remoteFixture(t, func(w http.ResponseWriter, r *http.Request) {
		method, _ := remoteRPC(r)
		if method == "SendMessage" {
			remoteReply(w, "TASK_STATE_WORKING")
		} else {
			http.Error(w, "disconnected", 503)
		}
	})
	var team *Team
	var calls atomic.Int32
	var notified atomic.Bool
	leader := adaptor.New(&fakeDriver{run: func(_ context.Context, r driver.Request, _ driver.EventSink) (driver.Response, error) {
		if calls.Add(1) == 1 {
			if _, err := call(team, r, "delegate", map[string]any{"member": "remote", "objective": "work"}); err != nil {
				return driver.Response{}, err
			}
		}
		if strings.Contains(r.Prompt, "unknown_state") {
			notified.Store(true)
		}
		return answer("leader", 1), nil
	}})
	team = fixture(t, Config{Leader: leader, Members: []Member{{Key: "remote", Remote: spec}}})
	team.members["remote"].Runner.(*remoteRunner).observationTimeout = time.Nanosecond
	_, result, err := collect(t, team.Send(ctx, "start"))
	if err != nil || !notified.Load() || len(result.Subagents()) != 1 || result.Subagents()[0].State != "failed" {
		t.Fatalf("notified=%v result=%+v err=%v", notified.Load(), result, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestRemoteStreamingContract(t *testing.T) {
	ctx := testContext(t)
	spec := remoteFixture(t, func(w http.ResponseWriter, r *http.Request) {
		method, _ := remoteRPC(r)
		if method != "SendStreamingMessage" {
			t.Errorf("unexpected %s", method)
			http.Error(w, "unexpected", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, state := range []string{"TASK_STATE_WORKING", "TASK_STATE_COMPLETED"} {
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"task\":{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":%q},\"artifacts\":[{\"artifactId\":\"a\",\"parts\":[{\"text\":\"streamed\"}]}]}}}\n\n", state)
		}
	})
	base := spec.HTTPClient.Transport
	spec.HTTPClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := base.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		if r.URL.Path == "/.well-known/agent-card.json" {
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil {
				return nil, err
			}
			body = bytes.ReplaceAll(body, []byte(`"streaming":false`), []byte(`"streaming":true`))
			response.Body = io.NopCloser(bytes.NewReader(body))
			response.ContentLength = int64(len(body))
		}
		return response, nil
	})
	spec.Policy.RequireStreaming = true
	runner, err := prepareRemote(*spec)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.Send(ctx, "work")
	result, err := run.Result()
	if err != nil || result.Text != "streamed" {
		t.Fatalf("Result=%+v err=%v", result, err)
	}
	count := 0
	for event := range run.Events() {
		if _, ok := event.(adaptor.SubagentUpdate); ok {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("progress count=%d", count)
	}
}

func TestRemoteInputRequiredUsesRemotePolicy(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			spec := remoteFixture(t, func(w http.ResponseWriter, r *http.Request) {
				method, _ := remoteRPC(r)
				if method == "SendMessage" {
					remoteReply(w, "TASK_STATE_INPUT_REQUIRED")
				} else {
					remoteReply(w, "TASK_STATE_COMPLETED")
				}
			})
			spec.Policy.AllowInputRequired = allow
			runner, err := prepareRemote(*spec)
			if err != nil {
				t.Fatal(err)
			}
			run := runner.Send(testContext(t), "work")
			_, err = run.Result()
			if (err == nil) != allow {
				t.Fatalf("allow=%v err=%v", allow, err)
			}
			waiting := false
			for event := range run.Events() {
				if update, ok := event.(adaptor.SubagentUpdate); ok && update.State == string(WaitingApproval) {
					waiting = true
				}
				if _, ok := event.(*adaptor.ApprovalRequest); ok {
					t.Fatal("fabricated an unbound approval")
				}
			}
			if !waiting {
				t.Fatal("missing waiting approval state")
			}
		})
	}
}

func TestRemoteWorkingClearsWaitingApproval(t *testing.T) {
	ctx := testContext(t)
	leader := adaptor.New(&fakeDriver{run: func(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
		return answer("leader", 1), nil
	}})
	team := fixture(t, Config{Leader: leader})
	store := NewMemoryStore()
	lease, err := store.AcquireScope(ctx, "remote-state", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.SaveTask(ctx, lease, Task{Scope: lease.Scope, ID: "remote", Member: "remote", State: Queued})
	if err != nil {
		t.Fatal(err)
	}
	record.State = Running
	record, err = store.SaveTask(ctx, lease, record)
	if err != nil {
		t.Fatal(err)
	}
	run := &execution{team: team, store: store, ctx: ctx, lease: lease, scope: lease.Scope, log: newEventLog("run", lease.Scope, false), tasks: map[string]*taskRun{"remote": {record: record, active: true}}}
	// remoteRun.execute reports normalized waiting_approval for input-required,
	// then reports the observed A2A working state once remote input is supplied.
	run.onEvent(childEvent{task: "remote", event: adaptor.SubagentUpdate{Kind: adaptor.SubagentState, State: string(WaitingApproval)}})
	if got := run.tasks["remote"].record.State; got != WaitingApproval {
		t.Fatalf("remote input requirement was not recorded: %s", got)
	}
	run.onEvent(childEvent{task: "remote", event: adaptor.SubagentUpdate{Kind: adaptor.SubagentState, State: string(clienta2a.TaskStateWorking)}})
	if got := run.tasks["remote"].record.State; got != Running {
		t.Fatalf("remote resumed working but team_status and TaskStore remain %s", got)
	}
	if run.fatal != nil {
		t.Fatal(run.fatal)
	}
	tasks, err := store.Tasks(ctx, lease.Scope)
	if err != nil || len(tasks) != 1 || tasks[0].State != Running {
		t.Fatalf("resumed remote state was not persisted: tasks=%+v err=%v", tasks, err)
	}
	reply := make(chan toolResponse, 1)
	grant := capability{Actor: "leader", token: "remote-state-status", run: run}
	team.grantsMu.Lock()
	team.grants[grant.token] = grant
	team.grantsMu.Unlock()
	run.handleTool(toolCommand{ctx: ctx, grant: grant, name: "status", arguments: json.RawMessage(`{}`), reply: reply})
	status := <-reply
	if status.code != "" {
		t.Fatalf("team_status failed: %+v", status)
	}
	if tasks := status.value.(map[string]any)["tasks"].([]map[string]any); len(tasks) != 1 || tasks[0]["state"] != Running {
		t.Fatalf("team_status retained stale remote state: %+v", tasks)
	}
	run.log.finish()
	resumed := false
	for event := range run.log.events() {
		if update, ok := event.(adaptor.SubagentUpdate); ok && update.Data["nested_state"] == string(clienta2a.TaskStateWorking) {
			resumed = true
			if update.State != string(Running) || update.Task != "remote" || update.Member != "remote" {
				t.Fatalf("forwarded remote event retained stale state: %+v", update)
			}
		}
	}
	if !resumed {
		t.Fatal("missing forwarded remote working event")
	}
}
