package agui_test

import (
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
)

func TestTypedWorkerStatesReachActivity(t *testing.T) {
	tr := agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "leader"})
	initial := tr.Translate(adaptor.SubagentUpdate{Task: "task", Member: "worker", Kind: adaptor.SubagentState, State: "queued"})
	if len(initial) != 2 {
		t.Fatalf("queued task produced %d events", len(initial))
	}
	if got, ok := patchValue(initial[1], "/status"); !ok || got != "queued" {
		t.Fatalf("queued status=%v, present=%v", got, ok)
	}
	for _, step := range []struct {
		kind  adaptor.SubagentEventKind
		state string
		want  string
	}{
		{adaptor.SubagentStarted, "running", "running"},
		{adaptor.SubagentState, "waiting_approval", "input_required"},
		{adaptor.SubagentState, "running", "running"},
		{adaptor.SubagentFinished, "interrupted", "failed"},
	} {
		events := tr.Translate(adaptor.SubagentUpdate{Task: "task", Member: "worker", Kind: step.kind, State: step.state})
		if len(events) != 1 {
			t.Fatalf("state %s produced %d events", step.state, len(events))
		}
		if got, ok := patchValue(events[0], "/status"); !ok || got != step.want {
			t.Fatalf("state %s projected status=%v, present=%v", step.state, got, ok)
		}
	}
}

func TestTypedWorkerDeltaWithoutLegacyDetailsReachesActivity(t *testing.T) {
	tr := agui.NewEventTranslator()
	_ = tr.Translate(adaptor.RunStarted{RunID: "leader"})
	_ = tr.Translate(adaptor.SubagentUpdate{Task: "task", Member: "worker", Kind: adaptor.SubagentStarted})
	for _, text := range []string{"exact ", " text\n"} {
		events := tr.Translate(adaptor.SubagentUpdate{Task: "task", Member: "worker", Kind: adaptor.SubagentDelta, Delta: text})
		if len(events) != 1 {
			t.Fatalf("delta produced %d events", len(events))
		}
		want := "exact "
		if text == " text\n" {
			want += text
		}
		if got, ok := patchValue(events[0], "/text"); !ok || got != want {
			t.Fatalf("text=%q, want %q, present=%v", got, want, ok)
		}
	}
}
