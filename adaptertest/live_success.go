package adaptertest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Live success is stronger than structural SPI validity. A correctly represented
// failed Response remains legal input to VerifyOutcome.
func checkLiveSuccess(t *testing.T, d driver.Driver, resp *driver.Response, runErr error) bool {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("live diagnostic: %s", liveDiagnostic(resp, runErr))
		}
	})
	reportLiveViolations(t, VerifyOutcome(resp, runErr))
	if runErr != nil || resp.ExitCode != 0 || resp.Signal != "" || resp.TimedOut || resp.Failure != nil {
		t.Error("LIV-01: live success requires no Go error, exit 0, no signal, no timeout and no provider Failure")
		return false
	}
	if d.Descriptor().Sessions.SupportsResume {
		cp := resp.Checkpoint
		if cp == nil || !cp.Valid || cp.State == nil || cp.State.ResumeID == "" {
			t.Error("LIV-03: the resume-capable live success probe requires a healthy resumable checkpoint")
		}
	}
	reportLiveViolations(t, VerifyCheckpointCodec(d, resp))
	return true
}

// Verifier messages can contain provider-controlled text, identifiers and
// errors. Keep the clause identity; the bounded diagnostic below supplies safe
// outcome and integrity evidence without echoing those values.
func reportLiveViolations(t *testing.T, violations []Violation) {
	t.Helper()
	for i, v := range violations {
		if i == 25 {
			t.Errorf("live contract violations: %d additional details withheld", len(violations)-i)
			return
		}
		t.Errorf("%s: live contract violation (provider-controlled details withheld)", v.Clause)
	}
}

// The requested object has exactly one property. Token decoding also rejects
// duplicate properties, trailing documents and provider terminal wrappers.
func liveSchemaProbeValue(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	key, err := d.Token()
	if err != nil || key != "ok" {
		return false
	}
	var ok bool
	if d.Decode(&ok) != nil || !ok || d.More() {
		return false
	}
	token, err = d.Token()
	if err != nil || token != json.Delim('}') {
		return false
	}
	var tail any
	return d.Decode(&tail) == io.EOF
}

func checkLiveStructuredValue(t *testing.T, result *driver.StructuredOutput) {
	t.Helper()
	if result == nil {
		t.Error("SO-02: native run returned no StructuredOutput")
		return
	}
	if result.Source != driver.StructuredOutputSourceNative {
		t.Error("SO-02: native probe did not report the native source")
	}
	if !result.Valid {
		t.Error("SO-02: native probe did not report a validated value")
	}
	if !liveSchemaProbeValue(result.RawJSON) {
		t.Error("SO-02: native RawJSON must be exactly one object property ok with boolean true")
	}
	if result.Value != nil {
		raw, err := json.Marshal(result.Value)
		if err != nil || !liveSchemaProbeValue(raw) {
			t.Error("SO-02: StructuredOutput.Value disagrees with the requested and encoded business value")
		}
	}
}

type liveBytesDiagnostic struct {
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func liveBytes(raw []byte) liveBytesDiagnostic {
	return liveBytesDiagnostic{len(raw), fmt.Sprintf("%x", sha256.Sum256(raw))}
}

// No provider strings, JSON keys/values, stderr tails or error messages escape.
// Digests describe complete in-memory bytes, not a truncated surrogate. This
// establishes presence/integrity while provider owners investigate protocol
// details using their explicitly isolated, redacted diagnostics.
func liveDiagnostic(resp *driver.Response, runErr error) string {
	type diagnostic struct {
		Exit                                                 int                 `json:"exit"`
		Signal                                               liveBytesDiagnostic `json:"signal"`
		TimedOut                                             bool                `json:"timed_out"`
		Failure                                              bool                `json:"failure"`
		ErrorType                                            string              `json:"error_type"`
		Raw                                                  bool                `json:"raw_present"`
		Stdout, Stderr, FailureCode, FailureMessage          liveBytesDiagnostic
		Terminal                                             bool `json:"terminal_present"`
		TerminalEvent, TerminalJSON                          liveBytesDiagnostic
		TerminalJSONValid                                    bool `json:"terminal_json_valid"`
		Checkpoint, CheckpointValid, ResumeID                bool
		Structured, StructuredNative, StructuredValid        bool
		StructuredJSON                                       liveBytesDiagnostic
		StructuredJSONValid, ProbeValueMatches, ValuePresent bool
		ValidationErrors                                     int
	}
	d := diagnostic{Exit: resp.ExitCode, Signal: liveBytes([]byte(resp.Signal)), TimedOut: resp.TimedOut, Failure: resp.Failure != nil, ErrorType: fmt.Sprintf("%T", runErr)}
	if failure := resp.Failure; failure != nil {
		d.FailureCode = liveBytes([]byte(failure.Code))
		d.FailureMessage = liveBytes([]byte(failure.Message))
	}
	if raw := resp.RawStreams; raw != nil {
		d.Raw = true
		d.Stdout = liveBytes([]byte(raw.Stdout))
		d.Stderr = liveBytes([]byte(raw.Stderr))
		if terminal := raw.Terminal; terminal != nil {
			d.Terminal = true
			d.TerminalEvent = liveBytes([]byte(terminal.Event))
			d.TerminalJSON = liveBytes(terminal.JSON)
			d.TerminalJSONValid = json.Valid(terminal.JSON)
		}
	}
	if cp := resp.Checkpoint; cp != nil {
		d.Checkpoint = true
		d.CheckpointValid = cp.Valid
		d.ResumeID = cp.State != nil && cp.State.ResumeID != ""
	}
	if so := resp.StructuredOutput; so != nil {
		d.Structured = true
		d.StructuredNative = so.Source == driver.StructuredOutputSourceNative
		d.StructuredValid = so.Valid
		d.StructuredJSON = liveBytes(so.RawJSON)
		d.StructuredJSONValid = json.Valid(so.RawJSON)
		d.ProbeValueMatches = liveSchemaProbeValue(so.RawJSON)
		d.ValuePresent = so.Value != nil
		d.ValidationErrors = len(so.ValidationErrors)
	}
	raw, _ := json.Marshal(d)
	return string(raw)
}
