package subagentstream

import adaptor "github.com/agent-dance/agent-adaptor/v2"

// Update is the wire projection of a typed subagent event. Team already emits
// these on the execution's single Event stream; no bus or merge is needed.
type Update struct {
	// Meta preserves execution identity, order and provider coordinates.
	Meta adaptor.EventMeta `json:"meta"`
	// Task is the team task handle.
	Task string `json:"task"`
	// Member is the roster key.
	Member string `json:"member"`
	// Kind describes started, delta, message, state or finished.
	Kind adaptor.SubagentEventKind `json:"kind"`
	// State is the task lifecycle state.
	State string `json:"state,omitempty"`
	// Delta is incremental text or a peer message.
	Delta string `json:"delta,omitempty"`
	// Summary is the task's bounded status summary.
	Summary string `json:"summary,omitempty"`
	// Data preserves protocol extensions without defining team semantics.
	Data map[string]any `json:"data,omitempty"`
}

// Project translates a SubagentUpdate from the existing Event stream. Other
// events return false and remain the caller's responsibility. No execution,
// scheduling, synthetic terminal or parallel error policy is introduced.
func Project(event adaptor.Event) (Update, bool) {
	e, ok := event.(adaptor.SubagentUpdate)
	if !ok {
		return Update{}, false
	}
	return Update{Meta: e.Meta(), Task: e.Task, Member: e.Member, Kind: e.Kind, State: e.State, Delta: e.Delta, Summary: e.Summary, Data: e.Data}, true
}
