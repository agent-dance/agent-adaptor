package adaptor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/mcp"
	"github.com/agent-dance/agent-adaptor/v2/profile"
)

// The Standby's internal event forwarding must not count as host subscription.
func TestStandbyUnsubscribedApprovalFallsBackImmediately(t *testing.T) {
	fake := newFakeDriver()
	asking := make(chan struct{})
	allowAsk := make(chan struct{})
	answered := make(chan driver.DecisionResponse, 1)
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		close(asking)
		select {
		case <-allowAsk:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
		answer, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "allow?"})
		answered <- answer
		return messageCheckpoint("done"), err
	}
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Timeout: time.Hour, OnTimeout: adaptor.FallbackContinue}}))
	standby, _ := openTestStandby(t, agent, "review-approval")
	// Intentionally never call standby.Events().
	if err := standby.Send(context.Background(), "work", adaptor.MessageID("approval-start")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-asking:
	case <-time.After(5 * time.Second):
		t.Fatal("Driver never requested approval")
	}
	// Send takes the Standby mutex, fencing its current.Events() call. Reuse
	// the initial ID so this synchronization does not admit another message.
	if err := standby.Send(context.Background(), "work", adaptor.MessageID("approval-start")); err != nil {
		t.Fatal(err)
	}
	close(allowAsk)
	select {
	case answer := <-answered:
		if answer.Result != driver.DecisionTimedOut {
			t.Fatalf("fallback = %s, want timed_out", answer.Result)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("approval still waiting although Standby.Events was never called; immediate timeout fallback was bypassed")
	}
}

func TestStandbyInvalidPolicyRejectedBeforeAdmission(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	standby, _ := openTestStandby(t, adaptor.New(fake), "invalid-policy")
	events := standby.Events()
	err := standby.Send(context.Background(), "rejected original", adaptor.MessageID("invalid"), adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxPending: -1}}))
	var message *adaptor.MessageError
	if !errors.Is(err, adaptor.ErrInvalidPolicy) || !errors.As(err, &message) || message.ID != "invalid" || message.Text != "rejected original" || message.Reason != adaptor.MessageProviderRejected {
		t.Fatalf("invalid policy admission = %v", err)
	}
	// Rejection never enters the message state machine (§5.2), so there is
	// no execution or carry. Reusing the ID with valid options must work.
	select {
	case event := <-events:
		t.Fatalf("rejected input published an event: %+v", event)
	default:
	}
	if err := standby.Send(context.Background(), "valid replacement", adaptor.MessageID("invalid")); err != nil {
		t.Fatal(err)
	}
	call := standbyReceive(t, calls)
	if call.request.Prompt != "valid replacement" {
		t.Fatalf("rejected input was carried: %q", call.request.Prompt)
	}
	call.reply <- standbyReply{response: messageCheckpoint("valid")}
	if _, done := standbyDone(t, events); done.Err != nil {
		t.Fatal(done.Err)
	}
}

func TestStandbyInvalidSchemaRejectedBeforeAdmission(t *testing.T) {
	for _, schema := range []string{"{", `{"type":42}`} {
		t.Run(schema, func(t *testing.T) {
			fake, calls := controlledStandbyDriver()
			standby, _ := openTestStandby(t, adaptor.New(fake), "invalid-schema")
			events := standby.Events()
			err := standby.Send(context.Background(), "invalid schema input", adaptor.MessageID("schema"), adaptor.WithSchemaJSON([]byte(schema)))
			var rejected *adaptor.MessageError
			if !errors.Is(err, adaptor.ErrInvalidOutputSchema) || !errors.As(err, &rejected) || rejected.ID != "schema" || rejected.Reason != adaptor.MessageProviderRejected {
				t.Fatalf("invalid schema admission=%v", err)
			}
			select {
			case event := <-events:
				t.Fatalf("rejected schema input published %+v", event)
			default:
			}
			if err := standby.Send(context.Background(), "valid input", adaptor.MessageID("schema")); err != nil {
				t.Fatal(err)
			}
			call := standbyReceive(t, calls)
			if call.request.Prompt != "valid input" {
				t.Fatalf("invalid schema input carried under new options: %q", call.request.Prompt)
			}
			call.reply <- standbyReply{response: messageCheckpoint("valid")}
			if _, done := standbyDone(t, events); done.Err != nil {
				t.Fatal(done.Err)
			}
		})
	}
}

func TestStandbyInvalidMCPDoesNotCarryWithDifferentOptions(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	descriptor := fake.Descriptor()
	descriptor.MCP = driver.MCPCapability{Supported: true, Stdio: true}
	fake.descriptor = &descriptor
	standby, _ := openTestStandby(t, adaptor.New(fake), "invalid-mcp")
	events := standby.Events()
	if err := standby.Send(context.Background(), "input with invalid MCP", adaptor.MessageID("invalid"), adaptor.WithMCP(mcp.Stdio("missing-command", ""))); err != nil {
		t.Fatal(err)
	}
	observed, done := standbyDone(t, events)
	if !errors.Is(done.Err, adaptor.ErrInvalidMCPConfig) {
		t.Fatalf("MCP error=%v", done.Err)
	}
	settled := false
	for _, event := range observed {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "invalid" && update.State == adaptor.MessageFailed {
			if update.Reason != adaptor.MessageProviderRejected || update.Text != "input with invalid MCP" {
				t.Fatalf("configuration failure eligible for replay: %+v", update)
			}
			settled = true
		}
	}
	if !settled {
		t.Fatal("accepted MCP input omitted terminal receipt")
	}
	if err := standby.Send(context.Background(), "valid next input", adaptor.MessageID("valid")); err != nil {
		t.Fatal(err)
	}
	call := standbyReceive(t, calls)
	if call.request.Prompt != "valid next input" {
		t.Fatalf("invalid MCP input carried under new options: %q", call.request.Prompt)
	}
	call.reply <- standbyReply{response: messageCheckpoint("valid")}
	if _, done := standbyDone(t, events); done.Err != nil {
		t.Fatal(done.Err)
	}
}

func TestStandbyInvalidProfileReturnsInputBeforeDone(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		resources profile.Resources
	}{
		{name: "instructions_path_and_content", resources: profile.Resources{Instructions: &profile.Instructions{Path: "unused", Content: "extra"}}},
		{name: "agent_key_missing", resources: profile.Resources{Agents: []profile.SubAgent{{Instructions: "worker"}}}},
		{name: "hook_key_missing", resources: profile.Resources{Hooks: []profile.Hook{{}}}},
		{name: "config_target_missing", resources: profile.Resources{Config: []profile.ConfigPatch{{Key: "patch"}}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fake, calls := controlledStandbyDriver()
			standby, _ := openTestStandby(t, adaptor.New(fake), "invalid-profile")
			events := standby.Events()
			if err := standby.Send(context.Background(), "input with invalid profile", adaptor.MessageID("invalid"), adaptor.WithProfileResources(scenario.resources)); err != nil {
				t.Fatal(err)
			}
			observed, done := standbyDone(t, events)
			if done.Err == nil {
				t.Fatal("invalid profile preparation succeeded")
			}
			returned := false
			for _, event := range observed {
				if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "invalid" && update.State == adaptor.MessageFailed {
					if update.Reason != adaptor.MessageProviderRejected || update.Text != "input with invalid profile" {
						t.Fatalf("profile preparation failure eligible for replay: %+v", update)
					}
					returned = true
				}
			}
			if !returned {
				t.Fatal("profile preparation failure omitted the admitted input")
			}
			if err := standby.Send(context.Background(), "valid next input", adaptor.MessageID("valid")); err != nil {
				t.Fatal(err)
			}
			call := standbyReceive(t, calls)
			if call.request.Prompt != "valid next input" {
				t.Fatalf("invalid profile input silently replayed: %q", call.request.Prompt)
			}
			call.reply <- standbyReply{response: messageCheckpoint("valid")}
			if _, done := standbyDone(t, events); done.Err != nil {
				t.Fatal(done.Err)
			}
		})
	}
}

func TestStandbyPreflightFailurePublishesMessageOutcomeBeforeDone(t *testing.T) {
	inner, calls := controlledStandbyDriver()
	cause := errors.New("invalid captured configuration")
	fake := &invalidConfigDriver{fakeDriver: inner, cause: cause}
	standby, _ := openTestStandby(t, adaptor.New(fake), "preflight-receipts")
	events := standby.Events()
	if err := standby.Send(context.Background(), "accepted original", adaptor.MessageID("initial")); err != nil {
		t.Fatal(err)
	}
	observed, done := standbyDone(t, events)
	if !errors.Is(done.Err, adaptor.ErrInvalidDriverConfig) || !errors.Is(done.Err, cause) {
		t.Fatalf("Done.Err = %v", done.Err)
	}
	failed := false
	for _, event := range observed {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.ID == "initial" && update.State == adaptor.MessageFailed && update.Text == "accepted original" && update.Reason == adaptor.MessageProviderRejected {
			failed = true
		}
	}
	if !failed {
		t.Fatal("accepted Standby input has no final MessageUpdate before Done on preflight failure")
	}
	fake.cause = nil
	if err := standby.Send(context.Background(), "next valid input", adaptor.MessageID("next")); err != nil {
		t.Fatal(err)
	}
	call := standbyReceive(t, calls)
	if call.request.Prompt != "next valid input" {
		t.Fatalf("invalid preflight input silently retried: %q", call.request.Prompt)
	}
	call.reply <- standbyReply{response: messageCheckpoint("next")}
	if _, done := standbyDone(t, events); done.Err != nil {
		t.Fatal(done.Err)
	}
}

func TestStandbyFullCarryAcceptsFreshWakeupInput(t *testing.T) {
	fake, calls := controlledStandbyDriver()
	agent := adaptor.New(fake, adaptor.WithPolicy(adaptor.Policy{Messages: adaptor.MessagePolicy{MaxPending: 1}}))
	standby, _ := openTestStandby(t, agent, "review-full-carry")
	events := standby.Events()
	if err := standby.Send(context.Background(), "old input", adaptor.MessageID("old")); err != nil {
		t.Fatal(err)
	}
	call := standbyReceive(t, calls)
	call.reply <- standbyReply{response: driver.Response{Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "ordinary non-retryable failure"}}}
	if _, done := standbyDone(t, events); done.Err == nil {
		t.Fatal("expected first execution failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := standby.Send(ctx, "new input should wake carried work", adaptor.MessageID("new")); err != nil {
		t.Fatalf("idle Standby cannot accept a fresh wakeup when carry fills MaxPending: %v", err)
	}
	next := standbyReceive(t, calls)
	oldIndex, newIndex := strings.Index(next.request.Prompt, "old input"), strings.Index(next.request.Prompt, "new input should wake carried work")
	if oldIndex < 0 || newIndex <= oldIndex {
		t.Fatalf("carry must precede fresh input: %q", next.request.Prompt)
	}
	next.reply <- standbyReply{response: messageCheckpoint("recovered")}
	observed, done := standbyDone(t, events)
	if done.Err != nil {
		t.Fatal(done.Err)
	}
	var delivered []string
	for _, event := range observed {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.State == adaptor.MessageDelivered {
			delivered = append(delivered, update.ID)
		}
	}
	if len(delivered) != 2 || len(done.Result.Undelivered()) != 0 {
		t.Fatalf("fresh/carry delivery receipts=%v result=%+v", delivered, done.Result)
	}
	outcomes := done.Result.Messages()
	if len(outcomes) != 2 || outcomes[0].ID != "old" || outcomes[1].ID != "new" {
		t.Fatalf("outcomes must preserve admission order: %+v", outcomes)
	}
}

func TestStandbyPreflightFailureSettlesCarriedInputsBeforeDone(t *testing.T) {
	inner, calls := controlledStandbyDriver()
	fake := &invalidConfigDriver{fakeDriver: inner}
	standby, _ := openTestStandby(t, adaptor.New(fake), "carried-preflight")
	events := standby.Events()
	if err := standby.Send(context.Background(), "old input", adaptor.MessageID("old")); err != nil {
		t.Fatal(err)
	}
	first := standbyReceive(t, calls)
	first.reply <- standbyReply{err: errors.New("non-retryable process failure")}
	if _, done := standbyDone(t, events); done.Err == nil {
		t.Fatal("initial failure hidden")
	}
	fake.cause = errors.New("configuration rejected on wakeup")
	if err := standby.Send(context.Background(), "fresh input", adaptor.MessageID("fresh")); err != nil {
		t.Fatal(err)
	}
	observed, done := standbyDone(t, events)
	if !errors.Is(done.Err, adaptor.ErrInvalidDriverConfig) {
		t.Fatalf("preflight failure=%v", done.Err)
	}
	var failed []adaptor.MessageUpdate
	for _, event := range observed {
		if update, ok := event.(adaptor.MessageUpdate); ok && update.State == adaptor.MessageFailed {
			failed = append(failed, update)
		}
	}
	if len(failed) != 2 || failed[0].ID != "old" || failed[0].Text != "old input" || failed[1].ID != "fresh" || failed[1].Text != "fresh input" {
		t.Fatalf("preflight omitted or reordered accepted inputs: %+v", failed)
	}
	for _, update := range failed {
		if update.Reason != adaptor.MessageProviderRejected {
			t.Fatalf("invalid configuration left input eligible for carry: %+v", update)
		}
	}
}

func TestStandbyCancellationPreservesReliableToolResult(t *testing.T) {
	fake := newFakeDriver()
	entered := make(chan struct{})
	fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
		// Publish the reliable result before cancellation. The outer Standby
		// broker is full with lifecycle events until the host starts reading.
		if err := sink.EmitStream(driver.StreamPayload{Kind: driver.StreamToolCallResult, ToolCallID: "review-tool", Result: map[string]any{"text": "tool finished"}}); err != nil {
			return driver.Response{}, err
		}
		close(entered)
		<-ctx.Done()
		return driver.Response{}, ctx.Err()
	}
	standby, _ := openTestStandby(t, adaptor.New(fake, adaptor.WithBlockingEvents(), adaptor.WithEventBuffer(1)), "review-cancel-tool")
	events := standby.Events()
	if err := standby.Send(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Driver did not start")
	}
	standby.Cancel()
	observed, _ := standbyDone(t, events)
	for _, event := range observed {
		if result, ok := event.(adaptor.ToolResult); ok && result.ID == "review-tool" {
			return
		}
	}
	t.Fatal("Standby silently discarded reliable ToolResult after Cancel although the inner Stream retained it")
}

func TestStandbySubscribedApprovalCanBeAnswered(t *testing.T) {
	for _, subscribeBeforeSend := range []bool{false, true} {
		t.Run(map[bool]string{false: "during_execution", true: "before_execution"}[subscribeBeforeSend], func(t *testing.T) {
			fake := newFakeDriver()
			entered, ask := make(chan struct{}), make(chan struct{})
			fake.runFunc = func(ctx context.Context, _ driver.Request, sink driver.EventSink) (driver.Response, error) {
				close(entered)
				select {
				case <-ask:
				case <-ctx.Done():
					return driver.Response{}, ctx.Err()
				}
				response, err := sink.(driver.DecisionCapableSink).RequestDecision(ctx, driver.DecisionRequest{Kind: driver.HumanDecisionPermission, Prompt: "allow?"})
				if err == nil && response.Result != driver.DecisionApproved {
					t.Errorf("subscribed approval response=%+v", response)
				}
				return messageCheckpoint("approved"), err
			}
			standby, _ := openTestStandby(t, adaptor.New(fake), "subscribed-approval")
			var events <-chan adaptor.Event
			if subscribeBeforeSend {
				events = standby.Events()
			}
			if err := standby.Send(context.Background(), "work"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Driver did not start")
			}
			if !subscribeBeforeSend {
				events = standby.Events()
			}
			close(ask)
			answered := false
			for {
				event := standbyReceive(t, events)
				if request, ok := event.(*adaptor.ApprovalRequest); ok {
					if err := request.Approve(context.Background()); err != nil {
						t.Fatal(err)
					}
					answered = true
				}
				if done, ok := event.(adaptor.Done); ok {
					if done.Err != nil || !answered {
						t.Fatalf("answered=%v Done=%+v", answered, done)
					}
					break
				}
			}
		})
	}
}
