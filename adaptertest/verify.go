package adaptertest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Violation is one contract breach detected by a verifier. Clause is the
// numbered contract rule from the catalogue in the package documentation;
// each clause cites the driver-package godoc it is grounded in.
type Violation struct {
	Clause  string
	Message string
}

func (v Violation) String() string { return v.Clause + ": " + v.Message }

func violationf(clause, format string, args ...any) Violation {
	return Violation{Clause: clause, Message: fmt.Sprintf(format, args...)}
}

// VerifySessionCapability checks the declaration and support-interface half
// of CAP-01 without starting a provider process. Resume support requires both
// a stable, non-nil SessionCodec and a stable, non-empty construction-config
// fingerprint; Thread correctness depends on both identities.
func VerifySessionCapability(d driver.Driver) (out []Violation) {
	if d == nil {
		return []Violation{violationf("CAP-01", "Driver is nil")}
	}
	resume := d.Descriptor().Sessions.SupportsResume
	provider, hasProvider := d.(driver.SessionCodecProvider)
	var codec driver.SessionCodec
	var codecPanicked bool
	if hasProvider {
		codec, codecPanicked = callSessionCodec(provider)
	}
	codecNil := nilDynamicValue(codec)

	if resume {
		switch {
		case !hasProvider:
			out = append(out, violationf("CAP-01", "Sessions.SupportsResume=true but Driver does not implement SessionCodecProvider"))
		case codecPanicked:
			out = append(out, violationf("CAP-01", "SessionCodec() panicked on a resume-capable Driver"))
		case codecNil:
			out = append(out, violationf("CAP-01", "SessionCodec() returned nil or a typed-nil codec on a resume-capable Driver"))
		default:
			first, firstOK := callCodecName(codec)
			second, secondOK := callCodecName(codec)
			if !firstOK || !secondOK {
				out = append(out, violationf("CAP-01", "SessionCodec.Name() panicked on a resume-capable Driver"))
			} else if strings.TrimSpace(first) == "" {
				out = append(out, violationf("CAP-01", "SessionCodec.Name() is empty on a resume-capable Driver"))
			} else if first != second {
				out = append(out, violationf("CAP-01", "SessionCodec.Name() is unstable: %q then %q", first, second))
			}
		}

		fingerprinter, ok := d.(driver.SessionConfigFingerprinter)
		if !ok {
			out = append(out, violationf("CAP-01", "Sessions.SupportsResume=true but Driver does not implement SessionConfigFingerprinter"))
		} else {
			first, firstErr, firstOK := callConfigFingerprint(fingerprinter)
			second, secondErr, secondOK := callConfigFingerprint(fingerprinter)
			switch {
			case !firstOK || !secondOK:
				out = append(out, violationf("CAP-01", "SessionConfigFingerprint() panicked on a resume-capable Driver"))
			case firstErr != nil:
				out = append(out, violationf("CAP-01", "SessionConfigFingerprint() failed: %v", firstErr))
			case secondErr != nil:
				out = append(out, violationf("CAP-01", "SessionConfigFingerprint() stability check failed: %v", secondErr))
			case strings.TrimSpace(first) == "":
				out = append(out, violationf("CAP-01", "SessionConfigFingerprint() returned an empty fingerprint"))
			case first != second:
				out = append(out, violationf("CAP-01", "SessionConfigFingerprint() is unstable"))
			}
		}
	} else if hasProvider && !codecPanicked && !codecNil {
		out = append(out, violationf("CAP-01", "Driver exposes a non-nil SessionCodec but Sessions.SupportsResume=false; the declaration is an iff contract"))
	}
	return out
}

// VerifyProcessCapability checks that a Driver advertising a persistent
// process also exposes the lifecycle hook Agent.Close needs to reap it.
func VerifyProcessCapability(d driver.Driver) []Violation {
	if d == nil {
		return []Violation{violationf("CAP-11", "Driver is nil")}
	}
	if d.Descriptor().Process.Persistent {
		if _, ok := d.(driver.ProcessLifecycleDriver); !ok {
			return []Violation{violationf("CAP-11", "Process.Persistent=true but Driver does not implement ProcessLifecycleDriver")}
		}
	}
	return nil
}

func nilDynamicValue(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

func callSessionCodec(provider driver.SessionCodecProvider) (codec driver.SessionCodec, ok bool) {
	defer func() {
		if recover() != nil {
			codec, ok = nil, true
		}
	}()
	return provider.SessionCodec(), false
}

func callCodecName(codec driver.SessionCodec) (name string, ok bool) {
	defer func() {
		if recover() != nil {
			name, ok = "", false
		}
	}()
	return codec.Name(), true
}

func callConfigFingerprint(fingerprinter driver.SessionConfigFingerprinter) (fingerprint string, err error, ok bool) {
	defer func() {
		if recover() != nil {
			fingerprint, err, ok = "", nil, false
		}
	}()
	fingerprint, err = fingerprinter.SessionConfigFingerprint()
	return fingerprint, err, true
}

// VerifyStructuredOutputCapability checks the declaration half of the
// structured-output matrix (SO-01). The optional live probe verifies the
// native, non-streaming baseline; core's structured contract tests own
// automatic selection and rejection across provider-streaming and HITL.
func VerifyStructuredOutputCapability(capability driver.StructuredOutputCapability) []Violation {
	declared := capability.JSONSchemaNative || capability.JSONSchemaPromptValidate
	anyWorks := capability.WorksWithRun || capability.WorksWithStreaming || capability.WorksWithHITL
	var out []Violation
	if !declared && anyWorks {
		out = append(out, violationf("SO-01",
			"WorksWith* is true without JSONSchemaNative or JSONSchemaPromptValidate; a transport flag cannot create an output mechanism"))
	}
	if declared && !capability.WorksWithRun {
		out = append(out, violationf("SO-01",
			"a structured-output mechanism is declared with WorksWithRun=false; structured output uses v1's single execution pipeline"))
	}
	return out
}

// knownStreamKind reports whether kind is one of the 19 normalized
// StreamKinds declared in driver/events.go. Vendor-specific kinds outside
// that set are tolerated and skipped by the verifiers (StreamKind docs:
// "Drivers may emit a subset"; the set is open for provider extensions).
func knownStreamKind(kind driver.StreamKind) bool {
	switch kind {
	case driver.StreamRunStarted, driver.StreamRunFinished, driver.StreamRunError,
		driver.StreamStepStarted, driver.StreamStepFinished,
		driver.StreamTextStart, driver.StreamTextContent, driver.StreamTextEnd,
		driver.StreamToolCallStart, driver.StreamToolCallArgs, driver.StreamToolCallEnd, driver.StreamToolCallResult,
		driver.StreamReasoningStart, driver.StreamReasoningContent, driver.StreamReasoningEnd,
		driver.StreamHITLRequested, driver.StreamHITLResolved,
		driver.StreamDropped:
		return true
	}
	return false
}

// VerifyStreamSequence checks the driver-side stream event timing contract
// (clauses EVT-01 .. EVT-11, EVT-13) against payloads exactly as the driver
// emitted them (before SDK Sequence/Timestamp backfill). The authority for
// each clause is the godoc in package driver (StreamKind, StreamPayload,
// Role, EventSink); see the package documentation for the catalogue.
func VerifyStreamSequence(payloads []driver.StreamPayload) []Violation {
	var out []Violation

	if len(payloads) > 0 && payloads[0].Kind != driver.StreamRunStarted {
		out = append(out, violationf("EVT-01",
			"first stream payload is %q, want run.started (StreamRunStarted marks the beginning of every streamed run)",
			payloads[0].Kind))
	}

	started := 0
	terminalCount := 0
	terminal := false
	openText := map[string]bool{}
	closedText := map[string]bool{}
	openReasoning := map[string]bool{}
	closedReasoning := map[string]bool{}
	openTool := map[string]bool{}
	closedTool := map[string]bool{}
	openStep := map[string]int{}

	contentAfterTerminal := func(i int, kind driver.StreamKind) {
		if terminal {
			out = append(out, violationf("EVT-02",
				"payload %d (%s) emitted after a terminal frame; the terminal frame MUST be last",
				i, kind))
		}
	}

	for i, p := range payloads {
		if p.Sequence != 0 || p.Seq != 0 || !p.Timestamp.IsZero() {
			out = append(out, violationf("EVT-10",
				"payload %d (%s) carries driver-set Sequence=%d Seq=%d Timestamp=%v; Sequence/Seq/Timestamp are backfilled by the SDK (StreamPayload, EventSink.EmitStream docs)",
				i, p.Kind, p.Sequence, p.Seq, p.Timestamp))
		}
		if !knownStreamKind(p.Kind) {
			if terminal {
				out = append(out, violationf("EVT-02", "payload %d (%s) emitted after the terminal frame; terminal MUST be last", i, p.Kind))
			}
			continue
		}
		if terminal && p.Kind == driver.StreamDropped {
			out = append(out, violationf("EVT-02", "payload %d (%s) emitted after the terminal frame; terminal MUST be last", i, p.Kind))
		}
		if p.Role != driver.RoleAssistant {
			out = append(out, violationf("EVT-09",
				"payload %d (%s) carries Role=%q; drivers MUST leave Role at the zero value on every Kind they emit (Role docs)",
				i, p.Kind, p.Role))
		}

		switch p.Kind {
		case driver.StreamRunStarted:
			started++
			if started > 1 {
				out = append(out, violationf("EVT-01", "payload %d: duplicate run.started (%d total)", i, started))
			}
			if p.MessageID != "" || p.ToolCallID != "" {
				out = append(out, violationf("EVT-13",
					"payload %d (run.started) carries MessageID=%q ToolCallID=%q; run.* frames leave both empty", i, p.MessageID, p.ToolCallID))
			}
			contentAfterTerminal(i, p.Kind)

		case driver.StreamRunFinished:
			terminalCount++
			contentAfterTerminal(i, p.Kind)
			if p.MessageID != "" || p.ToolCallID != "" {
				out = append(out, violationf("EVT-13",
					"payload %d (run.finished) carries MessageID=%q ToolCallID=%q; run.* frames leave both empty", i, p.MessageID, p.ToolCallID))
			}
			for id := range openText {
				out = append(out, violationf("EVT-11", "payload %d: run.finished with text lifecycle %q still open", i, id))
			}
			for id := range openReasoning {
				out = append(out, violationf("EVT-11", "payload %d: run.finished with reasoning lifecycle %q still open", i, id))
			}
			for id := range openTool {
				out = append(out, violationf("EVT-11", "payload %d: run.finished with tool_call lifecycle %q still open", i, id))
			}
			for name, count := range openStep {
				if count > 0 {
					out = append(out, violationf("EVT-11", "payload %d: run.finished with step lifecycle %q still open (%d)", i, name, count))
				}
			}
			terminal = true

		case driver.StreamRunError:
			terminalCount++
			contentAfterTerminal(i, p.Kind)
			if p.Error == nil {
				out = append(out, violationf("EVT-02", "payload %d: run.error without Error (StreamPayload docs: Error on error)", i))
			}
			if p.MessageID != "" || p.ToolCallID != "" {
				out = append(out, violationf("EVT-13",
					"payload %d (run.error) carries MessageID=%q ToolCallID=%q; run.* frames leave both empty", i, p.MessageID, p.ToolCallID))
			}
			for id := range openText {
				out = append(out, violationf("EVT-11", "payload %d: run.error with text lifecycle %q still open", i, id))
			}
			for id := range openReasoning {
				out = append(out, violationf("EVT-11", "payload %d: run.error with reasoning lifecycle %q still open", i, id))
			}
			for id := range openTool {
				out = append(out, violationf("EVT-11", "payload %d: run.error with tool_call lifecycle %q still open", i, id))
			}
			for name, count := range openStep {
				if count > 0 {
					out = append(out, violationf("EVT-11", "payload %d: run.error with step lifecycle %q still open (%d)", i, name, count))
				}
			}
			terminal = true

		case driver.StreamStepStarted:
			if p.Name == "" {
				out = append(out, violationf("EVT-07", "payload %d (%s) without Name; step frames require Name", i, p.Kind))
			} else {
				openStep[p.Name]++
			}
			contentAfterTerminal(i, p.Kind)

		case driver.StreamStepFinished:
			if p.Name == "" {
				out = append(out, violationf("EVT-07", "payload %d (%s) without Name; step frames require Name", i, p.Kind))
			} else if openStep[p.Name] == 0 {
				out = append(out, violationf("EVT-07", "payload %d: step.finished for %q without a matching step.started", i, p.Name))
			} else {
				openStep[p.Name]--
			}
			contentAfterTerminal(i, p.Kind)

		case driver.StreamTextStart:
			contentAfterTerminal(i, p.Kind)
			if p.MessageID == "" {
				out = append(out, violationf("EVT-03", "payload %d: text.start without MessageID", i))
			} else if openText[p.MessageID] || closedText[p.MessageID] {
				out = append(out, violationf("EVT-03", "payload %d: text.start reopens MessageID %q", i, p.MessageID))
			} else {
				openText[p.MessageID] = true
			}

		case driver.StreamTextContent:
			contentAfterTerminal(i, p.Kind)
			if p.MessageID == "" {
				out = append(out, violationf("EVT-03", "payload %d: text.content without MessageID", i))
			} else if !openText[p.MessageID] {
				out = append(out, violationf("EVT-03", "payload %d: text.content for MessageID %q outside an open text lifecycle", i, p.MessageID))
			}
			if p.Delta == "" {
				out = append(out, violationf("EVT-04", "payload %d: text.content with empty Delta; StreamPayload docs require Delta non-empty", i))
			}

		case driver.StreamTextEnd:
			if p.MessageID == "" {
				out = append(out, violationf("EVT-03", "payload %d: text.end without MessageID", i))
			} else if !openText[p.MessageID] {
				out = append(out, violationf("EVT-03", "payload %d: text.end for MessageID %q that is not open", i, p.MessageID))
			} else {
				delete(openText, p.MessageID)
				closedText[p.MessageID] = true
			}

		case driver.StreamToolCallStart:
			contentAfterTerminal(i, p.Kind)
			if p.ToolCallID == "" || p.Name == "" {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.start requires ToolCallID and Name (got ToolCallID=%q Name=%q)", i, p.ToolCallID, p.Name))
			}
			if p.ToolCallID != "" {
				if openTool[p.ToolCallID] || closedTool[p.ToolCallID] {
					out = append(out, violationf("EVT-05", "payload %d: tool_call.start reopens ToolCallID %q", i, p.ToolCallID))
				} else {
					openTool[p.ToolCallID] = true
				}
			}

		case driver.StreamToolCallArgs:
			contentAfterTerminal(i, p.Kind)
			if p.ToolCallID == "" {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.args without ToolCallID", i))
			} else if !openTool[p.ToolCallID] {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.args for ToolCallID %q outside an open tool_call lifecycle", i, p.ToolCallID))
			}
			if p.Delta == "" {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.args with empty Delta; StreamPayload docs require Delta non-empty", i))
			}

		case driver.StreamToolCallEnd:
			if p.ToolCallID == "" {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.end without ToolCallID", i))
			} else if !openTool[p.ToolCallID] {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.end for ToolCallID %q that is not open", i, p.ToolCallID))
			} else {
				delete(openTool, p.ToolCallID)
				closedTool[p.ToolCallID] = true
			}

		case driver.StreamToolCallResult:
			contentAfterTerminal(i, p.Kind)
			if p.ToolCallID == "" {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.result without ToolCallID", i))
			} else if !openTool[p.ToolCallID] && !closedTool[p.ToolCallID] {
				out = append(out, violationf("EVT-05", "payload %d: tool_call.result for unknown ToolCallID %q (no prior tool_call.start)", i, p.ToolCallID))
			}

		case driver.StreamReasoningStart:
			contentAfterTerminal(i, p.Kind)
			if p.MessageID == "" {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.start without MessageID", i))
			} else if openReasoning[p.MessageID] || closedReasoning[p.MessageID] {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.start reopens MessageID %q", i, p.MessageID))
			} else {
				openReasoning[p.MessageID] = true
			}

		case driver.StreamReasoningContent:
			contentAfterTerminal(i, p.Kind)
			if p.MessageID == "" {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.content without MessageID", i))
			} else if !openReasoning[p.MessageID] {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.content for MessageID %q outside an open reasoning lifecycle", i, p.MessageID))
			}
			if p.Delta == "" {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.content with empty Delta", i))
			}

		case driver.StreamReasoningEnd:
			if p.MessageID == "" {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.end without MessageID", i))
			} else if !openReasoning[p.MessageID] {
				out = append(out, violationf("EVT-06", "payload %d: reasoning.end for MessageID %q that is not open", i, p.MessageID))
			} else {
				delete(openReasoning, p.MessageID)
				closedReasoning[p.MessageID] = true
			}

		case driver.StreamHITLRequested:
			contentAfterTerminal(i, p.Kind)
			if p.HITLRequested == nil {
				out = append(out, violationf("EVT-08", "payload %d: hitl.requested without HITLRequested envelope", i))
			}

		case driver.StreamHITLResolved:
			contentAfterTerminal(i, p.Kind)
			if p.HITLResolved == nil {
				out = append(out, violationf("EVT-08", "payload %d: hitl.resolved without HITLResolved envelope", i))
			}

		case driver.StreamDropped:
			// Provider-side loss report. It obeys the same run.started-first
			// and terminal-last envelope ordering as every other payload.
		}
	}
	if started != 1 {
		out = append(out, violationf("EVT-01", "run.started count = %d, want exactly one", started))
	}
	if terminalCount != 1 {
		out = append(out, violationf("EVT-02", "terminal frame count = %d, want exactly one run.finished or run.error", terminalCount))
	}
	if terminalCount == 1 {
		for i := len(payloads) - 1; i >= 0; i-- {
			if !knownStreamKind(payloads[i].Kind) || payloads[i].Kind == driver.StreamDropped {
				continue
			}
			if payloads[i].Kind != driver.StreamRunFinished && payloads[i].Kind != driver.StreamRunError {
				out = append(out, violationf("EVT-02", "last normalized payload is %q, want the unique terminal frame", payloads[i].Kind))
			}
			break
		}
	}
	return out
}

// VerifyStreamCapability cross-checks a driver's declared StreamCapability
// against the payloads it actually emitted (clause EVT-12). Only negative
// declarations are enforced: a false capability flag means the corresponding
// kinds must not appear (StreamCapability docs: every field is additive).
func VerifyStreamCapability(capability driver.StreamCapability, payloads []driver.StreamPayload) []Violation {
	var out []Violation
	seenArgs, seenReasoning, seenHITL := false, false, false
	for i, p := range payloads {
		switch p.Kind {
		case driver.StreamToolCallArgs:
			if !capability.ToolCallArgs && !seenArgs {
				seenArgs = true
				out = append(out, violationf("EVT-12",
					"payload %d: tool_call.args emitted but StreamCapability.ToolCallArgs is false", i))
			}
		case driver.StreamReasoningStart, driver.StreamReasoningContent, driver.StreamReasoningEnd:
			if !capability.Reasoning && !seenReasoning {
				seenReasoning = true
				out = append(out, violationf("EVT-12",
					"payload %d: %s emitted but StreamCapability.Reasoning is false", i, p.Kind))
			}
		case driver.StreamHITLRequested, driver.StreamHITLResolved:
			if !capability.HITL && !seenHITL {
				seenHITL = true
				out = append(out, violationf("EVT-12",
					"payload %d: %s emitted but StreamCapability.HITL is false", i, p.Kind))
			}
		}
	}
	return out
}

// VerifyRunEvents checks the driver-side RunEvent envelope contract
// (clauses RUN-01 .. RUN-03; TRN-* for embedded transcript items) against
// events exactly as the driver emitted them.
func VerifyRunEvents(events []driver.RunEvent) []Violation {
	var out []Violation
	for i, ev := range events {
		if ev.Seq != 0 {
			out = append(out, violationf("RUN-03",
				"event %d (%s) carries driver-set Seq=%d; Seq is assigned monotonically by the SDK per-run (RunEvent docs)", i, ev.Type, ev.Seq))
		}
		switch ev.Type {
		case driver.RunEventChunk:
			if ev.Stream != "stdout" && ev.Stream != "stderr" {
				out = append(out, violationf("RUN-01",
					`event %d: chunk with Stream=%q, want "stdout" or "stderr" (RunEvent field usage)`, i, ev.Stream))
			}
		case driver.RunEventItem:
			if ev.Item == nil {
				out = append(out, violationf("RUN-02", "event %d: item event with nil Item (RunEvent field usage)", i))
			} else {
				out = append(out, verifyTranscriptItem(*ev.Item, fmt.Sprintf("event %d", i))...)
			}
		default:
			// invocation/spawn/runtime/lifecycle and vendor types carry
			// Text/Metadata/Data with no structural requirement.
		}
	}
	return out
}

// VerifyTranscript checks TranscriptItem kind field rules (clauses
// TRN-01 .. TRN-04) for a full transcript slice.
func VerifyTranscript(items []driver.TranscriptItem) []Violation {
	var out []Violation
	for i, item := range items {
		out = append(out, verifyTranscriptItem(item, fmt.Sprintf("transcript item %d", i))...)
	}
	return out
}

func verifyTranscriptItem(item driver.TranscriptItem, where string) []Violation {
	var out []Violation
	switch item.Kind {
	case driver.TranscriptAssistant, driver.TranscriptThinking, driver.TranscriptUser,
		driver.TranscriptStdout, driver.TranscriptStderr, driver.TranscriptSystem,
		driver.TranscriptSummary, driver.TranscriptQuestion, driver.TranscriptFailure:
		if item.Text == "" {
			out = append(out, violationf("TRN-01", "%s: kind %q requires Text (TranscriptItem kind field rules)", where, item.Kind))
		}
	case driver.TranscriptToolCall:
		if item.ToolName == "" {
			out = append(out, violationf("TRN-02", "%s: tool_call requires ToolName", where))
		}
	case driver.TranscriptToolResult:
		if item.ToolUseID == "" {
			out = append(out, violationf("TRN-03", "%s: tool_result requires ToolUseID", where))
		}
	case driver.TranscriptInit, driver.TranscriptResult:
		// All fields optional/recommended.
	}
	if item.Delta && item.Kind != driver.TranscriptAssistant && item.Kind != driver.TranscriptThinking {
		out = append(out, violationf("TRN-04", "%s: Delta=true on kind %q; Delta is allowed for assistant and thinking only", where, item.Kind))
	}
	return out
}

// VerifyOutcome checks Response-level structural invariants (clauses
// RSP-01 .. RSP-03 plus TRN-* for the transcript) together with the error
// returned by Driver.Run. A non-nil runErr makes a valid checkpoint unsafe
// even when the Response's process fields happen to look successful.
func VerifyOutcome(resp *driver.Response, runErr error) []Violation {
	out := VerifyResponse(resp)
	if runErr != nil && resp != nil && resp.Checkpoint != nil && resp.Checkpoint.Valid {
		out = append(out, violationf("RSP-01",
			"Checkpoint.Valid=true when Driver.Run returned error %v; infrastructure or execution errors MUST NOT produce a persistable checkpoint",
			runErr))
	}
	return out
}

// VerifyCheckpointCodec checks RSP-04 without running a paid/live probe. A
// valid checkpoint is meaningful only when the same Driver exposes a codec
// that accepts its resume identity and derives a non-empty guard.
func VerifyCheckpointCodec(d driver.Driver, resp *driver.Response) (out []Violation) {
	if resp == nil || resp.Checkpoint == nil || !resp.Checkpoint.Valid || resp.Checkpoint.State == nil {
		return nil
	}
	provider, ok := d.(driver.SessionCodecProvider)
	if !ok {
		return []Violation{violationf("RSP-04",
			"valid checkpoint returned by a driver without SessionCodecProvider")}
	}
	codec, panicked := callSessionCodec(provider)
	if panicked {
		return []Violation{violationf("RSP-04",
			"valid checkpoint returned but SessionCodec() panicked")}
	}
	if nilDynamicValue(codec) {
		return []Violation{violationf("RSP-04",
			"valid checkpoint returned but SessionCodecProvider returned nil or a typed-nil codec")}
	}
	defer func() {
		if recover() != nil {
			out = append(out, violationf("RSP-04", "session codec panicked while verifying a valid checkpoint"))
		}
	}()
	params := codec.ToParams(resp.Checkpoint.State)
	if params.ResumeID != resp.Checkpoint.State.ResumeID {
		out = append(out, violationf("RSP-04",
			"codec.ToParams(checkpoint) changed ResumeID from %q to %q", resp.Checkpoint.State.ResumeID, params.ResumeID))
	}
	if codec.GuardFingerprint(params) == "" {
		out = append(out, violationf("RSP-04", "GuardFingerprint is empty for a valid checkpoint"))
	}
	restored := codec.FromParams(params)
	if restored == nil {
		out = append(out, violationf("RSP-04", "codec.FromParams returned nil for a valid checkpoint"))
	} else if roundTrip := codec.ToParams(restored); !reflect.DeepEqual(roundTrip, params) {
		out = append(out, violationf("RSP-04",
			"checkpoint codec round-trip is lossy: first=%+v second=%+v", params, roundTrip))
	}
	return out
}

// VerifyResponse checks the invariants observable from Response alone. Use
// VerifyOutcome when the Driver.Run error is available.
func VerifyResponse(resp *driver.Response) []Violation {
	if resp == nil {
		return nil
	}
	out := VerifyTranscript(resp.Transcript)

	if cp := resp.Checkpoint; cp != nil && cp.Valid {
		if cp.State == nil {
			out = append(out, violationf("RSP-01", "Checkpoint.Valid=true with nil State (Checkpoint docs: Valid=false for non-resumable runs)"))
		} else if cp.State.ResumeID == "" {
			out = append(out, violationf("RSP-01", "Checkpoint.Valid=true with empty State.ResumeID; a resumable checkpoint needs the provider session handle"))
		}
		if resp.ExitCode != 0 || resp.Signal != "" || resp.TimedOut || resp.Failure != nil {
			out = append(out, violationf("RSP-01",
				"Checkpoint.Valid=true on unsafe outcome (exit=%d signal=%q timed_out=%v failure=%v); Checkpoint requires a clean successful run",
				resp.ExitCode, resp.Signal, resp.TimedOut, resp.Failure != nil))
		}
	}

	if f := resp.Failure; f != nil {
		human := f.Code == driver.FailureReject || f.Code == driver.FailureTimeout
		if human && f.HumanDecision == nil {
			out = append(out, violationf("RSP-02",
				"Failure.Code=%q without HumanDecision; RunFailure docs: HumanDecision is non-nil exactly when Code is FailureReject or FailureTimeout", f.Code))
		}
		if !human && f.HumanDecision != nil {
			out = append(out, violationf("RSP-02",
				"Failure.Code=%q with HumanDecision set; RunFailure docs: HumanDecision is non-nil exactly when Code is FailureReject or FailureTimeout", f.Code))
		}
	}

	if rs := resp.RawStreams; rs != nil && resp.Output != "" && resp.Output == rs.Stdout {
		trimmed := strings.TrimSpace(rs.Stdout)
		if strings.Contains(trimmed, "\n") && strings.HasPrefix(trimmed, "{") {
			out = append(out, violationf("RSP-03",
				"Output equals the full protocol-shaped stdout dump; Response.Output docs: final assistant-facing text only, no raw stdout dumps"))
		}
	}
	if rs := resp.RawStreams; rs != nil && rs.Terminal != nil {
		if strings.TrimSpace(rs.Terminal.Event) == "" {
			out = append(out, violationf("RSP-05", "RawStreams.Terminal has an empty provider event name"))
		}
		if len(rs.Terminal.JSON) == 0 || !json.Valid(rs.Terminal.JSON) {
			out = append(out, violationf("RSP-05", "RawStreams.Terminal.JSON is not valid provider terminal JSON"))
		}
	}
	return out
}

// VerifyTranscriptMirror checks the hard SPI invariant that collecting every
// driver-emitted RunEventItem in emission order exactly reproduces
// Response.Transcript. Streaming deltas participate when and only when the
// driver also includes them in the final Transcript.
func VerifyTranscriptMirror(events []driver.RunEvent, transcript []driver.TranscriptItem) []Violation {
	var items []driver.TranscriptItem
	for _, ev := range events {
		if ev.Type == driver.RunEventItem && ev.Item != nil {
			items = append(items, *ev.Item)
		}
	}
	if len(items) == 0 && len(transcript) == 0 {
		return nil
	}
	if !reflect.DeepEqual(items, transcript) {
		return []Violation{violationf("RUN-04",
			"RunEventItem sequence (%d items) does not mirror Response.Transcript (%d items)", len(items), len(transcript))}
	}
	return nil
}
