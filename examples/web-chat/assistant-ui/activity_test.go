package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	events "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui/aguisession"
)

type exampleWireEvent struct {
	events.Event
	raw json.RawMessage
}

func (e exampleWireEvent) ToJSON() ([]byte, error) { return e.raw, nil }

func assertScriptedActivity(t *testing.T, wire string, history []aguisession.Message) {
	t.Helper()
	var live []aguisession.Message
	snapshots, deltas := 0, 0
	for _, line := range strings.Split(wire, "\n") {
		if strings.HasPrefix(line, "data: ") {
			raw := json.RawMessage(line[6:])
			var event struct{ Type string }
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type == "ACTIVITY_SNAPSHOT" {
				snapshots++
			}
			if event.Type == "ACTIVITY_DELTA" {
				deltas++
			}
			live = aguisession.Fold(live, exampleWireEvent{raw: raw})
		}
	}
	if snapshots != 1 || deltas != 1 {
		t.Fatalf("activity trace: %d snapshots %d deltas", snapshots, deltas)
	}
	activity := func(ms []aguisession.Message) aguisession.Message {
		for _, m := range ms {
			if m["role"] == "activity" {
				return m
			}
		}
		return nil
	}
	got, want := activity(live), activity(history)
	if got == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("live/reload activity differs: %v vs %v", got, want)
	}
	content := got["content"].(map[string]any)
	if content["text"] != "Checked 13 commands; command 13 needs attention." || content["status"] != "completed" {
		t.Fatal(content)
	}
	parent, ok := content["parentToolCallId"].(string)
	if !ok {
		t.Fatal(content)
	}
	for _, m := range history {
		if m["role"] == "tool" && m["toolCallId"] == parent {
			return
		}
	}
	t.Fatal("activity lost its scoped tool association")
}

func TestScriptedActivityLiveReloadAndOrderedEvents(t *testing.T) {
	a := appForTest(t, config{dataDir: t.TempDir()})
	w := postTurn(t, a, "activity", "1", nil)
	history, err := a.session.Messages(context.Background(), aguisession.ThreadKey("activity"))
	if err != nil {
		t.Fatal(err)
	}
	assertScriptedActivity(t, w.Body.String(), history)
	records, err := a.session.Events(context.Background(), aguisession.ThreadKey("activity"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var wire strings.Builder
	for _, r := range records {
		for _, event := range r.Events {
			raw, err := event.ToJSON()
			if err != nil {
				t.Fatal(err)
			}
			wire.WriteString("data: " + string(raw) + "\n\n")
		}
	}
	assertScriptedActivity(t, wire.String(), history)
}
