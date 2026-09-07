package a2adelegation

import adaptor "github.com/agent-dance/agent-adaptor"

// terminalReason observes only the one Local Stream being drained. It retains
// bounded qualification state, never provider payload or another failure path.
// The reading goroutine calls reason only after observing channel closure.
type terminalReason struct {
	runID     string
	count     uint8
	last      bool
	candidate string
}

func (h *terminalReason) observe(ev adaptor.Event) {
	h.last = false
	var terminal *adaptor.RunFinished
	switch e := ev.(type) {
	case adaptor.RunFinished:
		terminal = &e
	case *adaptor.RunFinished:
		terminal = e
	default:
		return
	}
	if h.count < 2 {
		h.count++
	}
	h.candidate = ""
	if terminal == nil || h.runID == "" || terminal.Meta().RunID != h.runID || !terminal.Failed || !failureCodeKnown(string(terminal.Reason)) {
		return
	}
	h.last = true
	h.candidate = string(terminal.Reason)
}
func (h *terminalReason) reason() string {
	if h.count == 1 && h.last {
		return h.candidate
	}
	return ""
}
