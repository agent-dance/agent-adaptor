package a2a

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
		return true
	case EventTask:
		return event.Task != nil && executionFinalTask(*event.Task)
	case EventStatus:
		return event.Status != nil && executionFinalState(event.Status.State)
	default:
		return false
	}
}
