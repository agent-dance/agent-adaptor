package a2adelegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	bridgea2a "github.com/agent-dance/agent-adaptor/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/clients/a2a"
)

func TestAlignmentArtifactPartsDeclaration(t *testing.T) {
	if _, ok := reflect.TypeOf(DelegationArtifact{}).FieldByName("Parts"); !ok {
		t.Fatal("live artifact DTO discards all remote Parts")
	}
	for _, name := range []string{"Append", "LastChunk"} {
		if _, ok := reflect.TypeOf(DelegationEvent{}).FieldByName(name); !ok {
			t.Errorf("live artifact DTO discards %s", name)
		}
	}
}

func TestAlignmentArtifactMappingDeepCopy(t *testing.T) {
	data := map[string]any{"nested": []any{map[string]any{"value": "original"}}}
	parts := cloneRemoteParts([]clienta2a.Part{{Kind: clienta2a.PartData, Data: data}})
	data["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
	if got := parts[0].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("mapped Parts share mutable Data: %v", got)
	}
}

func TestAlignmentArtifactEventBusDeepCopy(t *testing.T) {
	bus := NewEventBus(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, second := bus.SubscribeRun(ctx, "run"), bus.SubscribeRun(ctx, "run")
	source := DelegationEvent{RunID: "run", Kind: DelegationArtifactCreated, Artifact: &DelegationArtifact{ID: "a", Metadata: map[string]any{"nested": []any{map[string]any{"value": "original"}}}}}
	bus.Publish(source)
	source.Artifact.Metadata["nested"].([]any)[0].(map[string]any)["value"] = "source changed"
	a, b := <-first, <-second
	value := func(e DelegationEvent) any { return e.Artifact.Metadata["nested"].([]any)[0].(map[string]any)["value"] }
	if value(a) != "original" {
		t.Fatalf("published event aliases source: %v", value(a))
	}
	a.Artifact.Metadata["nested"].([]any)[0].(map[string]any)["value"] = "subscriber changed"
	if value(b) != "original" {
		t.Fatalf("subscribers share event: %v", value(b))
	}
	replay := <-bus.SubscribeRun(ctx, "run")
	if value(replay) != "original" {
		t.Fatalf("replay shares event: %v", value(replay))
	}
}

func alignmentArtifactFixture() clienta2a.Artifact {
	return clienta2a.Artifact{ID: "artifact", Name: "result", Description: "deliverable", Extensions: []string{"fixture.v1"},
		Metadata: map[string]any{"nested": []any{map[string]any{"value": "original"}}},
		Raw:      map[string]any{"protocol": map[string]any{"secret": "protocol-only"}},
		Parts: []clienta2a.Part{
			{Kind: clienta2a.PartText, Text: "first"},
			{Kind: clienta2a.PartData, Data: map[string]any{"nested": []any{map[string]any{"value": "original"}}}, Metadata: map[string]any{"nested": []any{"original"}}},
			{Kind: clienta2a.PartURL, URL: "https://fixture.invalid/deliverable", Filename: "remote.bin", MediaType: "application/octet-stream"},
			{Kind: clienta2a.PartRaw, Raw: []byte("file-bytes"), Filename: "inline.bin", MediaType: "application/octet-stream"},
		},
	}
}

func TestAlignmentArtifactUpdates(t *testing.T) {
	source := alignmentArtifactFixture()
	history := clienta2a.Task{ID: "task", ContextID: "context", Status: alignmentQuestion("answered"), Artifacts: []clienta2a.Artifact{{ID: source.ID, Name: "history", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "old"}}}}}
	replacement := cloneA2AArtifact(source)
	replacement.Parts[0].Text = "replacement"
	tail := clienta2a.Artifact{ID: source.ID, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "tail"}}}
	stream := &fakeA2AStream{events: make(chan streamRecv, 7), closed: make(chan struct{})}
	for _, ev := range []clienta2a.Event{
		{Kind: clienta2a.EventTask, Task: &history},
		{Kind: clienta2a.EventArtifact, Artifact: &source},
		{Kind: clienta2a.EventArtifact, Artifact: &replacement},
		{Kind: clienta2a.EventTask, Task: &history},
		{Kind: clienta2a.EventArtifact, Artifact: &tail, Append: true},
		{Kind: clienta2a.EventArtifact, Artifact: &clienta2a.Artifact{ID: source.ID}, Append: true, LastChunk: true},
		{Kind: clienta2a.EventTerminal, Status: &clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}},
	} {
		ev.TaskID, ev.ContextID = history.ID, history.ContextID
		stream.events <- streamRecv{event: ev}
	}
	close(stream.events)
	card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: true}}
	reg, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card})
	if err != nil {
		t.Fatal(err)
	}
	bus := NewEventBus(64)
	d := NewDelegator(reg, bus)
	d.NewClient = func(RemoteAgentSpec) A2AClient {
		return &fakeA2AClient{card: card, stream: stream, getErr: errors.New("no later snapshot")}
	}
	result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "answer", IncludeRemoteArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemoteArtifacts) != 1 || len(result.Artifacts) != 1 || len(result.Artifacts[0].Parts) != 0 {
		t.Fatalf("final projection duplicates artifacts: %+v", result)
	}
	full := result.RemoteArtifacts[0]
	if len(full.Parts) != 5 || full.Parts[0].Text != "replacement" || full.Parts[4].Text != "tail" || full.Name != "result" {
		t.Fatalf("wrong cumulative final result: %+v", full)
	}
	var updates []DelegationEvent
	for _, ev := range drainAvailableBus(t, bus, "run") {
		if ev.Kind == DelegationArtifactCreated {
			updates = append(updates, ev)
		}
		if ev.Kind == DelegationInputRequired || (ev.Kind == DelegationStatus && ev.Status == string(clienta2a.TaskStateInputRequired)) {
			t.Fatal("old question was replayed")
		}
	}
	if len(updates) != 5 {
		t.Fatalf("updates=%d, expected historical plus four live updates", len(updates))
	}
	if len(updates[1].Artifact.Parts) != 4 || updates[1].Artifact.Parts[0].Text != "first" || updates[1].Append {
		t.Fatalf("first live update changed: %+v", updates[1])
	}
	if len(updates[2].Artifact.Parts) != 4 || updates[2].Artifact.Parts[0].Text != "replacement" || updates[2].Append {
		t.Fatalf("replacement changed: %+v", updates[2])
	}
	if len(updates[3].Artifact.Parts) != 1 || updates[3].Artifact.Parts[0].Text != "tail" || !updates[3].Append || updates[3].LastChunk {
		t.Fatalf("append contains accumulated parts: %+v", updates[3])
	}
	if !updates[4].Append || !updates[4].LastChunk || len(updates[4].Artifact.Parts) != 0 {
		t.Fatalf("empty closing update lost: %+v", updates[4])
	}
	source.Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed"
	source.Parts[3].Raw[0] = 'X'
	replacement.Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "changed"
	if updates[1].Artifact.Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] != "original" || string(updates[1].Artifact.Parts[3].Raw) != "file-bytes" || full.Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatal("source mutation changed published or final content")
	}
	full.Parts[3].Raw[0] = 'Y'
	if string(updates[2].Artifact.Parts[3].Raw) != "file-bytes" {
		t.Fatal("result and live event share bytes")
	}
}

func TestAlignmentArtifactProjectionBoundary(t *testing.T) {
	for _, include := range []bool{false, true} {
		t.Run(fmt.Sprint(include), func(t *testing.T) {
			artifact := alignmentArtifactFixture()
			mapper := newEventMapper(DelegationEvent{RunID: "run"})
			mapper.includeRemoteArtifacts = include
			ev := mapper.Map(clienta2a.Event{Kind: clienta2a.EventArtifact, Artifact: &artifact})[1]
			result := resultFromTask(DelegationResult{}, clienta2a.Task{Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{artifact}}, include)
			if len(result.Artifacts) != 1 || result.Artifacts[0].Parts != nil {
				t.Fatal("compact final artifacts acquired hidden content")
			}
			if !include {
				if ev.Artifact.Parts != nil || result.RemoteArtifacts != nil || ev.Raw["parts_omitted"] != "remote_artifacts_not_requested" {
					t.Fatalf("default projection/degradation: %+v %+v", ev, result)
				}
				raw, _ := json.Marshal(ev)
				for _, hidden := range []string{"first", "file-bytes", "protocol-only"} {
					if strings.Contains(string(raw), hidden) {
						t.Fatalf("default leaks %q", hidden)
					}
				}
			} else {
				if len(ev.Artifact.Parts) != 4 || !reflect.DeepEqual(ev.Artifact.Parts, result.RemoteArtifacts[0].Parts) {
					t.Fatal("event and final full projection disagree")
				}
				if ev.Artifact.URI != artifact.Parts[2].URL || ev.Artifact.MediaType != artifact.Parts[2].MediaType {
					t.Fatal("compact file reference regressed")
				}
				if ev.Raw["protocol"].(map[string]any)["secret"] != "protocol-only" || ev.Artifact.Parts[1].Data.(map[string]any)["protocol"] != nil {
					t.Fatal("protocol Raw mixed into Parts")
				}
			}
		})
	}
}

func TestAlignmentArtifactSizePolicy(t *testing.T) {
	text := func(s string) clienta2a.Artifact {
		return clienta2a.Artifact{ID: "a", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: s}}}
	}
	for _, tc := range []struct {
		name          string
		limit         int64
		first, second clienta2a.Artifact
		append        bool
		code          string
	}{
		{name: "exact boundary", limit: 4, first: text("1234")},
		{name: "one over", limit: 4, first: text("12345"), code: "artifact_too_large"},
		{name: "append exact", limit: 4, first: text("12"), second: text("34"), append: true},
		{name: "append over", limit: 4, first: text("12"), second: text("345"), append: true, code: "artifact_too_large"},
		{name: "replacement resets", limit: 4, first: text("1234"), second: text("345")},
		{name: "metadata bounded", limit: 4, first: clienta2a.Artifact{ID: "a", Metadata: map[string]any{"secret": "do-not-diagnose"}}, code: "artifact_too_large"},
		{name: "raw bounded", limit: 4, first: clienta2a.Artifact{ID: "a", Raw: map[string]any{"secret": "do-not-diagnose"}}, code: "artifact_too_large"},
		{name: "data bounded", limit: 4, first: clienta2a.Artifact{ID: "a", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"secret": "do-not-diagnose"}}}}, code: "artifact_too_large"},
		{name: "inline bounded", limit: 4, first: clienta2a.Artifact{ID: "a", Parts: []clienta2a.Part{{Kind: clienta2a.PartRaw, Raw: []byte("do-not-diagnose")}}}, code: "artifact_too_large"},
		{name: "invalid without limit", first: clienta2a.Artifact{ID: "a", Parts: []clienta2a.Part{{Kind: clienta2a.PartData, Data: map[string]any{"secret": "do-not-diagnose", "bad": make(chan int)}}}}, code: "artifact_invalid"},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, streaming), func(t *testing.T) {
				task := clienta2a.Task{ID: "task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}}
				mergeStreamArtifact(&task, tc.first, false)
				if tc.second.ID != "" {
					mergeStreamArtifact(&task, tc.second, tc.append)
				}
				card := clienta2a.AgentCard{Name: "fixture", Capabilities: clienta2a.Capabilities{Streaming: streaming}}
				reg, _ := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card, Policy: DelegationPolicy{MaxArtifactBytes: tc.limit}})
				client := &fakeA2AClient{card: card, sendTask: task, getTasks: []clienta2a.Task{task}}
				if streaming {
					stream := &fakeA2AStream{events: make(chan streamRecv, 3), closed: make(chan struct{})}
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: "task", Artifact: &tc.first}}
					if tc.second.ID != "" {
						stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventArtifact, TaskID: "task", Artifact: &tc.second, Append: tc.append, LastChunk: true}}
					}
					stream.events <- streamRecv{event: clienta2a.Event{Kind: clienta2a.EventTerminal, TaskID: "task", Status: &task.Status}}
					close(stream.events)
					client.stream = stream
				}
				bus := NewEventBus(32)
				d := NewDelegator(reg, bus)
				d.NewClient = func(RemoteAgentSpec) A2AClient { return client }
				result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "bounded output", IncludeRemoteArtifacts: true})
				if tc.code == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var de *DelegationError
					if !errors.As(err, &de) || de.Code != tc.code || len(result.RemoteArtifacts) != 0 {
						t.Fatalf("policy result=%+v error=%v", result, err)
					}
				}
				drops := 0
				for _, ev := range drainAvailableBus(t, bus, "run") {
					if ev.Kind == DelegationStreamDropped {
						drops++
						if ev.Raw["reason"] != tc.code || ev.Artifact != nil || ev.Result != nil || ev.Args != nil {
							t.Fatalf("unsafe or incorrect degradation: %+v", ev)
						}
						raw, marshalErr := json.Marshal(ev)
						if marshalErr != nil || strings.Contains(string(raw), "do-not-diagnose") {
							t.Fatalf("degradation exposed payload: %s %v", raw, marshalErr)
						}
					}
				}
				if (drops > 0) != (tc.code != "") {
					t.Fatalf("drops=%d code=%q", drops, tc.code)
				}
			})
		}
	}
}

func TestAlignmentArtifactServiceIsolation(t *testing.T) {
	source := alignmentArtifactFixture()
	card := clienta2a.AgentCard{Name: "fixture"}
	task := clienta2a.Task{ID: "task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{source}}
	observed := make(chan struct{})
	mutateParts := func(parts []RemotePart) {
		parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
		parts[1].Metadata["nested"].([]any)[0] = "mutated"
		parts[3].Raw[0] = 'X'
	}
	service, err := NewService(Config{
		Agents:    []AgentRef{RemoteAgent(RemoteAgentSpec{Key: "fixture", AgentCard: &card})},
		NewClient: func(RemoteAgentSpec) A2AClient { return &fakeA2AClient{card: card, sendTask: task} },
		Hook: DelegationLifecycleHookFuncs{AfterFunc: func(_ context.Context, after AfterDelegation) error {
			mutateParts(after.Result.RemoteArtifacts[0].Parts)
			return nil
		}},
		Observe: func(ev DelegationEvent) {
			if ev.Kind == DelegationArtifactCreated {
				mutateParts(ev.Artifact.Parts)
				ev.Raw["protocol"].(map[string]any)["secret"] = "mutated"
			}
			if isTerminal(ev.Kind) {
				close(observed)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	result, err := service.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "deliver", IncludeRemoteArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	<-observed
	check := func(parts []RemotePart) bool {
		return len(parts) == 4 && parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] == "original" && parts[1].Metadata["nested"].([]any)[0] == "original" && string(parts[3].Raw) == "file-bytes"
	}
	if !check(result.RemoteArtifacts[0].Parts) {
		t.Fatal("hook or observer mutated returned result")
	}
	source.Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "source mutated"
	source.Raw["protocol"].(map[string]any)["secret"] = "source mutated"
	source.Parts[3].Raw[0] = 'X'
	mutateParts(result.RemoteArtifacts[0].Parts)
	result.RemoteArtifacts[0].Extensions[0] = "mutated"
	result.Artifacts[0].Metadata["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			single, ok := service.Result("run", "fixture")
			many := service.Results("run")["fixture"]
			ordered := service.Delegations("run")
			if !ok || len(ordered) != 1 || !check(single.RemoteArtifacts[0].Parts) || !check(many.RemoteArtifacts[0].Parts) || !check(ordered[0].RemoteArtifacts[0].Parts) {
				t.Error("service result accessor aliases mutable data")
				return
			}
			for _, value := range []DelegationResult{single, many, ordered[0]} {
				mutateParts(value.RemoteArtifacts[0].Parts)
				value.RemoteArtifacts[0].Raw["protocol"].(map[string]any)["secret"] = "mutated"
				value.Artifacts[0].Metadata["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
			}
		}()
	}
	wg.Wait()
	final, _ := service.Result("run", "fixture")
	if !check(final.RemoteArtifacts[0].Parts) || final.RemoteArtifacts[0].Extensions[0] != "fixture.v1" || final.RemoteArtifacts[0].Raw["protocol"].(map[string]any)["secret"] != "protocol-only" || final.Artifacts[0].Metadata["nested"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatal("service owned state mutated")
	}
	for _, ev := range drainAvailableBus(t, service.Bus(), "run") {
		if ev.Kind == DelegationArtifactCreated && (!check(ev.Artifact.Parts) || ev.Raw["protocol"].(map[string]any)["secret"] != "protocol-only") {
			t.Fatal("service observer mutated event replay")
		}
	}
}

func TestAlignmentArtifactNestedTypedValues(t *testing.T) {
	type record struct {
		Values []map[string][]byte
		Ptr    *map[string][]string
	}
	nested := map[string][]string{"key": {"original"}}
	source := record{Values: []map[string][]byte{{"key": []byte("original")}}, Ptr: &nested}
	// This value cannot be JSON encoded. Cloning must still isolate its mutable
	// siblings without using a failed JSON round trip's original-value fallback.
	data := map[string]any{"record": &source, "raw": json.RawMessage(`{"ok":true}`), "unencodable": func() {}}
	out := cloneRemoteParts([]clienta2a.Part{{Kind: clienta2a.PartData, Data: data}})
	source.Values[0]["key"][0] = 'X'
	nested["key"][0] = "mutated"
	data["raw"].(json.RawMessage)[0] = 'X'
	got := out[0].Data.(map[string]any)
	rec := got["record"].(*record)
	if string(rec.Values[0]["key"]) != "original" || (*rec.Ptr)["key"][0] != "original" || string(got["raw"].(json.RawMessage)) != `{"ok":true}` {
		t.Fatal("typed nested mutable data aliases source after non-JSON clone")
	}
}

func TestAlignmentArtifactEventAllPayloadCopies(t *testing.T) {
	value := func() map[string]any {
		return map[string]any{"values": []map[string]any{{"bytes": []byte("original")}}}
	}
	ev := DelegationEvent{RunID: "run", Kind: DelegationArtifactCreated, Args: value(), Result: value(), Raw: value(), Error: &DelegationError{Metadata: value()}, StatusParts: []RemotePart{{Kind: clienta2a.PartData, Data: value()}}, Artifact: &DelegationArtifact{Metadata: value(), Parts: []RemotePart{{Kind: clienta2a.PartData, Data: value(), Metadata: value(), Raw: []byte("original")}}}}
	bus := NewEventBus(64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := bus.SubscribeRun(ctx, "run"), bus.SubscribeRun(ctx, "run")
	bus.Publish(ev)
	mutate := func(x DelegationEvent) {
		for _, v := range []any{x.Args, x.Result, x.Raw, x.Error.Metadata, x.StatusParts[0].Data, x.Artifact.Metadata, x.Artifact.Parts[0].Data, x.Artifact.Parts[0].Metadata} {
			v.(map[string]any)["values"].([]map[string]any)[0]["bytes"].([]byte)[0] = 'X'
		}
		x.Artifact.Parts[0].Raw[0] = 'X'
	}
	mutate(ev)
	mutate(<-a)
	check := func(x DelegationEvent) {
		for _, v := range []any{x.Args, x.Result, x.Raw, x.Error.Metadata, x.StatusParts[0].Data, x.Artifact.Metadata, x.Artifact.Parts[0].Data, x.Artifact.Parts[0].Metadata} {
			if string(v.(map[string]any)["values"].([]map[string]any)[0]["bytes"].([]byte)) != "original" {
				t.Fatal("event payload shared across ownership boundary")
			}
		}
		if string(x.Artifact.Parts[0].Raw) != "original" {
			t.Fatal("inline file bytes shared")
		}
	}
	check(<-b)
	check(<-bus.SubscribeRun(ctx, "run"))
	// Terminal deferral must take ownership before a lifecycle hook can mutate
	// the producer's reusable envelope.
	terminal := ev
	terminal.Kind = DelegationFinished
	buffer := terminalEventBuffer{parent: NewDelegator(nil, NewEventBus(4))}
	terminal.Raw = value()
	buffer.publish(terminal)
	terminal.Raw["values"].([]map[string]any)[0]["bytes"].([]byte)[0] = 'Y'
	if string(buffer.terminal.Raw["values"].([]map[string]any)[0]["bytes"].([]byte)) != "original" {
		t.Fatal("terminal buffer aliases producer")
	}
}

func TestAlignmentArtifactBackpressureClearsPayload(t *testing.T) {
	bus := NewEventBus(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := bus.SubscribeRun(ctx, "run")
	source := DelegationEvent{RunID: "run", Kind: DelegationArtifactCreated, Text: "secret", StatusParts: []RemotePart{{Text: "secret"}}, Error: &DelegationError{Message: "secret"}, Artifact: &DelegationArtifact{Parts: []RemotePart{{Text: "secret"}}}, Raw: map[string]any{"secret": "secret"}}
	for i := 0; i < subscriberBuffer+1; i++ {
		bus.Publish(source)
	}
	found := false
	for i := 0; i < subscriberBuffer; i++ {
		ev := <-ch
		if ev.Kind == DelegationStreamDropped {
			found = true
			raw, err := json.Marshal(ev)
			if err != nil || strings.Contains(string(raw), "secret") {
				t.Fatalf("backpressure leaked artifact payload: %s %v", raw, err)
			}
		}
	}
	if !found {
		t.Fatal("missing backpressure degradation")
	}
}

// A real bridge/client/delegator round trip proves local Parts opt-in cannot
// reopen diagnostics withheld by the remote bridge's ExposurePolicy.
func TestAlignmentArtifactRemoteExposureLoopback(t *testing.T) {
	for _, exposeMetadata := range []bool{false, true} {
		for _, include := range []bool{false, true} {
			t.Run(fmt.Sprintf("metadata=%v/full=%v", exposeMetadata, include), func(t *testing.T) {
				mux := http.NewServeMux()
				server := httptest.NewServer(mux)
				defer server.Close()
				bridge := bridgea2a.NewServer(alignmentArtifactRunner{}, bridgea2a.ServerOptions{
					AgentCard: bridgea2a.AgentCard{Name: "fixture", Version: "1", URL: server.URL + "/rpc", Capabilities: bridgea2a.Capabilities{Streaming: bridgea2a.CapabilityEnabled}},
					Exposure:  bridgea2a.ExposurePolicy{Diagnostics: bridgea2a.DiagnosticsPolicy{IncludeMetadata: exposeMetadata}},
				})
				mux.Handle("/rpc", bridge.Handler())
				mux.Handle("/.well-known/agent-card.json", bridge.AgentCardHandler())
				reg, err := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCardURL: server.URL + "/.well-known/agent-card.json"})
				if err != nil {
					t.Fatal(err)
				}
				bus := NewEventBus(64)
				d := NewDelegator(reg, bus)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := d.Delegate(ctx, DelegationRequest{RunID: "run", Agent: "fixture", Objective: "deliver", IncludeRemoteArtifacts: include})
				if err != nil {
					t.Fatal(err)
				}
				events := drainAvailableBus(t, bus, "run")
				artifacts := 0
				for _, ev := range events {
					if ev.Kind != DelegationArtifactCreated {
						continue
					}
					artifacts++
					if include {
						if !ev.LastChunk || ev.Append || len(ev.Artifact.Parts) != 1 {
							t.Fatalf("wire update flags/parts lost: %+v", ev)
						}
						data := ev.Artifact.Parts[0].Data.(map[string]any)
						if data["summary"] != "safe summary" {
							t.Fatalf("summary=%+v", data)
						}
						_, metadata := data["metadata"]
						if metadata != exposeMetadata {
							t.Fatalf("remote exposure mismatch: %+v", data)
						}
					} else if ev.Artifact.Parts != nil || ev.Raw["parts_omitted"] != "remote_artifacts_not_requested" {
						t.Fatalf("compact stream changed: %+v", ev)
					}
				}
				if artifacts != 1 {
					t.Fatalf("artifact events=%d", artifacts)
				}
				if include {
					if len(result.RemoteArtifacts) != 1 || len(result.RemoteArtifacts[0].Parts) != 1 {
						t.Fatalf("final artifact=%+v", result.RemoteArtifacts)
					}
					_, metadata := result.RemoteArtifacts[0].Parts[0].Data.(map[string]any)["metadata"]
					if metadata != exposeMetadata {
						t.Fatal("event/final exposure differ")
					}
				} else if result.RemoteArtifacts != nil {
					t.Fatal("default final exposes remote details")
				}
				encoded, marshalErr := json.Marshal(struct {
					Events []DelegationEvent
					Result DelegationResult
				}{events, result})
				if marshalErr != nil || strings.Contains(string(encoded), "credential-secret") {
					t.Fatalf("new Parts leaked credentials: %s %v", encoded, marshalErr)
				}
				if !exposeMetadata && strings.Contains(string(encoded), "metadata-visible") {
					t.Fatal("local opt-in reopened hidden metadata")
				}
			})
		}
	}
}

type alignmentArtifactRunner struct{}

func (alignmentArtifactRunner) Run(context.Context, string, ...adaptor.CallOption) (*adaptor.Result, error) {
	return &adaptor.Result{Text: "answer", Summary: "safe summary", Metadata: map[string]string{"label": "metadata-visible", "authorization": "Bearer credential-secret"}}, nil
}
func (r alignmentArtifactRunner) Stream(ctx context.Context, prompt string, opts ...adaptor.CallOption) adaptor.Stream {
	result, _ := r.Run(ctx, prompt, opts...)
	events := make(chan adaptor.Event)
	close(events)
	return &alignmentArtifactResultStream{events: events, result: result}
}

type alignmentArtifactResultStream struct {
	events chan adaptor.Event
	result *adaptor.Result
}

func (s *alignmentArtifactResultStream) Events() <-chan adaptor.Event     { return s.events }
func (s *alignmentArtifactResultStream) Result() (*adaptor.Result, error) { return s.result, nil }
func (s *alignmentArtifactResultStream) RunID() string                    { return "fixture" }
func (s *alignmentArtifactResultStream) Cancel()                          {}

func TestAlignmentArtifactResultCountLimit(t *testing.T) {
	card := clienta2a.AgentCard{Name: "fixture"}
	task := clienta2a.Task{ID: "task", Status: clienta2a.TaskStatus{State: clienta2a.TaskStateCompleted}, Artifacts: []clienta2a.Artifact{{ID: "first", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "one"}}}, {ID: "second", Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "two"}}}}}
	reg, _ := NewRegistry(RemoteAgentSpec{Key: "fixture", AgentCard: &card})
	for _, max := range []int{0, 1, 2} {
		bus := NewEventBus(32)
		d := NewDelegator(reg, bus)
		d.NewClient = func(RemoteAgentSpec) A2AClient { return &fakeA2AClient{card: card, sendTask: task} }
		result, err := d.Delegate(context.Background(), DelegationRequest{RunID: "run", Agent: "fixture", Objective: "deliver", IncludeRemoteArtifacts: true, MaxArtifacts: &max})
		if err != nil || len(result.Artifacts) != max || len(result.RemoteArtifacts) != 2 {
			t.Fatalf("count limit changed full result: %+v %v", result, err)
		}
		updates, drops := 0, 0
		for _, ev := range drainAvailableBus(t, bus, "run") {
			if ev.Kind == DelegationArtifactCreated {
				updates++
				if len(ev.Artifact.Parts) != 1 {
					t.Fatal("count limit truncated live update")
				}
			}
			if ev.Kind == DelegationStreamDropped && ev.Raw["reason"] == "artifact_result_limit" {
				drops++
				if ev.Raw["omitted_count"] != 2-max {
					t.Fatalf("drop count=%+v", ev.Raw)
				}
			}
		}
		if updates != 2 || (drops == 1) != (max < 2) {
			t.Fatalf("updates=%d drops=%d max=%d", updates, drops, max)
		}
	}
}

func TestAlignmentArtifactAppendMetadataBudget(t *testing.T) {
	mapper := newEventMapper(DelegationEvent{RunID: "run"})
	mapper.includeRemoteArtifacts = true
	mapper.maxArtifactBytes = 15
	for i := 0; i < 2; i++ {
		event := clienta2a.Event{Kind: clienta2a.EventArtifact, Append: i > 0, Artifact: &clienta2a.Artifact{ID: "a", Metadata: map[string]any{"tag": "x"}, Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "12"}}}}
		events := mapper.Map(event)
		last := events[len(events)-1]
		if last.Kind != DelegationArtifactCreated || len(last.Artifact.Parts) != 1 {
			t.Fatalf("metadata counted per update instead of cumulative view: %+v", last)
		}
	}
}

func TestAlignmentArtifactResultCloneIncludesParts(t *testing.T) {
	artifact := alignmentArtifactFixture()
	source := DelegationResult{
		Artifacts:       []DelegationArtifact{{ID: "artifact", Parts: cloneRemoteParts(artifact.Parts), Metadata: map[string]any{"nested": []any{"original"}}}},
		RemoteArtifacts: []RemoteArtifact{cloneRemoteArtifact(artifact)},
		RawTask:         map[string]any{"nested": []any{"original"}}, Metadata: map[string]any{"nested": []any{"original"}},
		Error: &DelegationError{Metadata: map[string]any{"nested": []any{"original"}}},
	}
	cloned := cloneDelegationResult(source)
	source.Artifacts[0].Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
	source.Artifacts[0].Parts[3].Raw[0] = 'X'
	source.Artifacts[0].Metadata["nested"].([]any)[0] = "mutated"
	for _, m := range []map[string]any{source.RawTask, source.Metadata, source.Error.Metadata} {
		m["nested"].([]any)[0] = "mutated"
	}
	if cloned.Artifacts[0].Parts[1].Data.(map[string]any)["nested"].([]any)[0].(map[string]any)["value"] != "original" || string(cloned.Artifacts[0].Parts[3].Raw) != "file-bytes" || cloned.Artifacts[0].Metadata["nested"].([]any)[0] != "original" {
		t.Fatal("compact DTO Parts clone shares data")
	}
	for _, m := range []map[string]any{cloned.RawTask, cloned.Metadata, cloned.Error.Metadata} {
		if m["nested"].([]any)[0] != "original" {
			t.Fatal("result auxiliary payload shares data")
		}
	}
}
