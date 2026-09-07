package a2a

import "reflect"

func executionFinalState(state TaskState) bool {
	return state.Terminal() || state == TaskStateInputRequired
}

func executionFinalTask(task Task) bool {
	return executionFinalState(task.Status.State)
}

func executionFinalEvent(event Event) bool {
	switch event.Kind {
	case EventMessage:
		return event.Message != nil
	case EventTerminal:
		return !persistedTaskSnapshot(event) || event.RecoveredState
	case EventTask:
		return event.RecoveredState && event.Task != nil && executionFinalTask(*event.Task)
	case EventStatus:
		return event.Status != nil && executionFinalState(event.Status.State)
	default:
		return false
	}
}

// A full Task received on a stream is historical unless explicitly recovered.
func persistedTaskSnapshot(event Event) bool {
	return event.Task != nil && event.Status == nil && event.Message == nil
}

// Recovery is an explicit query, but an unchanged snapshot is not evidence
// that a continuation produced a new terminal state or a new question.
func staleRecovery(task Task, snapshot *Task, continuation bool) bool {
	if snapshot == nil || snapshot.ID != task.ID {
		return continuation && task.Status.State == TaskStateInputRequired
	}
	if task.Status.State != snapshot.Status.State {
		return false
	}
	old, current := snapshot.Status.Message, task.Status.Message
	if current == nil {
		return true
	}
	if old == nil {
		return false
	}
	// Distinct formal message IDs identify distinct questions, even when
	// their wording/parts repeat. Compare content only without both IDs.
	if old.ID != "" && current.ID != "" {
		return old.ID == current.ID
	}
	return reflect.DeepEqual(old.Parts, current.Parts)
}
