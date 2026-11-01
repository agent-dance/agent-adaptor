package adaptertest

import (
	"reflect"
	"strings"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func hasBoundary(bs []driver.Boundary, b driver.Boundary) bool {
	for _, v := range bs {
		if v == b {
			return true
		}
	}
	return false
}
func containsPayload(payloads []string, text string) bool {
	for _, p := range payloads {
		if strings.Contains(p, text) {
			return true
		}
	}
	return false
}

// VerifyMessageCapability checks MSG-01/02: valid declarations, capability
// implications and a request-specific capability contained in Descriptor.
// ContinueResume is a valid downgrade from ContinueInRun when resume is supported.
func VerifyMessageCapability(desc driver.Descriptor, resolved driver.MessageCapability) (out []Violation) {
	check := func(c driver.MessageCapability) {
		seen := map[driver.Boundary]bool{}
		for _, b := range c.Boundaries {
			if b != driver.BoundaryImmediate && b != driver.BoundaryTool && b != driver.BoundaryStop && b != driver.BoundaryTurnEnd {
				out = append(out, violationf("MSG-01", "unknown boundary %q", b))
			}
			if seen[b] {
				out = append(out, violationf("MSG-01", "duplicate boundary %q", b))
			}
			seen[b] = true
		}
		if c.Interrupt && !seen[driver.BoundaryImmediate] {
			out = append(out, violationf("MSG-01", "Interrupt requires immediate boundary"))
		}
		if seen[driver.BoundaryTurnEnd] && c.Continue != driver.ContinueInRun && !desc.Sessions.SupportsResume {
			out = append(out, violationf("MSG-01", "turn_end requires in-run continuation or session resume"))
		}
		if c.Continue != driver.ContinueResume && c.Continue != driver.ContinueInRun {
			out = append(out, violationf("MSG-01", "unknown ContinueMode %d", c.Continue))
		}
		if c.MaxUnits < 0 || c.Unit != "" && c.Unit != driver.MessageUnitBytes && c.Unit != driver.MessageUnitUTF16 {
			out = append(out, violationf("MSG-01", "invalid framed size declaration: %d %q", c.MaxUnits, c.Unit))
		}
		if c.PersistsDelivered && !desc.Sessions.SupportsResume {
			out = append(out, violationf("MSG-01", "PersistsDelivered requires session resume"))
		}
	}
	check(desc.Messages)
	if !reflect.DeepEqual(desc.Messages, resolved) {
		check(resolved)
	}
	for _, b := range resolved.Boundaries {
		if !hasBoundary(desc.Messages.Boundaries, b) && b != driver.BoundaryTurnEnd {
			out = append(out, violationf("MSG-02", "resolver added undeclared boundary %q", b))
		}
	}
	if resolved.Interrupt && !desc.Messages.Interrupt || resolved.Continue == driver.ContinueInRun && desc.Messages.Continue != driver.ContinueInRun || resolved.ConfirmedDelivery && !desc.Messages.ConfirmedDelivery || resolved.PersistsDelivered && !desc.Messages.PersistsDelivered {
		out = append(out, violationf("MSG-02", "resolver exceeds static message capability"))
	}
	if desc.Messages.MaxUnits > 0 && (resolved.MaxUnits == 0 || resolved.MaxUnits > desc.Messages.MaxUnits || normalizedMessageUnit(resolved.Unit) != normalizedMessageUnit(desc.Messages.Unit)) {
		out = append(out, violationf("MSG-02", "resolver enlarged or changed the declared size envelope"))
	}
	return out
}
func normalizedMessageUnit(u driver.MessageUnit) driver.MessageUnit {
	if u == "" {
		return driver.MessageUnitBytes
	}
	return u
}

func verifyMessageCalls(caps driver.MessageCapability, calls []MessageCall, evidence MessageEvidence) (out []Violation) {
	open, opened, returned := false, false, false
	seenWindow := false
	claimed := map[string]driver.MessageItem{}
	acks := map[string]int{}
	for _, c := range calls {
		if returned {
			out = append(out, violationf("MSG-04", "%s used after Run returned", c.Operation))
		}
		switch c.Operation {
		case "return":
			returned = true
			for id := range claimed {
				if acks[id] != 1 {
					out = append(out, violationf("MSG-04", "%q had %d Acks at Run return, want exactly one", id, acks[id]))
				}
			}
			if open {
				out = append(out, violationf("MSG-03", "delivery window left open at Run return"))
			}
		case "window":
			if c.Open && open {
				out = append(out, violationf("MSG-03", "duplicate Window(true) without closing the window"))
			}
			if !c.Open && !open && seenWindow {
				out = append(out, violationf("MSG-03", "duplicate Window(false) without opening the window"))
			}
			// An initial Window(false) is a harmless explicit reset.
			seenWindow = true
			open = c.Open
			opened = opened || open
			if open && len(caps.Boundaries) == 0 && caps.Continue != driver.ContinueInRun {
				out = append(out, violationf("MSG-02", "resume-only transport opened a delivery window"))
			}
		case "claim":
			if c.Boundary == driver.BoundaryTurnEnd {
				if caps.Continue != driver.ContinueInRun {
					out = append(out, violationf("MSG-08", "ContinueResume Driver claimed at turn_end; continuation belongs to core"))
				}
				// Turn-end claims may follow Window(false), since the old turn is over.
				if len(caps.Boundaries) > 0 && !opened {
					out = append(out, violationf("MSG-03", "Claim before the first provider-confirmed Window(true)"))
				}
			} else {
				if !hasBoundary(caps.Boundaries, c.Boundary) {
					out = append(out, violationf("MSG-02", "Claim at undeclared/resolved-away boundary %q", c.Boundary))
				}
				if !open {
					out = append(out, violationf("MSG-03", "Claim(%s) outside an open window", c.Boundary))
				}
			}
			for _, item := range c.Items {
				if _, ok := claimed[item.ID]; ok {
					out = append(out, violationf("MSG-04", "%q claimed twice", item.ID))
				}
				claimed[item.ID] = item
				if item.Timing == driver.TimingInterrupt && (!caps.Interrupt || c.Boundary != driver.BoundaryImmediate) || item.Timing == driver.TimingAfterTurn && c.Boundary != driver.BoundaryTurnEnd {
					out = append(out, violationf("MSG-02", "%q claimed at a timing-incompatible boundary", item.ID))
				}
			}
		case "ack":
			item, ok := claimed[c.ID]
			acks[c.ID]++
			if !ok || acks[c.ID] != 1 {
				out = append(out, violationf("MSG-04", "Ack(%q) is unknown or duplicated", c.ID))
				continue
			}
			a := c.Ack
			if a.Delivered {
				if messageHasID(evidence.Ambiguous, c.ID) || messageHasID(evidence.Rejected, c.ID) {
					out = append(out, violationf("MSG-05", "Ack(%q) claims delivery after unknown acceptance or explicit rejection", c.ID))
				}
				if a.Mechanism == "" || a.Requeue || a.MaybeDelivered {
					out = append(out, violationf("MSG-05", "delivered Ack(%q) lacks a mechanism or also requests a retry", c.ID))
				}
				if !containsPayload(evidence.Received, item.Text) {
					out = append(out, violationf("MSG-05", "Ack(%q) claims delivery without verbatim provider input evidence", c.ID))
				}
				if caps.ConfirmedDelivery && !containsPayload(evidence.Confirmed, item.Text) {
					out = append(out, violationf("MSG-06", "Ack(%q) claims confirmed delivery without official provider echo", c.ID))
				}
			} else {
				if a.Err == nil {
					out = append(out, violationf("MSG-05", "undelivered Ack(%q) has no error", c.ID))
				}
				ambiguous := messageHasID(evidence.Ambiguous, c.ID) || containsPayload(evidence.Received, item.Text) && !messageHasID(evidence.Rejected, c.ID)
				if ambiguous && a.Requeue && !a.MaybeDelivered {
					out = append(out, violationf("MSG-05", "Ack(%q) retries possibly delivered input without MaybeDelivered", c.ID))
				}
				if a.MaybeDelivered && !a.Requeue {
					out = append(out, violationf("MSG-05", "Ack(%q) sets MaybeDelivered without Requeue", c.ID))
				}
			}
		}
	}
	if !returned {
		out = append(out, violationf("MSG-04", "port trace has no Run return marker; call Finish"))
	}
	return out
}

// VerifyMessageTurns checks MSG-07 using the fake provider's independently
// observed completed turns for this Run. It rejects missing/duplicate events,
// non-contiguous indices, missing completion times, text/usage loss and false
// interruption claims. Nil usage remains distinct from observed zero usage.
func VerifyMessageTurns(events []driver.RunEvent, expected []driver.TurnCompleted) (out []Violation) {
	var turns []*driver.TurnCompleted
	for _, e := range events {
		if e.Type == driver.RunEventTurnCompleted {
			turns = append(turns, e.Turn)
		}
	}
	if len(turns) != len(expected) {
		out = append(out, violationf("MSG-07", "TurnCompleted count %d, provider turns %d", len(turns), len(expected)))
	}
	for i, turn := range turns {
		if turn == nil {
			out = append(out, violationf("MSG-07", "TurnCompleted[%d] has nil payload", i))
			continue
		}
		if turn.Index != i || turn.CompletedAt.IsZero() {
			out = append(out, violationf("MSG-07", "turn %d has Index=%d or missing CompletedAt", i, turn.Index))
		}
		if i < len(expected) {
			want := expected[i]
			if turn.Text != want.Text || !reflect.DeepEqual(turn.Usage, want.Usage) || turn.Interrupted != want.Interrupted {
				out = append(out, violationf("MSG-07", "turn %d text/usage/interruption differs from provider evidence: got %+v, want %+v", i, *turn, want))
			}
		}
	}
	return out
}

func messageHasID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// VerifyMessageUsage checks MSG-07 accounting: Response.Usage is the sum of
// observed provider-turn usage, preserving the distinction between unobserved
// usage and an explicitly observed zero. Call this for completed successful Runs.
func VerifyMessageUsage(turns []driver.TurnCompleted, usage *driver.Usage) []Violation {
	var sum *driver.Usage
	for _, turn := range turns {
		if turn.Usage == nil {
			continue
		}
		if sum == nil {
			sum = &driver.Usage{}
		}
		sum.InputTokens += turn.Usage.InputTokens
		sum.OutputTokens += turn.Usage.OutputTokens
		sum.CachedInputTokens += turn.Usage.CachedInputTokens
		sum.EstimatedCostMilli += turn.Usage.EstimatedCostMilli
	}
	if !reflect.DeepEqual(sum, usage) {
		return []Violation{violationf("MSG-07", "Response.Usage %+v differs from observed turn sum %+v", usage, sum)}
	}
	return nil
}
