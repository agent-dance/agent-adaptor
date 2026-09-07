package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Decode the frozen declaration so this regression fixture can also execute
// against the pre-matrix baseline, where the unknown fields are ignored.
func alignmentHITLCaps(t *testing.T) driver.StructuredOutputCapability {
	t.Helper()
	var caps driver.StructuredOutputCapability
	err := json.Unmarshal([]byte(`{"JSONSchemaNative":true,"JSONSchemaPromptValidate":true,"WorksWithRun":true,"WorksWithStreaming":true,"WorksWithHITL":false,"NativeHITL":{"PlanReview":true,"Question":true},"PromptValidateHITL":{"Permission":true,"PlanReview":true,"Question":true}}`), &caps)
	if err != nil {
		t.Fatal(err)
	}
	return caps
}

func alignmentHITLDriver(caps driver.StructuredOutputCapability) *fakeDriver {
	d := newFakeDriver()
	d.descriptor = &driver.Descriptor{Type: "alignment-hitl", RunPolicyCaps: d.caps, StructuredOutput: caps}
	d.streamCaps = driver.StreamCapability{Native: true, HITL: true}
	d.runFunc = func(_ context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		res := driver.Response{
			Output: `{"project_name":"alignment"}`, Summary: "aligned", Model: "fake-model", Provider: "fake-provider",
			Usage:           &driver.Usage{InputTokens: 3, OutputTokens: 4},
			RawStreams:      &driver.RawStreams{Stdout: "fixture stdout", Stderr: "fixture stderr", Terminal: &driver.TerminalPayload{Event: "fixture.result", JSON: json.RawMessage(`{"success":true}`)}},
			Transcript:      []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: `{"project_name":"alignment"}`}},
			RuntimeServices: []driver.RuntimeServiceReport{{ID: "observed-service", Status: driver.RuntimeServiceRunning}},
		}
		if req.StructuredOutputSource == driver.StructuredOutputSourceNative {
			res.StructuredOutput = &driver.StructuredOutput{RawJSON: []byte(res.Output)}
		}
		return res, nil
	}
	return d
}

func TestAlignmentStructuredHITLPermissionPromptFallback(t *testing.T) {
	d := alignmentHITLDriver(alignmentHITLCaps(t))
	_, err := adaptor.New(d).Run(context.Background(), "extract", adaptor.WithSchema[projectMetadata](), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
	if err != nil {
		t.Fatalf("supported Permission Ask must fall back to prompt validation: %v", err)
	}
	req := d.lastRequest(t)
	if req.StructuredOutputSource != driver.StructuredOutputSourcePromptValidate || !req.Streaming {
		t.Fatalf("resolved request = %+v", req)
	}
}

func TestAlignmentStructuredHITLRejectsBeforeResources(t *testing.T) {
	for _, path := range []string{"run", "stream"} {
		t.Run(path, func(t *testing.T) {
			caps := fullStructuredCaps()
			caps.WorksWithHITL = false
			d := alignmentHITLDriver(caps)
			log := &callLog{}
			agent := adaptor.New(d,
				adaptor.WithWorkspaceManager(&fakeWorkspaceManager{log: log, lease: adaptor.WorkspaceLease{CWD: t.TempDir()}}),
				adaptor.WithServiceManager(&fakeServiceManager{log: log}),
				adaptor.WithServices(adaptor.ServiceSpec{ID: "runtime"}),
				adaptor.WithRunServices(&fakeProvider{name: "attachment", log: log}),
			)
			err := runPath(t, agent, path, adaptor.WithSchema[projectMetadata](), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
			if !errors.Is(err, driver.ErrStructuredOutputUnsupported) {
				t.Fatalf("error = %v", err)
			}
			if got := log.snapshot(); len(got) != 0 {
				t.Errorf("resources acquired after unsupported schema/Ask: %v", got)
			}
			if d.runCount() != 0 {
				t.Errorf("Driver.Run called %d times", d.runCount())
			}
		})
	}
}

func TestAlignmentStructuredHITLRunStreamEquivalence(t *testing.T) {
	tests := []struct {
		name      string
		approvals adaptor.ApprovalPolicy
		want      driver.StructuredOutputSource
	}{
		{"permission", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, driver.StructuredOutputSourcePromptValidate},
		{"plan", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk}, driver.StructuredOutputSourceNative},
		{"question", adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}, driver.StructuredOutputSourceNative},
		{"mixed_native", adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk}, driver.StructuredOutputSourceNative},
		{"mixed_prompt", adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk, PlanReview: adaptor.ApprovalAsk, Question: adaptor.QuestionAsk}, driver.StructuredOutputSourcePromptValidate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := alignmentHITLDriver(alignmentHITLCaps(t))
			agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Approvals: tc.approvals}))
			run, err := agent.Run(context.Background(), "extract", adaptor.WithSchema[projectMetadata]())
			if err != nil {
				t.Fatal(err)
			}
			stream := agent.Stream(context.Background(), "extract", adaptor.WithSchema[projectMetadata]())
			for range stream.Events() {
			}
			streamed, err := stream.Result()
			if err != nil {
				t.Fatal(err)
			}
			var runValue, streamValue projectMetadata
			if err = run.Decode(&runValue); err != nil {
				t.Fatal(err)
			}
			if err = streamed.Decode(&streamValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runValue, streamValue) || runValue.ProjectName != "alignment" || !reflect.DeepEqual(run.Raw(), streamed.Raw()) || !reflect.DeepEqual(run.Transcript(), streamed.Transcript()) || !reflect.DeepEqual(run.Services(), streamed.Services()) || run.Text != streamed.Text || run.Summary != streamed.Summary || run.Model != streamed.Model || run.Provider != streamed.Provider || !reflect.DeepEqual(run.Usage, streamed.Usage) {
				t.Fatalf("Run/Stream outputs differ: %+v / %+v", run, streamed)
			}
			first, second := d.request(t, 0), d.request(t, 1)
			if first.StructuredOutputSource != tc.want || !first.Streaming || second.StructuredOutputSource != first.StructuredOutputSource || second.Streaming != first.Streaming || second.Prompt != first.Prompt || !reflect.DeepEqual(first.OutputSchema, second.OutputSchema) || !reflect.DeepEqual(first.Policy, second.Policy) {
				t.Fatalf("resolved invocation differs: %+v / %+v", first, second)
			}
			if strings.Contains(first.Prompt, "Return only a single JSON value") != (tc.want == driver.StructuredOutputSourcePromptValidate) {
				t.Fatalf("wrong schema prompt injection: %q", first.Prompt)
			}
		})
	}
}

func TestAlignmentStructuredHITLSchemaAbsentAndPolicyGates(t *testing.T) {
	for _, path := range []string{"run", "stream"} {
		t.Run(path+"/schema_absent", func(t *testing.T) {
			d := alignmentHITLDriver(driver.StructuredOutputCapability{})
			if err := runPath(t, adaptor.New(d), path, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}})); err != nil {
				t.Fatal(err)
			}
			req := d.lastRequest(t)
			if req.OutputSchema != nil || req.StructuredOutputSource != "" || !req.Streaming {
				t.Fatalf("ordinary Permission Ask changed: %+v", req)
			}
		})
		for _, kind := range []string{"permission", "plan", "question"} {
			t.Run(path+"/unsupported_policy_"+kind, func(t *testing.T) {
				d := alignmentHITLDriver(alignmentHITLCaps(t))
				var approvals adaptor.ApprovalPolicy
				switch kind {
				case "permission":
					d.descriptor.RunPolicyCaps.Permission.Ask = false
					approvals.Permission = adaptor.ApprovalAsk
				case "plan":
					d.descriptor.RunPolicyCaps.PlanReview.Ask = false
					approvals.PlanReview = adaptor.ApprovalAsk
				case "question":
					d.descriptor.RunPolicyCaps.Question.Ask = false
					approvals.Question = adaptor.QuestionAsk
				}
				log := &callLog{}
				agent := adaptor.New(d, adaptor.WithRunServices(&fakeProvider{name: "attachment", log: log}))
				err := runPath(t, agent, path, adaptor.WithSchema[projectMetadata](), adaptor.WithPolicy(adaptor.Policy{Approvals: approvals}))
				if !errors.Is(err, driver.ErrHumanDecisionModeUnsupported) || d.runCount() != 0 || len(log.snapshot()) != 0 {
					t.Fatalf("schema must not grant unsupported Ask: %v, calls=%d, resources=%v", err, d.runCount(), log.snapshot())
				}
			})
		}
	}
}

func TestAlignmentStructuredHITLTransportCannotDropAsk(t *testing.T) {
	for _, path := range []string{"run", "stream"} {
		for _, kind := range []string{"none", "permission", "plan", "question"} {
			t.Run(path+"/"+kind, func(t *testing.T) {
				caps := alignmentHITLCaps(t)
				caps.WorksWithStreaming = false
				d := alignmentHITLDriver(caps)
				var approvals adaptor.ApprovalPolicy
				switch kind {
				case "permission":
					approvals.Permission = adaptor.ApprovalAsk
				case "plan":
					approvals.PlanReview = adaptor.ApprovalAsk
				case "question":
					approvals.Question = adaptor.QuestionAsk
				}
				log := &callLog{}
				agent := adaptor.New(d, adaptor.WithRunServices(&fakeProvider{name: "attachment", log: log}))
				err := runPath(t, agent, path, adaptor.WithSchema[projectMetadata](), adaptor.WithPolicy(adaptor.Policy{Approvals: approvals}))
				if kind == "none" {
					if err != nil {
						t.Fatal(err)
					}
					if req := d.lastRequest(t); req.Streaming || req.StructuredOutputSource != driver.StructuredOutputSourceNative {
						t.Fatalf("batch fallback unavailable: %+v", req)
					}
				} else if !errors.Is(err, driver.ErrStructuredOutputUnsupported) || d.runCount() != 0 || len(log.snapshot()) != 0 {
					t.Fatalf("schema discarded Ask transport: %v calls=%d resources=%v", err, d.runCount(), log.snapshot())
				}
			})
		}
	}
}

func TestAlignmentStructuredHITLCallPolicyReplacesDefaults(t *testing.T) {
	d := alignmentHITLDriver(alignmentHITLCaps(t))
	agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}))
	for i, tc := range []struct {
		opts []adaptor.CallOption
		want driver.StructuredOutputSource
	}{
		{nil, driver.StructuredOutputSourcePromptValidate},
		{[]adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}})}, driver.StructuredOutputSourceNative},
		{nil, driver.StructuredOutputSourcePromptValidate},
		{[]adaptor.CallOption{adaptor.WithPolicy(adaptor.Policy{})}, driver.StructuredOutputSourceNative},
	} {
		if _, err := agent.Run(context.Background(), "extract", append([]adaptor.CallOption{adaptor.WithSchema[projectMetadata]()}, tc.opts...)...); err != nil {
			t.Fatal(err)
		}
		if got := d.request(t, i).StructuredOutputSource; got != tc.want {
			t.Fatalf("call %d cached another policy/source: got %q want %q", i, got, tc.want)
		}
	}
}

func TestAlignmentStructuredHITLInvalidSchemaBeforeResources(t *testing.T) {
	for _, path := range []string{"run", "stream"} {
		t.Run(path, func(t *testing.T) {
			d := alignmentHITLDriver(alignmentHITLCaps(t))
			log := &callLog{}
			agent := adaptor.New(d, adaptor.WithWorkspaceManager(&fakeWorkspaceManager{log: log, lease: adaptor.WorkspaceLease{CWD: t.TempDir()}}), adaptor.WithRunServices(&fakeProvider{name: "attachment", log: log}))
			err := runPath(t, agent, path, adaptor.WithSchemaJSON([]byte(`{`)), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}}))
			if !errors.Is(err, adaptor.ErrInvalidOutputSchema) || len(log.snapshot()) != 0 || d.runCount() != 0 {
				t.Fatalf("invalid schema reached resources/driver: err=%v log=%v calls=%d", err, log.snapshot(), d.runCount())
			}
		})
	}
}

type alignmentTransportProbe struct {
	*fakeDriver
	calls atomic.Int32
}

func (d *alignmentTransportProbe) StreamCapability() driver.StreamCapability {
	d.calls.Add(1)
	return d.fakeDriver.StreamCapability()
}

func TestAlignmentStructuredHITLNegotiatesOnceBeforeResources(t *testing.T) {
	d := &alignmentTransportProbe{fakeDriver: alignmentHITLDriver(alignmentHITLCaps(t))}
	log := &callLog{}
	manager := &fakeServiceManager{log: log, ensure: func(context.Context, adaptor.ServiceRequest) ([]adaptor.ServiceRef, error) {
		if got := d.calls.Load(); got != 1 {
			t.Errorf("transport probes before resources = %d, want 1", got)
		}
		return nil, nil
	}}
	agent := adaptor.New(d, adaptor.WithServiceManager(manager), adaptor.WithServices(adaptor.ServiceSpec{ID: "runtime"}))
	if _, err := agent.Run(context.Background(), "extract", adaptor.WithSchema[projectMetadata](), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}})); err != nil {
		t.Fatal(err)
	}
	if got := d.calls.Load(); got != 1 {
		t.Fatalf("request assembly renegotiated transport: %d probes", got)
	}
	if len(log.snapshot()) == 0 {
		t.Fatal("resource fixture was not exercised")
	}
}
