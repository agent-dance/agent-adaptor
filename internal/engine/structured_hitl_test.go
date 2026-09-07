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

func alignmentOrdinaryAskCaps() RunPolicyCapabilities {
	return RunPolicyCapabilities{Permission: driver.HumanDecisionSupport{Ask: true}, PlanReview: driver.HumanDecisionSupport{Ask: true}, Question: driver.QuestionSupport{Ask: true}}
}

// All 7,776 combinations cover unset defaults as well as explicit Ask,
// AutoApprove and AutoReject. Expected bitsets independently distinguish a
// nil mechanism's legacy request from a precise mechanism's runtime demand.
func TestAlignmentStructuredHITLExhaustiveMatrix(t *testing.T) {
	binaryModes := []driver.HumanDecisionMode{driver.HumanDecisionUnset, driver.HumanDecisionAsk, driver.HumanDecisionAutoApprove, driver.HumanDecisionAutoReject}
	questionModes := []driver.QuestionMode{driver.QuestionUnset, driver.QuestionAsk, driver.QuestionAutoReject}
	for pi, permission := range binaryModes {
		for li, plan := range binaryModes {
			for qi, question := range questionModes {
				t.Run(fmt.Sprintf("permission_%d_plan_%d_question_%d", pi, li, qi), func(t *testing.T) {
					policy := RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: permission, PlanReview: plan, Question: question}}
					explicitAsk, effectiveAsk := 0, 0
					if pi == 1 {
						explicitAsk |= 1
					}
					if pi < 2 {
						effectiveAsk |= 1
					}
					if li == 1 {
						explicitAsk |= 2
					}
					if li < 2 {
						effectiveAsk |= 2
					}
					if qi == 1 {
						explicitAsk |= 4
						effectiveAsk |= 4
					}
					for native := -1; native < 8; native++ {
						for prompt := -1; prompt < 8; prompt++ {
							for _, legacy := range []bool{false, true} {
								caps := driver.StructuredOutputCapability{JSONSchemaNative: true, JSONSchemaPromptValidate: true, WorksWithRun: true, WorksWithStreaming: true, WorksWithHITL: legacy, NativeHITL: alignmentHITLMatrix(native), PromptValidateHITL: alignmentHITLMatrix(prompt)}
								desc := Descriptor{Type: "matrix-fixture", StructuredOutput: caps, RunPolicyCaps: alignmentOrdinaryAskCaps()}
								before := caps
								before.NativeHITL = alignmentHITLMatrix(native)
								before.PromptValidateHITL = alignmentHITLMatrix(prompt)
								eligible := func(mask int) bool {
									if mask == -1 {
										return explicitAsk == 0 || legacy
									}
									return effectiveAsk&mask == effectiveAsk
								}
								var want StructuredOutputSource
								if eligible(native) {
									want = StructuredOutputSourceNative
								} else if eligible(prompt) {
									want = StructuredOutputSourcePromptValidate
								}
								got, err := resolveStructuredOutputSource(desc, &OutputSchema{}, true, policy)
								if got != want || (err != nil) != (want == "") {
									t.Fatalf("native=%d prompt=%d legacy=%t: source=%q err=%v want=%q", native, prompt, legacy, got, err, want)
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
			got, err := resolveStructuredOutputSource(Descriptor{Type: "gate-fixture", StructuredOutput: caps, RunPolicyCaps: alignmentOrdinaryAskCaps()}, schema, tc.streaming, alignmentAskPolicy(7))
			if got != tc.want || (err != nil) != (tc.want == "" && !tc.noSchema) {
				t.Fatalf("source=%q err=%v, want %q", got, err, tc.want)
			}
		})
	}
}
