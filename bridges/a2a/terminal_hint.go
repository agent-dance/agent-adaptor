package a2a

import adaptor "github.com/agent-dance/agent-adaptor"

// terminalHint retains only bounded classification evidence from one Stream.
// A matching, unique, last failed terminal can clarify a bare error only after
// Events closes. Result's error still decides failure; a RunError always wins.
// The consuming goroutine owns this state across every drain path.
type terminalHint struct {
	runID     string
	terminals uint8
	last      bool
	closed    bool
	candidate adaptor.FailureReason
}

func (h *terminalHint) observe(event adaptor.Event) {
	h.last = false
	var terminal *adaptor.RunFinished
	switch e := event.(type) {
	case adaptor.RunFinished:
		terminal = &e
	case *adaptor.RunFinished:
		terminal = e
	default:
		return
	}
	// Include nil pointers and invalid terminals in uniqueness checks. Even
	// identical duplicates invalidate the hint instead of picking a winner.
	if h.terminals < 2 {
		h.terminals++
	}
	if h.terminals != 1 || terminal == nil {
		return
	}
	h.last = true
	if h.runID != "" && terminal.Meta().RunID == h.runID && terminal.Failed && knownFailureReason(terminal.Reason) {
		h.candidate = terminal.Reason
	}
}

func (h *terminalHint) reason() adaptor.FailureReason {
	if !h.closed || h.terminals != 1 || !h.last {
		return ""
	}
	return h.candidate
}
