package sessionrecorder

import (
	"context"
	"errors"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

// ExposurePolicy opts terminal records into diagnostic content. The zero
// value records only run identity, turns, answer, summary and error identity.
// It does not filter other typed events; hosts still own their event redaction.
type ExposurePolicy struct {
	// IncludeMetadata records provider/model identity, result metadata and business error details.
	IncludeMetadata bool
	// IncludeUsage records observed token usage, including observed zero values.
	IncludeUsage bool
	// IncludeRawStreams records complete stdout/stderr in Diagnostics.
	IncludeRawStreams bool
	// IncludeProviderResult records the provider terminal payload in Diagnostics.
	IncludeProviderResult bool
	// IncludeTranscript records semantic transcript entries in Diagnostics.
	IncludeTranscript bool
	// IncludeServices records observed runtime services in Diagnostics.
	IncludeServices bool
}

// ResultDiagnostics contains explicitly enabled audit data for a terminal
// record. Replay retains it on EventRecord because Result's private audit
// fields cannot be reconstructed through the public SDK contract.
type ResultDiagnostics struct {
	// Raw contains opted-in process streams and/or provider terminal payload.
	Raw *adaptor.RawStreams `json:"raw,omitempty"`
	// Transcript contains opted-in semantic transcript entries.
	Transcript []adaptor.TranscriptItem `json:"transcript,omitempty"`
	// Services contains opted-in observed service reports.
	Services []adaptor.ServiceReport `json:"services,omitempty"`
}

// WithExposure configures diagnostic retention for Done and TurnEnd, including
// partial results inside business errors. Defaults omit all diagnostics.
func WithExposure(policy ExposurePolicy) EventOption {
	return func(r *eventRecorder) { r.exposure = policy }
}

func projectResult(result *adaptor.Result, policy ExposurePolicy) *adaptor.Result {
	if result == nil {
		return nil
	}
	safe := &adaptor.Result{RunID: result.RunID, Turns: result.Turns, Text: result.Text, Summary: result.Summary}
	if policy.IncludeUsage && result.Usage != nil {
		usage := *result.Usage
		safe.Usage = &usage
	}
	if policy.IncludeMetadata {
		safe.Model, safe.Provider = result.Model, result.Provider
		if result.Metadata != nil {
			safe.Metadata = make(map[string]string, len(result.Metadata))
			for k, v := range result.Metadata {
				safe.Metadata[k] = v
			}
		}
	}
	return safe
}

func projectError(err error, policy ExposurePolicy) error {
	var business *adaptor.RunError
	if !errors.As(err, &business) {
		return err
	}
	safe := &adaptor.RunError{Reason: business.Reason, Message: business.Message, Result: projectResult(business.Result, policy)}
	if policy.IncludeMetadata {
		safe.Details = business.Details
	}
	var projected error = safe
	if errors.Is(err, context.Canceled) {
		projected = errors.Join(projected, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		projected = errors.Join(projected, context.DeadlineExceeded)
	}
	return projected
}

func projectTerminal(ev adaptor.Event, policy ExposurePolicy) (adaptor.Event, *ResultDiagnostics) {
	var result *adaptor.Result
	var failure error
	switch e := ev.(type) {
	case adaptor.Done:
		result, failure = e.Result, e.Err
		ev = adaptor.WithEventMeta(adaptor.Done{Result: projectResult(e.Result, policy), Err: projectError(e.Err, policy)}, e.Meta())
	case adaptor.TurnEnd:
		result, failure = e.Result, e.Err
		ev = adaptor.WithEventMeta(adaptor.TurnEnd{Turn: e.Turn, Result: projectResult(e.Result, policy), Err: projectError(e.Err, policy), MessageIDs: append([]string(nil), e.MessageIDs...)}, e.Meta())
	default:
		return ev, nil
	}
	if result == nil {
		var business *adaptor.RunError
		if errors.As(failure, &business) {
			result = business.Result
		}
	}
	if result == nil {
		return ev, nil
	}
	extra := &ResultDiagnostics{}
	if policy.IncludeRawStreams || policy.IncludeProviderResult {
		raw := result.Raw()
		if !policy.IncludeRawStreams {
			raw.Stdout, raw.Stderr = "", ""
		}
		if !policy.IncludeProviderResult {
			raw.Terminal = nil
		}
		extra.Raw = &raw
	}
	if policy.IncludeTranscript {
		extra.Transcript = result.Transcript()
	}
	if policy.IncludeServices {
		extra.Services = result.Services()
	}
	if extra.Raw == nil && len(extra.Transcript) == 0 && len(extra.Services) == 0 {
		extra = nil
	}
	return ev, extra
}

// recordedResult omits disabled diagnostic fields from the envelope entirely.
// Its field names also decode existing records made from Result directly.
type recordedResult struct {
	RunID    string `json:",omitempty"`
	Turns    int
	Text     string
	Summary  string
	Model    string            `json:",omitempty"`
	Provider string            `json:",omitempty"`
	Usage    *adaptor.Usage    `json:",omitempty"`
	Metadata map[string]string `json:",omitempty"`
}

func recordResult(result *adaptor.Result) *recordedResult {
	if result == nil {
		return nil
	}
	return &recordedResult{RunID: result.RunID, Turns: result.Turns, Text: result.Text, Summary: result.Summary, Model: result.Model, Provider: result.Provider, Usage: result.Usage, Metadata: result.Metadata}
}

func (r *recordedResult) restore() *adaptor.Result {
	if r == nil {
		return nil
	}
	return &adaptor.Result{RunID: r.RunID, Turns: r.Turns, Text: r.Text, Summary: r.Summary, Model: r.Model, Provider: r.Provider, Usage: r.Usage, Metadata: r.Metadata}
}
