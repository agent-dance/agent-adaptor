package adaptertest

import (
	"reflect"
	"slices"
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

type messageAttempt struct {
	item    driver.MessageItem
	number  int
	settled bool
	ack     driver.MessageAck
}

func verifyMessageCalls(caps driver.MessageCapability, calls []MessageCall, evidence MessageEvidence) (out []Violation) {
	open, opened, returned := false, false, false
	seenWindow := false
	latest := map[string]*messageAttempt{}
	var attempts []*messageAttempt
	for _, c := range calls {
		if returned {
			out = append(out, violationf("MSG-04", "%s used after Run returned", c.Operation))
		}
		switch c.Operation {
		case "return":
			returned = true
			for _, attempt := range attempts {
				if !attempt.settled {
					out = append(out, violationf("MSG-04", "%q attempt %d had no Ack at Run return", attempt.item.ID, attempt.number))
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
				number := 1
				if previous := latest[item.ID]; previous != nil {
					number = previous.number + 1
					if !previous.settled || previous.ack.Delivered || !previous.ack.Requeue {
						out = append(out, violationf("MSG-04", "%q claimed again without a settled Requeue", item.ID))
					}
					if c.Boundary != driver.BoundaryTurnEnd {
						out = append(out, violationf("MSG-05", "%q retried outside turn_end", item.ID))
					}
					if previous.ack.MaybeDelivered && !item.MaybeDuplicate {
						out = append(out, violationf("MSG-05", "%q retry lacks MaybeDuplicate after MaybeDelivered", item.ID))
					}
				}
				attempt := &messageAttempt{item: item, number: number}
				latest[item.ID] = attempt
				attempts = append(attempts, attempt)
				if item.Timing == driver.TimingInterrupt && (!caps.Interrupt || c.Boundary != driver.BoundaryImmediate) || item.Timing == driver.TimingAfterTurn && c.Boundary != driver.BoundaryTurnEnd && c.Boundary != driver.BoundaryStop {
					out = append(out, violationf("MSG-02", "%q claimed at a timing-incompatible boundary", item.ID))
				}
			}
		case "ack":
			attempt := latest[c.ID]
			if attempt == nil || attempt.settled {
				out = append(out, violationf("MSG-04", "Ack(%q) is unknown or duplicated", c.ID))
				continue
			}
			attempt.settled, attempt.ack = true, c.Ack
		}
	}
	if !returned {
		out = append(out, violationf("MSG-04", "port trace has no Run return marker; call Finish"))
	}
	out = append(out, verifyMessageAttempts(caps, attempts, latest, evidence)...)
	return out
}

func verifyMessageAttempts(caps driver.MessageCapability, attempts []*messageAttempt, latest map[string]*messageAttempt, evidence MessageEvidence) (out []Violation) {
	type key struct {
		id      string
		attempt int
	}
	scoped := map[key]MessageAttemptEvidence{}
	scopedIDs := map[string]bool{}
	for _, e := range evidence.Attempts {
		k := key{e.ID, e.Attempt}
		if last := latest[e.ID]; last == nil || e.Attempt < 1 || e.Attempt > last.number {
			out = append(out, violationf("MSG-05", "provider evidence names unknown %q attempt %d", e.ID, e.Attempt))
		}
		if _, exists := scoped[k]; exists {
			out = append(out, violationf("MSG-05", "duplicate provider evidence for %q attempt %d", e.ID, e.Attempt))
		}
		scoped[k], scopedIDs[e.ID] = e, true
	}
	var texts, received, confirmed []string
	seenText := map[string]bool{}
	for _, attempt := range attempts {
		item, a := attempt.item, attempt.ack
		if item.Text != "" && !seenText[item.Text] {
			texts = append(texts, item.Text)
			seenText[item.Text] = true
		}
		e := MessageAttemptEvidence{}
		if scopedIDs[item.ID] {
			var ok bool
			e, ok = scoped[key{item.ID, attempt.number}]
			if !ok {
				out = append(out, violationf("MSG-05", "%q attempt %d has no scoped provider evidence", item.ID, attempt.number))
			}
		} else if latest[item.ID] == attempt {
			e.Received, e.Confirmed = evidence.Received, evidence.Confirmed
			e.Ambiguous, e.Rejected = messageHasID(evidence.Ambiguous, item.ID), messageHasID(evidence.Rejected, item.ID)
		}
		hasReceipt := containsPayload(e.Received, item.Text)
		hasEcho := containsPayload(e.Confirmed, item.Text)
		// Include failed attempts that reached the peer in the expected wire
		// sequence. Ack order is deliberately irrelevant.
		if hasReceipt {
			received = append(received, item.Text)
		}
		if hasEcho {
			confirmed = append(confirmed, item.Text)
		}
		if !attempt.settled {
			continue
		}
		if a.Delivered {
			if e.Ambiguous || e.Rejected {
				out = append(out, violationf("MSG-05", "Ack(%q) attempt %d claims delivery after unknown acceptance or explicit rejection", item.ID, attempt.number))
			}
			if a.Mechanism == "" || a.Requeue || a.MaybeDelivered {
				out = append(out, violationf("MSG-05", "delivered Ack(%q) lacks a mechanism or also requests a retry", item.ID))
			}
			if !hasReceipt {
				out = append(out, violationf("MSG-05", "Ack(%q) attempt %d claims delivery without verbatim provider input evidence", item.ID, attempt.number))
			}
			if caps.ConfirmedDelivery && !hasEcho {
				out = append(out, violationf("MSG-06", "Ack(%q) attempt %d claims confirmed delivery without official provider echo", item.ID, attempt.number))
			}
		} else {
			if a.Err == nil {
				out = append(out, violationf("MSG-05", "undelivered Ack(%q) has no error", item.ID))
			}
			if (e.Ambiguous || hasReceipt && !e.Rejected) && a.Requeue && !a.MaybeDelivered {
				out = append(out, violationf("MSG-05", "Ack(%q) attempt %d retries possibly delivered input without MaybeDelivered", item.ID, attempt.number))
			}
			if a.MaybeDelivered && !a.Requeue {
				out = append(out, violationf("MSG-05", "Ack(%q) sets MaybeDelivered without Requeue", item.ID))
			}
		}
	}
	if observed := framedPayloads(evidence.Received, texts); !slices.Equal(observed, received) {
		out = append(out, violationf("MSG-04", "provider input is out of claim order or reuses a receipt: observed %d framed inputs, want %d in claim order", len(observed), len(received)))
	}
	// Confirmation responses may arrive in a different order from provider
	// input, and the provider may echo the same input more than once. Require
	// a distinct echo occurrence for each observed attempt, without reusing it.
	echoesNeeded := map[string]int{}
	for _, text := range confirmed {
		echoesNeeded[text]++
	}
	for _, text := range framedPayloads(evidence.Confirmed, texts) {
		if echoesNeeded[text] > 0 {
			echoesNeeded[text]--
		}
	}
	for _, count := range echoesNeeded {
		if count > 0 {
			out = append(out, violationf("MSG-06", "provider echo reused across delivery attempts"))
			break
		}
	}
	return out
}

// framedPayloads extracts every observed occurrence of the claimed texts in
// wire order. Unrelated input (such as the initial prompt) is ignored. Matching
// the longest text at an offset keeps a retry's duplicate-warning prefix from
// also being counted as its original, shorter message.
func framedPayloads(payloads, texts []string) (out []string) {
	for _, payload := range payloads {
		for offset := 0; offset < len(payload); {
			start, text := -1, ""
			for _, candidate := range texts {
				i := strings.Index(payload[offset:], candidate)
				if i >= 0 && (start < 0 || i < start || i == start && len(candidate) > len(text)) {
					start, text = i, candidate
				}
			}
			if start < 0 {
				break
			}
			out = append(out, text)
			offset += start + len(text)
		}
	}
	return out
}

// payloadCursor locates framed items inside separately received payloads or a
// combined batch. Advancing by the full text prevents one occurrence from
// proving delivery of two items with identical text.
type payloadCursor struct{ payload, offset int }

func (c *payloadCursor) take(payloads []string, text string) bool {
	if text == "" {
		return false
	}
	for p := c.payload; p < len(payloads); p++ {
		offset := 0
		if p == c.payload {
			offset = c.offset
		}
		if i := strings.Index(payloads[p][offset:], text); i >= 0 {
			c.payload, c.offset = p, offset+i+len(text)
			return true
		}
	}
	return false
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
