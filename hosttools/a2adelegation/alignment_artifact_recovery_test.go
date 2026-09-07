package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func TestAlignmentRecoveredArtifactReconciliation(t *testing.T) {
	text := func(s string) clienta2a.Part { return clienta2a.Part{Kind: clienta2a.PartText, Text: s} }
	data := func(n int) clienta2a.Part {
		return clienta2a.Part{Kind: clienta2a.PartData, Data: map[string]any{"n": n, "private": "payload-secret"}}
	}
	file := func(url string) clienta2a.Part {
		return clienta2a.Part{Kind: clienta2a.PartURL, URL: url, Filename: "result.bin", MediaType: "application/octet-stream"}
	}
	raw := func(b string) clienta2a.Part {
		return clienta2a.Part{Kind: clienta2a.PartRaw, Raw: []byte(b), Filename: "result.bin"}
	}
	for _, tc := range []struct {
		name               string
		live, recovered    []clienta2a.Part
		keepLive, conflict bool
	}{
		{name: "text completes within same part", live: []clienta2a.Part{text("first")}, recovered: []clienta2a.Part{text("firstsecond")}},
		{name: "text chunk boundaries differ", live: []clienta2a.Part{text("first"), text("second")}, recovered: []clienta2a.Part{text("firstsecondthird")}},
		{name: "lagging text prefix", live: []clienta2a.Part{text("first"), text("second")}, recovered: []clienta2a.Part{text("first")}, keepLive: true},
		{name: "equal normalized text", live: []clienta2a.Part{text("first"), text("second")}, recovered: []clienta2a.Part{text("firstsecond")}},
		{name: "mixed data text file extension", live: []clienta2a.Part{data(1), text("first")}, recovered: []clienta2a.Part{data(1), text("firstsecond"), file("https://fixture.invalid/secret-file")}},
		{name: "lagging structured sequence", live: []clienta2a.Part{data(1), data(2)}, recovered: []clienta2a.Part{data(1)}, keepLive: true},
		{name: "structured replacement", live: []clienta2a.Part{data(1)}, recovered: []clienta2a.Part{data(2)}, conflict: true},
		{name: "file replacement", live: []clienta2a.Part{file("https://fixture.invalid/live-secret")}, recovered: []clienta2a.Part{file("https://fixture.invalid/query-secret")}, conflict: true},
		{name: "raw file replacement", live: []clienta2a.Part{raw("observed-secret")}, recovered: []clienta2a.Part{raw("recovered-secret")}, conflict: true},
		{name: "mixed nonprefix replacement", live: []clienta2a.Part{data(1), text("observed-secret")}, recovered: []clienta2a.Part{data(1), text("recovered-secret"), file("https://fixture.invalid/secret-file")}, conflict: true},
		{name: "text metadata boundary is significant", live: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "first", Metadata: map[string]any{"scope": "observed-secret"}}}, recovered: []clienta2a.Part{text("firstsecond")}, conflict: true},
	} {
		for _, mode := range []string{"transport error", "marked recovery", "live terminal query"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
				registry, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card})
				if err != nil {
					t.Fatal(err)
				}
				initial := clienta2a.Task{ID: "task", ContextID: "context", Status: alignmentQuestion("answered")}
				recovered := clienta2a.Task{ID: initial.ID, ContextID: initial.ContextID, Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{{ID: "a", Name: "complete", Parts: tc.recovered}}}
				stream := &fakeA2AStream{events: make(chan streamRecv, 3), closed: make(chan struct{})}
				stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTask, TaskID: initial.ID, ContextID: initial.ContextID, Task: &initial}}
				stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: initial.ID, ContextID: initial.ContextID, Artifact: &clienta2a.Artifact{ID: "a", Name: "partial", Parts: tc.live}}}
				switch mode {
				case "transport error":
					stream.events <- streamRecv{err: errors.New("fixture stream interrupted")}
				case "marked recovery":
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: initial.ID, ContextID: initial.ContextID, Task: &recovered, RecoveredState: true}}
				case "live terminal query":
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: initial.ID, ContextID: initial.ContextID, Status: &recovered.Status}}
				}
				close(stream.events)
				client := &fakeA2AClient{card: card, stream: stream, getTasks: []clienta2a.Task{recovered}}
				bus := NewEventBus(64)
				delegator := NewDelegator(registry, bus)
				delegator.NewClient = func(RemoteAgentSpec) A2AClient { return client }
				result, err := delegator.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "recover", IncludeRemoteArtifacts: true})
				if err != nil || result.Status != "completed" {
					t.Fatalf("recovery failed: %+v %v", result, err)
				}
				if len(result.RemoteArtifacts) != 1 {
					t.Fatalf("artifacts=%+v", result.RemoteArtifacts)
				}
				want := tc.recovered
				if tc.keepLive {
					want = tc.live
				}
				expectedName := "complete"
				if tc.keepLive {
					expectedName = "partial"
				}
				if result.RemoteArtifacts[0].Name != expectedName {
					t.Fatalf("artifact metadata did not follow selected view: %+v", result.RemoteArtifacts[0])
				}
				got := result.RemoteArtifacts[0].Parts
				if len(got) != len(want) {
					t.Fatalf("parts=%+v want %+v", got, want)
				}
				for i := range want {
					expected := cloneRemoteArtifact(clienta2a.Artifact{Parts: []clienta2a.Part{want[i]}}).Parts[0]
					if !reflect.DeepEqual(got[i], expected) {
						t.Fatalf("part %d=%+v want %+v", i, got[i], expected)
					}
				}
				events := drainAvailableBus(t, bus, "run")
				conflicts := 0
				liveSeen := false
				for _, event := range events {
					if event.Kind == DelegationArtifactCreated && event.Artifact != nil && event.Artifact.Name == "partial" {
						liveSeen = true
					}
					if event.Kind == DelegationStreamDropped && event.Raw["reason"] == "artifact_recovery_conflict" {
						conflicts++
						if !liveSeen {
							t.Fatal("conflict erased or preceded observed live history")
						}
						if event.Raw["resolution"] != "recovered_snapshot" || event.RemoteArtifactID != "a" {
							t.Fatalf("conflict=%+v", event)
						}
						encoded, _ := json.Marshal(event)
						if strings.Contains(string(encoded), "secret") {
							t.Fatalf("conflict leaks payload: %s", encoded)
						}
					}
				}
				wantConflicts := 0
				if tc.conflict {
					wantConflicts = 1
				}
				if conflicts != wantConflicts {
					t.Fatalf("conflicts=%d want %d events=%+v", conflicts, wantConflicts, events)
				}
				if !liveSeen || client.cancelCalls != 0 {
					t.Fatalf("live history=%v cancel=%d", liveSeen, client.cancelCalls)
				}
			})
		}
	}
}
