package adaptertest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentObservationCapabilityParentOracle(t *testing.T) {
	for _, tc := range []struct {
		name, scope, parentScope, parentID string
		invalid                            bool
	}{
		{"same_scope_self_parent", "s", "s", "call", true},
		{"root_scope_self_parent", "", "", "call", true},
		{"cross_scope_same_id", "s", "parent", "call", false},
		{"same_scope_other_id", "s", "s", "parent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := alignmentObservationPayloads()
			for _, i := range []int{1, 4} {
				p[i].Capability.ScopeID = tc.scope
				p[i].Capability.ParentScopeID = tc.parentScope
				p[i].Capability.ParentToolCallID = tc.parentID
			}
			violations := VerifyStreamSequence(p)
			if !tc.invalid {
				if len(violations) != 0 {
					t.Fatalf("valid scoped parent rejected: %v", violations)
				}
				return
			}
			for _, v := range violations {
				if v.Clause == "OBS-03" {
					return
				}
			}
			t.Fatalf("self-parent accepted: want OBS-03, got %v", violations)
		})
	}
}

func TestAlignmentObservationEnvelopeOracle(t *testing.T) {
	for _, field := range []string{"Sequence", "Seq", "Timestamp", "valid"} {
		t.Run(field, func(t *testing.T) {
			p := alignmentObservationPayloads()
			if err := alignmentSetEnvelope(&p[1], field); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name       string
				violations []Violation
			}{
				{"rich", VerifyStreamSequence(p)},
				{"facts_only", verifyObservationSequence([]driver.StreamPayload{p[1], p[4]})},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if field == "valid" {
						if len(tc.violations) != 0 {
							t.Fatal(tc.violations)
						}
						return
					}
					if len(tc.violations) != 1 || tc.violations[0].Clause != "EVT-10" {
						t.Fatalf("want exactly one EVT-10, got %v", tc.violations)
					}
				})
			}
		})
	}
}

// The child runs the real suite entry points with an in-memory Driver. A
// subprocess is necessary because those entry points report through testing.T.
// No provider executable, credential, or live opt-in is involved.
func TestAlignmentObservationSuiteEntrypointOracle(t *testing.T) {
	const selector = "AGENT_ADAPTOR_T23_OBSERVATION_ORACLE"
	if value := os.Getenv(selector); value != "" {
		entry, field, ok := strings.Cut(value, "/")
		if !ok {
			t.Fatal("invalid fixture selector")
		}
		d := alignmentObservationOnlyDriver{field: field, streaming: entry == "nonrich"}
		c := &suiteConfig{livePrompt: "in-memory fixture", liveTimeout: time.Second, liveStructured: true}
		switch entry {
		case "nonrich":
			checkLiveRun(t, d, c)
		case "native_batch":
			checkLiveStructuredOutput(t, d, d.Descriptor(), c)
		default:
			t.Fatal("unknown fixture entry")
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"nonrich", "native_batch"} {
		for _, field := range []string{"Sequence", "Seq", "Timestamp", "valid"} {
			t.Run(entry+"/"+field, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, self, "-test.run=^TestAlignmentObservationSuiteEntrypointOracle$", "-test.v", "-test.timeout=15s")
				cmd.Env = append(os.Environ(), selector+"="+entry+"/"+field)
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("fixture timed out: %v\n%s", ctx.Err(), output)
				}
				if strings.Contains(string(output), "SKIP") {
					t.Fatalf("suite entry did not execute: %s", output)
				}
				if field == "valid" {
					if err != nil || !strings.Contains(string(output), "--- PASS:") {
						t.Fatalf("valid facts-only entry failed: %v\n%s", err, output)
					}
					return
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(string(output), "EVT-10:") {
					t.Fatalf("want executed entry to fail with EVT-10, got %v\n%s", err, output)
				}
			})
		}
	}
}

// Deliberately has no StreamSupport: observation facts alone do not imply a
// rich text/tool protocol or require run.started/run.finished frames.
type alignmentObservationOnlyDriver struct {
	field     string
	streaming bool
}

func (d alignmentObservationOnlyDriver) Descriptor() driver.Descriptor {
	desc := driver.Descriptor{StructuredOutput: driver.StructuredOutputCapability{JSONSchemaNative: true, WorksWithRun: true}}
	desc.Observation.Streaming.MCP = true
	desc.Observation.Batch.MCP = true
	return desc
}
func (d alignmentObservationOnlyDriver) ValidateConfig(any) error { return nil }
func (d alignmentObservationOnlyDriver) Run(_ context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if req.Streaming != d.streaming {
		return driver.Response{}, fmt.Errorf("fixture got Streaming=%v, want %v", req.Streaming, d.streaming)
	}
	p := alignmentObservationPayloads()
	if err := alignmentSetEnvelope(&p[1], d.field); err != nil {
		return driver.Response{}, err
	}
	for _, i := range []int{1, 4} {
		if err := sink.EmitStream(p[i]); err != nil {
			return driver.Response{}, err
		}
	}
	return driver.Response{
		Output: `{"ok":true}`,
		StructuredOutput: &driver.StructuredOutput{
			Source: driver.StructuredOutputSourceNative, Valid: true, RawJSON: []byte(`{"ok":true}`),
		},
	}, nil
}

func alignmentSetEnvelope(p *driver.StreamPayload, field string) error {
	switch field {
	case "Sequence":
		p.Sequence = 7
	case "Seq":
		p.Seq = 7
	case "Timestamp":
		p.Timestamp = time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	case "valid":
	default:
		return fmt.Errorf("unknown envelope fixture %q", field)
	}
	return nil
}
