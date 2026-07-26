package adaptor

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Consumer-facing aliases for audit types that already live in the driver
// SPI package, so application code only imports this package.
type (
	// Usage is normalized token/cost accounting. Values may be zero when
	// the driver cannot observe a metric.
	Usage = driver.Usage
	// RawStreams captures the complete raw stdout/stderr of one run.
	RawStreams = driver.RawStreams
	// TranscriptItem is the normalized semantic transcript unit.
	TranscriptItem = driver.TranscriptItem
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
	// Usage is normalized token/cost accounting (zero when unobserved).
	Usage Usage
	// Metadata is adapter-reported result metadata.
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
		Model:      resp.Model,
		Provider:   resp.Provider,
		Text:       resp.Output,
		Summary:    resp.Summary,
		Metadata:   maps.Clone(resp.Metadata),
		transcript: append([]TranscriptItem(nil), resp.Transcript...),
		structured: resp.StructuredOutput,
		services:   append([]ServiceReport(nil), resp.RuntimeServices...),
	}
	if resp.Usage != nil {
		res.Usage = *resp.Usage
	}
	if resp.RawStreams != nil {
		res.raw = *resp.RawStreams
	}
	return res
}

// Raw returns the complete raw stdout/stderr captured during the run — the
// stable audit/replay surface. It is deliberately separate from Text and
// Transcript (the layers never contaminate each other).
func (r *Result) Raw() RawStreams {
	if r == nil {
		return RawStreams{}
	}
	return r.raw
}

// Transcript returns the normalized semantic item stream parsed by the
// driver. The returned slice is a copy.
func (r *Result) Transcript() []TranscriptItem {
	if r == nil {
		return nil
	}
	return append([]TranscriptItem(nil), r.transcript...)
}

// Services returns the runtime-service execution reports for this run.
// The returned slice is a copy.
//
// TODO(P4.5): the consumer-facing shape may gain the typed MCP field when
// RuntimeServiceRef.MCP lands; until then this echoes the driver report.
func (r *Result) Services() []ServiceReport {
	if r == nil {
		return nil
	}
	return append([]ServiceReport(nil), r.services...)
}

// Decode unmarshals the run's structured output into v. When the driver
// produced a validated structured-output payload, that JSON is used;
// otherwise Decode falls back to interpreting Text as a JSON document.
//
// TODO(P3.5): RunAs[T] / WithSchema[T] complete the pipeline (schema
// derivation, mode negotiation, local validation). P0 provides the basic
// JSON decode only.
func (r *Result) Decode(v any) error {
	if r == nil {
		return errors.New("adaptor: Decode called on nil Result")
	}
	if r.structured != nil && len(r.structured.RawJSON) > 0 {
		return json.Unmarshal(r.structured.RawJSON, v)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return errors.New("adaptor: no structured output to decode")
	}
	return json.Unmarshal([]byte(text), v)
}
