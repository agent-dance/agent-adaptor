package adaptor_test

import (
	"context"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

type invalidConfigDriver struct {
	*fakeDriver
	cause error
}

func (d *invalidConfigDriver) ValidateConfig(any) error { return d.cause }

func runPath(t *testing.T, agent *adaptor.Agent, path string, opts ...adaptor.CallOption) error {
	t.Helper()
	switch path {
	case "run":
		_, err := agent.Run(context.Background(), "hello", opts...)
		return err
	case "stream":
		stream := agent.Stream(context.Background(), "hello", opts...)
		for range stream.Events() {
		}
		_, err := stream.Result()
		return err
	case "thread_run":
		_, err := agent.Thread("customer-thread").Run(context.Background(), "hello", opts...)
		return err
	case "thread_stream":
		stream := agent.Thread("customer-thread").Stream(context.Background(), "hello", opts...)
		for range stream.Events() {
		}
		_, err := stream.Result()
		return err
	default:
		t.Fatalf("unknown invocation path %q", path)
		return nil
	}
}

func TestInvalidDriverConfigIsStableAcrossUnifiedInvocationPaths(t *testing.T) {
	if adaptor.ErrInvalidDriverConfig != driver.ErrInvalidDriverConfig {
		t.Fatal("root and driver ErrInvalidDriverConfig must have one identity")
	}
	for _, path := range []string{"run", "stream", "thread_run", "thread_stream"} {
		t.Run(path, func(t *testing.T) {
			cause := errors.New("captured config is malformed")
			fake := &invalidConfigDriver{fakeDriver: newFakeDriver(), cause: cause}
			fake.descriptor = &driver.Descriptor{Type: "invalid-config"}
			agent := adaptor.New(fake)

			err := runPath(t, agent, path)
			if !errors.Is(err, adaptor.ErrInvalidDriverConfig) {
				t.Fatalf("error = %v, want ErrInvalidDriverConfig", err)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("error = %v, want original validation cause", err)
			}
			var typed *adaptor.InvalidDriverConfigError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %T %v, want *InvalidDriverConfigError", err, err)
			}
			if typed.Driver != "invalid-config" || typed.Cause != cause {
				t.Fatalf("typed error = %#v", typed)
			}
			if got := fake.runCount(); got != 0 {
				t.Fatalf("Driver.Run called %d times after config rejection", got)
			}
		})
	}
}

func TestExplicitApprovalModeCapabilityCheckedAcrossUnifiedInvocationPaths(t *testing.T) {
	for _, path := range []string{"run", "stream", "thread_run", "thread_stream"} {
		t.Run(path, func(t *testing.T) {
			fake := newFakeDriver()
			fake.descriptor = &driver.Descriptor{Type: "no-hitl"}
			agent := adaptor.New(fake)

			err := runPath(t, agent, path, adaptor.WithPolicy(adaptor.Policy{
				Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk},
			}))
			if !errors.Is(err, adaptor.ErrHumanDecisionModeUnsupported) {
				t.Fatalf("error = %v, want ErrHumanDecisionModeUnsupported", err)
			}
			var typed *adaptor.HumanDecisionModeUnsupportedError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %T %v, want *HumanDecisionModeUnsupportedError", err, err)
			}
			if typed.Driver != "no-hitl" || typed.Kind != driver.HumanDecisionPermission || typed.Mode != string(driver.HumanDecisionAsk) {
				t.Fatalf("typed error = %#v", typed)
			}
			if got := fake.runCount(); got != 0 {
				t.Fatalf("Driver.Run called %d times after capability rejection", got)
			}
		})
	}
}

func TestApprovalCapabilityMatrixChecksEveryExplicitMode(t *testing.T) {
	tests := []struct {
		name   string
		policy adaptor.ApprovalPolicy
		kind   driver.HumanDecisionKind
		mode   string
	}{
		{name: "permission_ask", policy: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}, kind: driver.HumanDecisionPermission, mode: "ask"},
		{name: "permission_auto_approve", policy: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}, kind: driver.HumanDecisionPermission, mode: "auto_approve"},
		{name: "permission_auto_deny", policy: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoDeny}, kind: driver.HumanDecisionPermission, mode: "auto_reject"},
		{name: "plan_review_ask", policy: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk}, kind: driver.HumanDecisionPlanReview, mode: "ask"},
		{name: "plan_review_auto_approve", policy: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAutoApprove}, kind: driver.HumanDecisionPlanReview, mode: "auto_approve"},
		{name: "plan_review_auto_deny", policy: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAutoDeny}, kind: driver.HumanDecisionPlanReview, mode: "auto_reject"},
		{name: "question_ask", policy: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk}, kind: driver.HumanDecisionQuestion, mode: "ask"},
		{name: "question_auto_deny", policy: adaptor.ApprovalPolicy{Question: adaptor.QuestionAutoDeny}, kind: driver.HumanDecisionQuestion, mode: "auto_reject"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.descriptor = &driver.Descriptor{Type: "none"}
			agent := adaptor.New(fake)
			_, err := agent.Run(context.Background(), "hello", adaptor.WithPolicy(adaptor.Policy{Approvals: test.policy}))

			var typed *adaptor.HumanDecisionModeUnsupportedError
			if !errors.Is(err, adaptor.ErrHumanDecisionModeUnsupported) || !errors.As(err, &typed) {
				t.Fatalf("error = %T %v", err, err)
			}
			if typed.Kind != test.kind || typed.Mode != test.mode {
				t.Fatalf("typed error = %#v, want kind=%s mode=%s", typed, test.kind, test.mode)
			}
			if got := fake.runCount(); got != 0 {
				t.Fatalf("Driver.Run called %d times", got)
			}
		})
	}
}

func TestExplicitPolicyDimensionsRequireDriverCapabilities(t *testing.T) {
	if adaptor.ErrPolicyCapabilityUnsupported != driver.ErrPolicyCapabilityUnsupported {
		t.Fatal("root and driver ErrPolicyCapabilityUnsupported must have one identity")
	}
	tests := []struct {
		name      string
		policy    adaptor.Policy
		dimension string
		value     string
	}{
		{name: "sandbox", policy: adaptor.Policy{Sandbox: adaptor.ReadOnly}, dimension: "sandbox", value: "read_only"},
		{name: "web_search", policy: adaptor.Policy{WebSearch: adaptor.FeatureAllow}, dimension: "web_search", value: "allow"},
		{name: "browser", policy: adaptor.Policy{Browser: adaptor.FeatureDeny}, dimension: "browser", value: "deny"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeDriver()
			fake.descriptor = &driver.Descriptor{Type: "minimal"}
			agent := adaptor.New(fake)

			_, err := agent.Run(context.Background(), "hello", adaptor.WithPolicy(test.policy))
			if !errors.Is(err, adaptor.ErrPolicyCapabilityUnsupported) {
				t.Fatalf("error = %v, want ErrPolicyCapabilityUnsupported", err)
			}
			var typed *adaptor.PolicyCapabilityUnsupportedError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %T %v, want *PolicyCapabilityUnsupportedError", err, err)
			}
			if typed.Driver != "minimal" || typed.Dimension != test.dimension || typed.Value != test.value {
				t.Fatalf("typed error = %#v", typed)
			}
			if got := fake.runCount(); got != 0 {
				t.Fatalf("Driver.Run called %d times", got)
			}
		})
	}
}

func TestZeroPolicyDoesNotRequireAdvertisedCapabilities(t *testing.T) {
	fake := newFakeDriver()
	fake.descriptor = &driver.Descriptor{Type: "no-hitl"}
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{}))

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("zero policy rejected: %v", err)
	}
	if result == nil || result.Text != "ok" {
		t.Fatalf("result = %#v", result)
	}
	if got := fake.runCount(); got != 1 {
		t.Fatalf("Driver.Run calls = %d, want 1", got)
	}
}

func TestAgentDefaultExplicitApprovalModeIsCapabilityChecked(t *testing.T) {
	fake := newFakeDriver()
	fake.descriptor = &driver.Descriptor{Type: "no-hitl"}
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk},
	}))

	_, err := agent.Run(context.Background(), "hello")
	var typed *adaptor.HumanDecisionModeUnsupportedError
	if !errors.Is(err, adaptor.ErrHumanDecisionModeUnsupported) || !errors.As(err, &typed) {
		t.Fatalf("error = %T %v", err, err)
	}
	if typed.Kind != driver.HumanDecisionPlanReview || typed.Mode != string(driver.HumanDecisionAsk) {
		t.Fatalf("typed error = %#v", typed)
	}
	if got := fake.runCount(); got != 0 {
		t.Fatalf("Driver.Run called %d times", got)
	}
}

func TestInvalidPolicyValuesHaveStableTypedError(t *testing.T) {
	tests := []struct {
		name   string
		policy adaptor.Policy
		field  string
	}{
		{name: "sandbox", policy: adaptor.Policy{Sandbox: adaptor.SandboxLevel("invalid")}, field: "Policy.Sandbox"},
		{name: "web_search", policy: adaptor.Policy{WebSearch: adaptor.FeatureLevel("invalid")}, field: "Policy.WebSearch"},
		{name: "browser", policy: adaptor.Policy{Browser: adaptor.FeatureLevel("invalid")}, field: "Policy.Browser"},
		{name: "permission", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalMode("invalid")}}, field: "Policy.Approvals.Permission"},
		{name: "plan_review", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalMode("invalid")}}, field: "Policy.Approvals.PlanReview"},
		{name: "question", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionMode("invalid")}}, field: "Policy.Approvals.Question"},
		{name: "on_timeout", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnTimeout: adaptor.FallbackAction("invalid")}}, field: "Policy.Approvals.OnTimeout"},
		{name: "on_reject", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{OnReject: adaptor.FallbackAction("invalid")}}, field: "Policy.Approvals.OnReject"},
		{name: "max_retries", policy: adaptor.Policy{Approvals: adaptor.ApprovalPolicy{MaxRetries: -1}}, field: "Policy.Approvals.MaxRetries"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeDriver()
			agent := adaptor.New(fake)
			_, err := agent.Run(context.Background(), "hello", adaptor.WithPolicy(test.policy))

			if !errors.Is(err, adaptor.ErrInvalidPolicy) {
				t.Fatalf("error = %v, want ErrInvalidPolicy", err)
			}
			var typed *adaptor.InvalidPolicyError
			if !errors.As(err, &typed) {
				t.Fatalf("error = %T %v, want *InvalidPolicyError", err, err)
			}
			if typed.Driver != "fake" || typed.Field != test.field {
				t.Fatalf("typed error = %#v", typed)
			}
			if got := fake.runCount(); got != 0 {
				t.Fatalf("Driver.Run called %d times", got)
			}
		})
	}
}
