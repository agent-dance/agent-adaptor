package codebuddy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
)

type alignmentObservationService struct {
	mu     sync.Mutex
	events map[string][]adaptor.Event
}

func (s *alignmentObservationService) AttachRun(_ context.Context, id string) (adaptor.RunAttachment, error) {
	return adaptor.RunAttachment{Observation: adaptor.ObservationDemand{CapabilityInvocations: true, Todos: true}, Observer: func(_ context.Context, _ adaptor.RunEventInfo, e adaptor.Event) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.events[id] = append(s.events[id], e)
		return nil
	}}, nil
}
func (*alignmentObservationService) DetachRun(context.Context, string) error { return nil }
func TestAlignmentObservationPublicRunStreamAndPersistent(t *testing.T) {
	const native = "T15_NATIVE_RAW_NONCE"
	protocol := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`,
		alignmentCall("mcp__knowledge__search", "m", `{"secret":"not a fact"}`),
		alignmentResult("m", `"ok"`, false, ""),
		alignmentCall("TodoWrite", "clear", `{"newTodos":[]}`),
		alignmentResult("clear", `"Todo list updated successfully"`, false, ""),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"intermediate"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"` + native + `","usage":{"input_tokens":0,"output_tokens":0}}`, ""}, "\n")
	path := filepath.Join(t.TempDir(), "protocol")
	if err := os.WriteFile(path, []byte(protocol), 0600); err != nil {
		t.Fatal(err)
	}
	service := &alignmentObservationService{events: map[string][]adaptor.Event{}}
	fx := newPersistentCodeBuddyFixtureWithOptions(t, []driver.EnvBinding{{Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path}}, adaptor.WithAppendSystemPrompt(native), adaptor.WithMCP(mcp.Stdio("knowledge", "fixture-unused-mcp")), adaptor.WithRunServices(service))
	defer fx.close()
	for _, runner := range []adaptor.Runner{fx.agent, fx.thread} {
		first, err := runner.Run(context.Background(), "first")
		if err != nil {
			t.Fatal(err)
		}
		stream := runner.Stream(context.Background(), "second")
		var capCount, todoCount int
		for e := range stream.Events() {
			switch event := e.(type) {
			case adaptor.CapabilityInvocation:
				capCount++
				raw, _ := json.Marshal(event)
				if strings.Contains(string(raw), "secret") {
					t.Fatal("args leaked into fact")
				}
			case adaptor.TodoUpdated:
				todoCount++
				if event.Snapshot.Items == nil || len(event.Snapshot.Items) != 0 || event.Snapshot.Revision != 1 {
					t.Fatal("public clear lost")
				}
			case adaptor.Notice:
				raw, _ := json.Marshal(event)
				if event.Kind != adaptor.NoticeTranscriptItem && strings.Contains(string(raw), native) {
					t.Fatalf("native text leaked into SDK notice kind=%s", event.Kind)
				}
			}
		}
		second, err := stream.Result()
		if err != nil {
			t.Fatal(err)
		}
		if capCount != 2 || todoCount != 1 {
			t.Fatalf("public facts %d/%d", capCount, todoCount)
		}
		for _, result := range []*adaptor.Result{first, second} {
			if result.Text != native || result.Summary != "" || !strings.Contains(result.Raw().Stdout, native) || result.Raw().Terminal == nil || len(result.Transcript()) < 5 || result.Usage == nil || result.Usage.InputTokens != 0 {
				t.Fatalf("output layers changed %#v", result)
			}
		}
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.events) != 4 {
		t.Fatalf("observer runs=%d", len(service.events))
	}
	for _, events := range service.events {
		if len(events) != 3 {
			t.Fatalf("observer facts=%d", len(events))
		}
	}
	if fx.spawnCount(t) != 3 {
		t.Fatal("stateless runs + one reused persistent process expected")
	}
}
