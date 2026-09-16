package adaptertest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

const liveOracleSelector = "AGENT_ADAPTOR_LIVE_SUCCESS_ORACLE"
const liveOracleSecret = "T23_ARTIFICIAL_SECRET_DO_NOT_LOG_91fc"

// The subprocess executes the actual public suite with an in-memory Driver.
// The default command never invokes a provider, even when testing live probes.
func TestLiveSuccessSuiteOracle(t *testing.T) {
	if selector := os.Getenv(liveOracleSelector); selector != "" {
		entry, scenario, ok := strings.Cut(selector, "/")
		if !ok {
			t.Fatal("invalid oracle selector")
		}
		calls := 0
		d := liveSuccessOracleDriver{scenario: scenario, calls: &calls}
		var candidate driver.Driver = d
		if strings.HasPrefix(scenario, "resume-") {
			candidate = liveSuccessOracleResumeDriver{d}
		}
		prompt := ""
		if strings.HasPrefix(scenario, "custom-") {
			prompt = "Custom application prompt; do not replace this text."
		}
		opts := []Option{WithLiveRun(prompt), WithLiveStructuredOutput()}
		switch scenario {
		case "custom-expect-before":
			opts = append([]Option{WithLiveExpectedOutput("Application answer")}, opts...)
		case "custom-expect-after", "custom-expect-mismatch":
			opts = append(opts, WithLiveExpectedOutput("Application answer"))
		case "custom-expect-empty":
			opts = append(opts, WithLiveExpectedOutput(""))
		case "custom-expect-whitespace":
			opts = append(opts, WithLiveExpectedOutput("  Application answer\n"))
		case "custom-expect-last":
			opts = append(opts, WithLiveExpectedOutput("wrong first expectation"), WithLiveExpectedOutput("Application answer"))
		}
		TestDriver(t, func() driver.Driver { return candidate }, opts...)
		if calls != 1 {
			t.Fatalf("oracle executed %d calls, want exactly one", calls)
		}
		t.Logf("oracle entry=%s calls=%d", entry, calls)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"live_run", "live_structured_output"} {
		type oracleCase struct{ name, clause string }
		scenarios := []oracleCase{
			{"success", ""}, {"exit", "LIV-01"}, {"signal", "LIV-01"}, {"timeout", "LIV-01"},
			{"failure", "LIV-01"}, {"go-error", "LIV-01"}, {"envelope-secret", "EVT-10"},
			{"resume-success", ""}, {"resume-missing", "LIV-03"}, {"resume-invalid", "LIV-03"}, {"resume-codec", "RSP-04"},
		}
		if entry == "live_run" {
			scenarios = append(scenarios, []oracleCase{
				{"empty", "LIV-02"}, {"whitespace", "LIV-02"}, {"wrong-text", "LIV-02"}, {"custom-text", ""}, {"custom-empty", ""},
				{"custom-expect-before", ""}, {"custom-expect-after", ""}, {"custom-expect-empty", ""},
				{"custom-expect-whitespace", ""}, {"custom-expect-last", ""}, {"custom-expect-mismatch", "LIV-02"},
			}...)
		} else {
			for _, name := range []string{"schema-nil", "schema-source", "schema-invalid", "schema-empty", "schema-malformed", "schema-null", "schema-array", "schema-missing", "schema-false", "schema-type", "schema-extra", "schema-duplicate", "schema-trailing", "schema-value", "schema-value-extra", "schema-value-unencodable"} {
				scenarios = append(scenarios, oracleCase{name, "SO-02"})
			}
			scenarios = append(scenarios, oracleCase{"schema-value-absent", ""})
		}
		for _, tc := range scenarios {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, self, "-test.run=^TestLiveSuccessSuiteOracle$/^"+entry+"$", "-test.v")
				cmd.Env = append(os.Environ(), liveOracleSelector+"="+entry+"/"+tc.name)
				out, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatal("oracle child exceeded its external watchdog")
				}
				if strings.Contains(string(out), liveOracleSecret) {
					t.Fatal("live diagnostic leaked the artificial secret canary")
				}
				if strings.Contains(string(out), "SKIP") {
					t.Fatal("oracle child skipped its required probe")
				}
				if tc.clause != "" && (!strings.Contains(string(out), "live diagnostic:") || !strings.Contains(string(out), `"terminal_present":true`) || !strings.Contains(string(out), `"raw_present":true`)) {
					t.Fatalf("failed probe lost its safe outcome and terminal evidence: %s", out)
				}
				if tc.clause == "" {
					if err != nil || !strings.Contains(string(out), "calls=1") {
						t.Fatalf("valid probe rejected: %v\n%s", err, out)
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), tc.clause+":") || !strings.Contains(string(out), "calls=1") {
						t.Fatalf("want executed probe failure with %s, got %v\n%s", tc.clause, err, out)
					}
				}
			})
		}
	}
}

type liveSuccessOracleDriver struct {
	scenario string
	calls    *int
}

func (d liveSuccessOracleDriver) Descriptor() driver.Descriptor {
	return driver.Descriptor{Type: "live-oracle", Sessions: driver.SessionCapability{SupportsResume: strings.HasPrefix(d.scenario, "resume-")}, StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, WorksWithRun: true}}
}
func (liveSuccessOracleDriver) ValidateConfig(any) error { return nil }
func (d liveSuccessOracleDriver) Run(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	*d.calls++
	r := driver.Response{Output: "OK", RawStreams: &driver.RawStreams{Stdout: liveOracleSecret, Stderr: liveOracleSecret, Terminal: &driver.TerminalPayload{Event: liveOracleSecret, JSON: []byte(`{"private":"` + liveOracleSecret + `"}`)}}, StructuredOutput: &driver.StructuredOutput{Source: driver.StructuredOutputSourceNative, Valid: true, RawJSON: []byte(`{"ok":true}`), Value: map[string]any{"ok": true}}}
	if strings.HasPrefix(d.scenario, "resume-") {
		r.Checkpoint = &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: "oracle-session", Data: map[string]string{driver.SessionParamProfileFingerprint: req.ProfilePayload.SessionFingerprint()}}}
	}
	switch d.scenario {
	case "exit":
		r.ExitCode = 1
	case "signal":
		r.Signal = liveOracleSecret
	case "timeout":
		r.TimedOut = true
	case "failure":
		r.Failure = &driver.RunFailure{Code: driver.FailureAgentError, Message: liveOracleSecret}
	case "go-error":
		return r, errors.New(liveOracleSecret)
	case "envelope-secret":
		_ = sink.EmitStream(driver.StreamPayload{Sequence: 1, Kind: driver.StreamKind(liveOracleSecret)})
	case "empty", "custom-empty", "custom-expect-empty":
		r.Output = ""
	case "whitespace":
		r.Output = " \n\t"
	case "wrong-text", "custom-expect-mismatch":
		r.Output = liveOracleSecret
	case "custom-text", "custom-expect-before", "custom-expect-after", "custom-expect-last":
		r.Output = "Application answer"
	case "custom-expect-whitespace":
		r.Output = "\nApplication answer \t"
	case "resume-missing":
		r.Checkpoint = nil
	case "resume-invalid":
		r.Checkpoint.Valid = false
	case "schema-nil":
		r.StructuredOutput = nil
	case "schema-source":
		r.StructuredOutput.Source = driver.StructuredOutputSource(liveOracleSecret)
	case "schema-invalid":
		r.StructuredOutput.Valid = false
		r.StructuredOutput.ValidationErrors = []string{liveOracleSecret}
	case "schema-empty":
		r.StructuredOutput.RawJSON = nil
	case "schema-malformed":
		r.StructuredOutput.RawJSON = []byte("{" + liveOracleSecret)
	case "schema-null":
		r.StructuredOutput.RawJSON = []byte("null")
	case "schema-array":
		r.StructuredOutput.RawJSON = []byte("[]")
	case "schema-missing":
		r.StructuredOutput.RawJSON = []byte("{}")
	case "schema-false":
		r.StructuredOutput.RawJSON = []byte(`{"ok":false}`)
	case "schema-type":
		r.StructuredOutput.RawJSON = []byte(`{"ok":"true"}`)
	case "schema-extra":
		r.StructuredOutput.RawJSON = []byte(`{"ok":true,"` + liveOracleSecret + `":1}`)
	case "schema-duplicate":
		r.StructuredOutput.RawJSON = []byte(`{"ok":false,"ok":true}`)
	case "schema-trailing":
		r.StructuredOutput.RawJSON = []byte(`{"ok":true} {"ok":true}`)
	case "schema-value-extra":
		r.StructuredOutput.Value = map[string]any{"ok": true, liveOracleSecret: 1}
	case "schema-value-unencodable":
		r.StructuredOutput.Value = make(chan string)
	case "schema-value":
		r.StructuredOutput.Value = map[string]any{"ok": false}
	case "schema-value-absent":
		r.StructuredOutput.Value = nil
	}
	if strings.HasPrefix(d.scenario, "custom-") {
		if req.Prompt != "Custom application prompt; do not replace this text." {
			return r, fmt.Errorf("custom prompt was changed")
		}
	} else if req.OutputSchema == nil && req.Prompt != DefaultLivePrompt {
		return r, fmt.Errorf("default prompt was changed")
	}
	return r, nil
}

type liveSuccessOracleResumeDriver struct{ liveSuccessOracleDriver }

func (d liveSuccessOracleResumeDriver) SessionCodec() driver.SessionCodec {
	if d.scenario == "resume-codec" {
		return nil
	}
	return NewReferenceDriver(ReferenceConfig{}).(driver.SessionCodecProvider).SessionCodec()
}
func (liveSuccessOracleResumeDriver) SessionConfigFingerprint() string { return "oracle-config" }

func TestLiveSuccessDoesNotChangeFailureSPI(t *testing.T) {
	for _, resp := range []driver.Response{{ExitCode: 1}, {Signal: "SIGTERM"}, {TimedOut: true}, {Failure: &driver.RunFailure{Code: driver.FailureAgentError}}, {}} {
		if vs := VerifyOutcome(&resp, nil); len(vs) != 0 {
			t.Fatalf("legal structural response rejected: %v", vs)
		}
	}
	if vs := VerifyOutcome(&driver.Response{}, errors.New("transport failure")); len(vs) != 0 {
		t.Fatalf("legal structural error rejected: %v", vs)
	}
}

// Diagnostic evidence must retain complete-byte integrity without printing a
// provider-controlled value, even when every available layer is sensitive.
func TestLiveSuccessDiagnostic(t *testing.T) {
	secret := []byte(liveOracleSecret)
	resp := driver.Response{
		ExitCode: 7, Signal: liveOracleSecret, TimedOut: true,
		Failure: &driver.RunFailure{Code: driver.FailureCode(liveOracleSecret), Message: liveOracleSecret},
		RawStreams: &driver.RawStreams{Stdout: liveOracleSecret, Stderr: liveOracleSecret,
			Terminal: &driver.TerminalPayload{Event: liveOracleSecret, JSON: secret}},
		Checkpoint:       &driver.Checkpoint{Valid: true, State: &driver.SessionState{ResumeID: liveOracleSecret}},
		StructuredOutput: &driver.StructuredOutput{Source: driver.StructuredOutputSource(liveOracleSecret), RawJSON: secret, Value: liveOracleSecret, ValidationErrors: []string{liveOracleSecret}},
	}
	out := liveDiagnostic(&resp, fmt.Errorf("wrapped: %w", errors.New(liveOracleSecret)))
	if strings.Contains(out, liveOracleSecret) {
		t.Fatal("safe diagnostic leaked the artificial secret canary")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("diagnostic is not structured JSON: %v", err)
	}
	for _, field := range []string{"signal", "Stdout", "Stderr", "TerminalEvent", "TerminalJSON", "StructuredJSON", "FailureCode", "FailureMessage"} {
		var got liveBytesDiagnostic
		if json.Unmarshal(fields[field], &got) != nil || got.Bytes != len(secret) || got.SHA256 != fmt.Sprintf("%x", sha256.Sum256(secret)) {
			t.Errorf("%s diagnostic lost complete byte length/hash", field)
		}
	}
	for field, expected := range map[string]string{"exit": "7", "timed_out": "true", "failure": "true", "raw_present": "true", "terminal_present": "true", "Checkpoint": "true", "ResumeID": "true", "Structured": "true", "ValuePresent": "true", "ValidationErrors": "1"} {
		if string(fields[field]) != expected {
			t.Errorf("%s = %s, want %s", field, fields[field], expected)
		}
	}
}
