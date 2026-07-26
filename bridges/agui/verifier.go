package agui

import (
	"errors"
	"fmt"

	aguievents "github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
)

// VerifySequence runs a Translator's output through every layer of
// compliance checking we can do in Go:
//
//  1. The AG-UI Go SDK's events.ValidateSequence — enforces structural
//     rules covered by the official package (run/message/tool-call
//     lifecycle pairing, reasoning message pairing, step pairing).
//  2. A thin supplementary verifier that covers rules enforced by
//     CopilotKit's TypeScript verifyEvents but missing from the Go SDK:
//     - "First event must be RUN_STARTED" (or RUN_ERROR).
//     - "No further events after RUN_ERROR / RUN_FINISHED".
//     - "Open TEXT/TOOL/REASONING/STEP lifecycles must close before
//       RUN_FINISHED" (Go ValidateSequence permits dangling lifecycles
//       at run end; the TS verifier does not).
//
// Returns nil when the sequence is compliant. Tests should fail fast on
// any non-nil error. Callers that want to separate concerns can call the
// two phases individually via ValidateStructure / validateLifecycleRules.
//
// This function is intended for test-time compliance regression. It is
// cheap enough to run in production assertions, but Translator itself
// guarantees compliance by construction so runtime verification is
// optional.
func VerifySequence(events []aguievents.Event) error {
	if err := ValidateStructure(events); err != nil {
		return err
	}
	return verifyCopilotKitRules(events)
}

// ValidateStructure is a thin wrapper around events.ValidateSequence so
// callers do not have to depend on the ag-ui import directly.
func ValidateStructure(events []aguievents.Event) error {
	return aguievents.ValidateSequence(events)
}

// verifyCopilotKitRules implements the three checks CopilotKit performs
// that Go's ValidateSequence omits.
func verifyCopilotKitRules(events []aguievents.Event) error {
	if len(events) == 0 {
		return nil
	}

	// Rule 1: The first event must be RUN_STARTED or RUN_ERROR.
	firstType := events[0].Type()
	if firstType != aguievents.EventTypeRunStarted && firstType != aguievents.EventTypeRunError {
		return fmt.Errorf("agui: first event must be RUN_STARTED or RUN_ERROR, got %s", firstType)
	}

	var (
		activeMessages  = map[string]bool{}
		activeReasoning = map[string]bool{}
		activeTools     = map[string]bool{}
		activeSteps     = map[string]bool{}
		runFinished     = false
	)

	for i, ev := range events {
		kind := ev.Type()

		// Rule 2: No events after RUN_FINISHED / RUN_ERROR except a new
		// RUN_STARTED (which the tests don't emit; CopilotKit allows
		// restart but the bridge doesn't).
		if runFinished && kind != aguievents.EventTypeRunStarted {
			return fmt.Errorf("agui: event %d (%s): no further events allowed after RUN_FINISHED / RUN_ERROR", i, kind)
		}

		switch kind {
		case aguievents.EventTypeTextMessageStart:
			id, ok := extractMessageID(ev)
			if !ok {
				continue
			}
			activeMessages[id] = true
		case aguievents.EventTypeTextMessageEnd:
			id, ok := extractMessageID(ev)
			if !ok {
				continue
			}
			delete(activeMessages, id)
		case aguievents.EventTypeReasoningMessageStart:
			id, ok := extractReasoningID(ev)
			if !ok {
				continue
			}
			activeReasoning[id] = true
		case aguievents.EventTypeReasoningMessageEnd:
			id, ok := extractReasoningID(ev)
			if !ok {
				continue
			}
			delete(activeReasoning, id)
		case aguievents.EventTypeToolCallStart:
			id, ok := extractToolCallID(ev)
			if !ok {
				continue
			}
			activeTools[id] = true
		case aguievents.EventTypeToolCallEnd:
			id, ok := extractToolCallID(ev)
			if !ok {
				continue
			}
			delete(activeTools, id)
		case aguievents.EventTypeStepStarted:
			name, ok := extractStepName(ev)
			if !ok {
				continue
			}
			activeSteps[name] = true
		case aguievents.EventTypeStepFinished:
			name, ok := extractStepName(ev)
			if !ok {
				continue
			}
			delete(activeSteps, name)

		case aguievents.EventTypeRunStarted:
			// Valid at position 0; allowed after RUN_FINISHED too, but
			// the Translator never re-starts a run and the tests don't
			// exercise that case.
			runFinished = false

		case aguievents.EventTypeRunFinished, aguievents.EventTypeRunError:
			// Rule 3: every open lifecycle must be closed before the run
			// terminates.
			if ids := mapKeys(activeMessages); len(ids) > 0 {
				return fmt.Errorf("agui: event %d (%s): unclosed text messages %v", i, kind, ids)
			}
			if ids := mapKeys(activeReasoning); len(ids) > 0 {
				return fmt.Errorf("agui: event %d (%s): unclosed reasoning messages %v", i, kind, ids)
			}
			if ids := mapKeys(activeTools); len(ids) > 0 {
				return fmt.Errorf("agui: event %d (%s): unclosed tool calls %v", i, kind, ids)
			}
			if names := mapKeys(activeSteps); len(names) > 0 {
				return fmt.Errorf("agui: event %d (%s): unfinished steps %v", i, kind, names)
			}
			runFinished = true
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Event field extraction helpers.
//
// The AG-UI Go SDK models each event as a distinct struct, so we type-switch
// rather than relying on reflection. Unknown event types fall through.
// ---------------------------------------------------------------------------

func extractMessageID(ev aguievents.Event) (string, bool) {
	switch e := ev.(type) {
	case *aguievents.TextMessageStartEvent:
		return e.MessageID, true
	case *aguievents.TextMessageEndEvent:
		return e.MessageID, true
	}
	return "", false
}

func extractReasoningID(ev aguievents.Event) (string, bool) {
	switch e := ev.(type) {
	case *aguievents.ReasoningMessageStartEvent:
		return e.MessageID, true
	case *aguievents.ReasoningMessageEndEvent:
		return e.MessageID, true
	}
	return "", false
}

func extractToolCallID(ev aguievents.Event) (string, bool) {
	switch e := ev.(type) {
	case *aguievents.ToolCallStartEvent:
		return e.ToolCallID, true
	case *aguievents.ToolCallEndEvent:
		return e.ToolCallID, true
	}
	return "", false
}

func extractStepName(ev aguievents.Event) (string, bool) {
	switch e := ev.(type) {
	case *aguievents.StepStartedEvent:
		return e.StepName, true
	case *aguievents.StepFinishedEvent:
		return e.StepName, true
	}
	return "", false
}

func mapKeys[K comparable, V any](m map[K]V) []K {
	if len(m) == 0 {
		return nil
	}
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// Ensure errors stays referenced for future expansion (cleaner than goimports churn).
var _ = errors.New
