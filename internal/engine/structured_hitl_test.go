package engine

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func alignmentAskPolicy(mask int) RunPolicy {
	var policy RunPolicy
	if mask&1 != 0 {
		policy.HumanDecision.Permission = HumanDecisionAsk
	}
	if mask&2 != 0 {
		policy.HumanDecision.PlanReview = HumanDecisionAsk
	}
	if mask&4 != 0 {
		policy.HumanDecision.Question = QuestionAsk
	}
	return policy
}

func alignmentHITLMatrix(mask int) *driver.StructuredOutputHITLCapability {
	if mask < 0 {
		return nil
	}
	return &driver.StructuredOutputHITLCapability{Permission: mask&1 != 0, PlanReview: mask&2 != 0, Question: mask&4 != 0}
}

// All 1,296 combinations verify mechanism-specific overrides, nil legacy
// behavior, explicit false, mixed Ask, and native priority. Bitset expectations
// are independent of the resolver's per-kind implementation.
func TestAlignmentStructuredHITLExhaustiveMatrix(t *testing.T) {
	for ask := 0; ask < 8; ask++ {
		t.Run(fmt.Sprintf("ask_%03b", ask), func(t *testing.T) {
			for native := -1; native < 8; native++ {
				for prompt := -1; prompt < 8; prompt++ {
					for _, legacy := range []bool{false, true} {
						caps := driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: legacy, NativeHITL: alignmentHITLMatrix(native), PromptValidateHITL: alignmentHITLMatrix(prompt)}
						desc := Descriptor{Type: "matrix-fixture", StructuredOutput: caps}
						before := caps
						before.NativeHITL = alignmentHITLMatrix(native)
						before.PromptValidateHITL = alignmentHITLMatrix(prompt)
						eligible := func(mask int) bool {
							if mask == -1 {
								if legacy {
									mask = 7
								} else {
									mask = 0
								}
							}
							return ask&mask == ask
						}
						var want StructuredOutputSource
						if eligible(native) {
							want = StructuredOutputSourceNative
						} else if eligible(prompt) {
							want = StructuredOutputSourcePromptValidate
						}
						got, err := resolveStructuredOutputSource(desc, &OutputSchema{}, true, alignmentAskPolicy(ask))
						if got != want || (err != nil) != (want == "") {
							t.Fatalf("native=%d prompt=%d legacy=%t: source=%q err=%v; want %q", native, prompt, legacy, got, err, want)
						}
						if err != nil {
							var typed *driver.StructuredOutputUnsupportedError
							if !errors.Is(err, driver.ErrStructuredOutputUnsupported) || !errors.As(err, &typed) || typed.Driver != desc.Type || typed.Reason == "" {
								t.Fatalf("unstable error: %v", err)
							}
						}
						if !reflect.DeepEqual(desc.StructuredOutput, before) {
							t.Fatal("resolver mutated descriptor")
						}
					}
				}
			}
		})
	}
}

func TestAlignmentStructuredHITLIndependentGates(t *testing.T) {
	full := driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, NativeHITL: alignmentHITLMatrix(7), PromptValidateHITL: alignmentHITLMatrix(7)}
	tests := []struct {
		name      string
		change    func(*driver.StructuredOutputCapability)
		streaming bool
		noSchema  bool
		want      StructuredOutputSource
	}{
		{name: "native_flag", change: func(c *driver.StructuredOutputCapability) { c.JSONSchemaNative = false }, want: StructuredOutputSourcePromptValidate},
		{name: "no_mechanism", change: func(c *driver.StructuredOutputCapability) {
			c.JSONSchemaNative = false
			c.JSONSchemaPromptValidate = false
		}},
		{name: "no_run", change: func(c *driver.StructuredOutputCapability) { c.WorksWithRun = false }},
		{name: "no_streaming", streaming: true, change: func(c *driver.StructuredOutputCapability) { c.WorksWithStreaming = false }},
		{name: "batch", change: func(c *driver.StructuredOutputCapability) { c.WorksWithStreaming = false }, want: StructuredOutputSourceNative},
		{name: "schema_absent", noSchema: true, change: func(c *driver.StructuredOutputCapability) { *c = driver.StructuredOutputCapability{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			caps := full
			tc.change(&caps)
			schema := &OutputSchema{}
			if tc.noSchema {
				schema = nil
			}
			got, err := resolveStructuredOutputSource(Descriptor{Type: "gate-fixture", StructuredOutput: caps}, schema, tc.streaming, alignmentAskPolicy(7))
			if got != tc.want || (err != nil) != (tc.want == "" && !tc.noSchema) {
				t.Fatalf("source=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
