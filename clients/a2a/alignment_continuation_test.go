package a2a

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAlignmentStreamSnapshotsAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, stale, live, recovered string
		broken                       bool
		newQuestion                  bool
		want                         int
		wantError                    bool
	}{
		{name: "old question then working and completed", stale: "TASK_STATE_INPUT_REQUIRED", live: "TASK_STATE_COMPLETED", want: 3},
		{name: "old completed then failed", stale: "TASK_STATE_COMPLETED", live: "TASK_STATE_FAILED", want: 3},
		{name: "old failed then completed", stale: "TASK_STATE_FAILED", live: "TASK_STATE_COMPLETED", want: 3},
		{name: "old question then new question", stale: "TASK_STATE_INPUT_REQUIRED", live: "TASK_STATE_INPUT_REQUIRED", want: 3},
		{name: "old question then EOF", stale: "TASK_STATE_INPUT_REQUIRED", want: 1},
		{name: "old completed then EOF", stale: "TASK_STATE_COMPLETED", want: 1},
		{name: "broken then recovered completed", stale: "TASK_STATE_INPUT_REQUIRED", broken: true, recovered: "TASK_STATE_COMPLETED", want: 2},
		{name: "broken continuation without history cannot invent question", broken: true, recovered: "TASK_STATE_INPUT_REQUIRED", wantError: true},
		{name: "broken then recovered new question", stale: "TASK_STATE_INPUT_REQUIRED", broken: true, recovered: "TASK_STATE_INPUT_REQUIRED", newQuestion: true, want: 2},
		{name: "broken cannot recover old question", stale: "TASK_STATE_INPUT_REQUIRED", broken: true, recovered: "TASK_STATE_INPUT_REQUIRED", want: 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/agent-card.json" {
					fmt.Fprintf(w, `{"name":"fixture","description":"fixture","version":"1","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],"capabilities":{"streaming":true},"defaultInputModes":["text/plain"],"defaultOutputModes":["text/plain"],"skills":[{"id":"chat","name":"Chat","description":"chat","tags":["chat"]}]}`, srv.URL+"/a2a")
					return
				}
				switch readRPCMethod(t, r) {
				case "SendStreamingMessage":
					var frames []string
					if tc.stale != "" {
						frames = append(frames, rpcResult(`{"task":`+taskJSON(tc.stale)+`}`))
					}
					if tc.live != "" {
						frames = append(frames, rpcResult(statusUpdateJSON("TASK_STATE_WORKING")), rpcResult(statusUpdateJSON(tc.live)))
					}
					if tc.broken {
						writeBrokenSSE(t, w, append(frames, `{"jsonrpc":`)...)
					} else {
						writeSSE(t, w, frames...)
					}
				case "GetTask":
					task := taskJSON(tc.recovered)
					if tc.newQuestion {
						task = strings.ReplaceAll(strings.ReplaceAll(task, "msg-agent", "new-question"), "done", "new question")
					}
					writeRPCResult(t, w, task)
				default:
					t.Error("unexpected RPC")
				}
			}))
			defer srv.Close()
			c := New(Options{AgentCardURL: srv.URL})
			msg := UserText("answer")
			msg.TaskID = "task-1"
			s, err := c.SendStream(context.Background(), SendRequest{Message: msg})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var events []Event
			for {
				e, err := s.Recv()
				if err != nil {
					if (err != io.EOF) != tc.wantError {
						t.Fatalf("stream error=%v, want error=%v", err, tc.wantError)
					}
					break
				}
				events = append(events, e)
			}
			if len(events) != tc.want {
				t.Fatalf("events=%+v, want %d", events, tc.want)
			}
			if tc.recovered != "" && !tc.wantError && !events[len(events)-1].RecoveredState {
				t.Fatal("missing explicit recovery marker")
			}
		})
	}
}
