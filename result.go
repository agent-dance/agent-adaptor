package adaptor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

// Consumer-facing aliases for audit types that already live in the driver
// SPI package, so application code only imports this package.
type (
	// Usage is normalized token/cost accounting. Individual observed values
	// may be zero; Result.Usage is nil when the provider reported no usage.
	Usage = driver.Usage
	// RawStreams captures the complete raw stdout/stderr of one run.
	RawStreams = driver.RawStreams
	// TerminalPayload preserves the exact provider terminal JSON recognized
	// by the driver parser and its provider-native event name.
	TerminalPayload = driver.TerminalPayload
	// ServiceReport is the execution report for one ensured runtime
	// service.
	ServiceReport = driver.RuntimeServiceReport
)

// Result is the outcome of one successful run. High-frequency fields are
// flat; audit surfaces are gathered behind Raw() / Transcript() / Services();
// structured output decodes via Decode.
//
// A run that completed but failed at the business level does not return a
// Result directly — it returns a *RunError whose Result field carries this
// same value (see RunError).
type Result struct {
	// Turns is the number of provider turns in this execution.
	Turns     int
	messages  []MessageOutcome
	subagents []SubagentResult
	// RunID is the SDK-assigned execution identifier.
	RunID string
	// Model is the effective model reported by the driver.
	Model string
	// Provider is the upstream provider reported by the driver.
	Provider string
	// Text is the final assistant-facing text. It never contains raw
	// stdout/stderr dumps, Summary text, or provider terminal JSON.
	Text string
	// Summary is a short host-facing label suitable for lists and logs,
	// deliberately separate from Text.
	Summary string
	// Usage is normalized token/cost accounting. nil means the provider did
	// not report usage; a non-nil zero value means usage was observed and all
	// normalized metrics were explicitly zero.
	Usage *Usage
	// Metadata is Driver-reported result metadata.
	Metadata map[string]string

	raw        RawStreams
	transcript []TranscriptItem
	structured *driver.StructuredOutput
	services   []ServiceReport
}

// resultFromResponse translates the driver response into the consumer
// Result. It never inspects Response.Failure — the caller decides whether
// the Result rides a success return or a *RunError.
func resultFromResponse(runID string, resp driver.Response) *Result {
	res := &Result{
		RunID:      runID,
		Turns:      1,
		Model:      resp.Model,
		Provider:   resp.Provider,
		Text:       resp.Output,
		Summary:    resp.Summary,
		Usage:      cloneUsage(resp.Usage),
		Metadata:   maps.Clone(resp.Metadata),
		transcript: publicTranscript(resp.Transcript),
		structured: cloneStructuredOutput(resp.StructuredOutput),
		services:   cloneServiceReports(resp.RuntimeServices),
	}
	if resp.RawStreams != nil {
		res.raw = cloneRawStreams(*resp.RawStreams)
	}
	return res
}

func cloneUsage(usage *Usage) *Usage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

// Raw returns the complete raw stdout/stderr and exact provider terminal JSON
// captured during the run — the stable audit/replay surface. It is deliberately
// separate from Text and Transcript (the layers never contaminate each other).
// The returned Terminal and JSON bytes are deep copies.
func (r *Result) Raw() RawStreams {
	if r == nil {
		return RawStreams{}
	}
	return cloneRawStreams(r.raw)
}

// Transcript returns the normalized semantic item stream parsed by the
// driver. The returned slice is a copy.
func (r *Result) Transcript() []TranscriptItem {
	if r == nil {
		return nil
	}
	return cloneTranscript(r.transcript)
}

// Services returns the runtime-service execution reports for this run. Reports
// observed by the driver are merged by stable service ID with reports from
// services the SDK actually ensured; driver fields override matching SDK fields
// and missing fields are filled from the SDK observation. The returned values,
// including Metadata maps, are copies.
//
// The report deliberately does not echo the typed ServiceRef.MCP declaration.
// Three reasons, in order of weight:
//
//  1. Direction. ServiceRef.MCP is pre-run *input* the host itself authored
//     (via WithServices or the provider it installed); ServiceReport is
//     post-run *observation*. Echoing an input back as an observation invites
//     hosts to read a declaration as evidence the server was actually reached,
//     which no driver reports today.
//  2. Fill honesty. Reports come from the driver (Response.RuntimeServices).
//     A driver echoing a report cannot know the SDK-side MCP declaration, so
//     the field would be populated on the SDK fallback path and empty on the
//     driver path — the exact "sometimes true" shape the SDK avoids.
//  3. Secrecy. The ref→report projection already drops SecretEnv on purpose.
//     MCP carries the endpoint URL and BearerTokenEnvVar next to it; putting
//     that pair into the surface hosts log wholesale works against the same
//     rule.
//
// Hosts that need the declaration have it: it is the value they passed in.
func (r *Result) Services() []ServiceReport {
	if r == nil {
		return nil
	}
	return cloneServiceReports(r.services)
}

func cloneRawStreams(raw RawStreams) RawStreams {
	raw.Terminal = cloneTerminalPayload(raw.Terminal)
	return raw
}

func cloneTerminalPayload(terminal *TerminalPayload) *TerminalPayload {
	if terminal == nil {
		return nil
	}
	clone := *terminal
	clone.JSON = append(json.RawMessage(nil), terminal.JSON...)
	return &clone
}

func cloneTranscript(items []TranscriptItem) []TranscriptItem {
	if items == nil {
		return nil
	}
	cloned := make([]TranscriptItem, len(items))
	for i := range items {
		cloned[i] = items[i]
		if items[i].Message != nil {
			message := *items[i].Message
			cloned[i].Message = &message
		}
		cloned[i].Errors = append([]string(nil), items[i].Errors...)
		cloned[i].Metadata = maps.Clone(items[i].Metadata)
		cloned[i].Input = cloneJSONValue(items[i].Input)
		if items[i].Data != nil {
			cloned[i].Data = make(map[string]any, len(items[i].Data))
			for key, value := range items[i].Data {
				cloned[i].Data[key] = cloneJSONValue(value)
			}
		}
		if items[i].Usage != nil {
			usage := *items[i].Usage
			cloned[i].Usage = &usage
		}
		if items[i].CostUSD != nil {
			cost := *items[i].CostUSD
			cloned[i].CostUSD = &cost
		}
	}
	return cloned
}

func cloneStructuredOutput(value *driver.StructuredOutput) *driver.StructuredOutput {
	if value == nil {
		return nil
	}
	clone := *value
	clone.RawJSON = append(json.RawMessage(nil), value.RawJSON...)
	clone.ValidationErrors = append([]string(nil), value.ValidationErrors...)
	clone.Value = cloneJSONValue(value.Value)
	return &clone
}

func cloneServiceReports(reports []ServiceReport) []ServiceReport {
	if reports == nil {
		return nil
	}
	cloned := make([]ServiceReport, len(reports))
	for i := range reports {
		cloned[i] = reports[i]
		cloned[i].Metadata = maps.Clone(reports[i].Metadata)
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	case []byte:
		return append([]byte(nil), value...)
	case []any:
		clone := make([]any, len(value))
		for i := range value {
			clone[i] = cloneJSONValue(value[i])
		}
		return clone
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, item := range value {
			clone[key] = cloneJSONValue(item)
		}
		return clone
	default:
		return value
	}
}

// Decode unmarshals the run's structured output into v.
//
// When the run requested structured output (WithSchema[T] / RunAs[T]), the
// validated payload is the only source: invalid output (possible under
// SchemaReturnInvalid — the default policy fails the run instead) and
// empty RawJSON are errors. Runs without a schema fall back to interpreting
// Text as a JSON document — the schema-less convenience decode.
func (r *Result) Decode(v any) error {
	if r == nil {
		return errors.New("adaptor: Decode called on nil Result")
	}
	if r.structured != nil {
		if !r.structured.Valid {
			return fmt.Errorf("adaptor: structured output invalid: %s", strings.Join(r.structured.ValidationErrors, "; "))
		}
		if len(r.structured.RawJSON) == 0 {
			return errors.New("adaptor: structured output RawJSON is empty")
		}
		return json.Unmarshal(r.structured.RawJSON, v)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return errors.New("adaptor: no structured output to decode")
	}
	return json.Unmarshal([]byte(text), v)
}

// TranscriptMessage records actual supplemental delivery without asserting a
// provider-specific user role. Text is the original, unframed message.
type TranscriptMessage struct {
	// ID is the opaque message identifier.
	ID string
	// From identifies the message author.
	From Author
	// Mechanism names the provider or core delivery mechanism.
	Mechanism string
	// Boundary is immediate, tool, stop, or turn_end when delivery was attempted.
	Boundary string
	// Text is the original unframed message, unless documented otherwise.
	Text string
}

// TranscriptItem is a parsed provider item or a core-recorded message delivery.
// Kind "message" has Message populated and does not imply a user role.
type TranscriptItem struct {
	// Kind identifies the author authority or transcript item type.
	Kind driver.TranscriptKind
	// Text is the original unframed message, unless documented otherwise.
	Text string
	// Delta marks incremental transcript content.
	Delta bool
	// ToolUseID correlates a tool call and its result.
	ToolUseID string
	// ToolName identifies the tool.
	ToolName string
	// Input is the parsed tool input.
	Input any
	// IsError marks a provider-reported error item.
	IsError bool
	// Model is the provider-reported model.
	Model string
	// SessionID is the provider-reported session identifier.
	SessionID string
	// Usage is observed token and cost accounting, or nil when unobserved.
	Usage *Usage
	// CostUSD is provider-reported cost when available.
	CostUSD *float64
	// Subtype preserves the provider result subtype.
	Subtype string
	// Errors preserves provider error descriptions.
	Errors []string
	// Metadata preserves short provider tags.
	Metadata map[string]string
	// Data preserves structured provider extensions.
	Data map[string]any
	// Message is populated only for Kind "message" and describes actual core-observed delivery.
	Message *TranscriptMessage
}

func publicTranscriptItem(item *driver.TranscriptItem) *TranscriptItem {
	if item == nil {
		return nil
	}
	items := publicTranscript([]driver.TranscriptItem{*item})
	return &items[0]
}
func publicTranscript(items []driver.TranscriptItem) []TranscriptItem {
	if items == nil {
		return nil
	}
	out := make([]TranscriptItem, len(items))
	for i, v := range items {
		out[i] = TranscriptItem{Kind: v.Kind, Text: v.Text, Delta: v.Delta, ToolUseID: v.ToolUseID, ToolName: v.ToolName, Input: v.Input, IsError: v.IsError, Model: v.Model, SessionID: v.SessionID, Usage: v.Usage, CostUSD: v.CostUSD, Subtype: v.Subtype, Errors: v.Errors, Metadata: v.Metadata, Data: v.Data}
	}
	return cloneTranscript(out)
}

// Messages returns the final message outcomes in admission order.
func (r *Result) Messages() []MessageOutcome {
	if r == nil {
		return nil
	}
	return append([]MessageOutcome(nil), r.messages...)
}

// Undelivered returns failed messages with their original text.
func (r *Result) Undelivered() []MessageOutcome {
	var out []MessageOutcome
	for _, m := range r.Messages() {
		if m.State == MessageFailed {
			out = append(out, m)
		}
	}
	return out
}

// SubagentResult is a delegated task's summary and observed usage.
type SubagentResult struct {
	// Member is the delegated agent key.
	Member string
	// Task is the delegation identifier.
	Task string
	// State is the message or delegated task lifecycle state.
	State string
	// Summary is the task result summary.
	Summary string
	// Usage is observed token and cost accounting, or nil when unobserved.
	Usage *Usage
	// Unread contains peer messages not yet consumed by the task.
	Unread []string
}

// Subagents returns copies of the delegated task summaries.
func (r *Result) Subagents() []SubagentResult {
	if r == nil {
		return nil
	}
	return cloneSubagents(r.subagents)
}
func cloneSubagents(in []SubagentResult) []SubagentResult {
	if in == nil {
		return nil
	}
	out := append([]SubagentResult(nil), in...)
	for i := range out {
		out[i].Usage = cloneUsage(out[i].Usage)
		out[i].Unread = append([]string(nil), out[i].Unread...)
	}
	return out
}
func addUsage(a, b *Usage) *Usage {
	if b == nil {
		return a
	}
	if a == nil {
		return cloneUsage(b)
	}
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CachedInputTokens += b.CachedInputTokens
	a.EstimatedCostMilli += b.EstimatedCostMilli
	return a
}
