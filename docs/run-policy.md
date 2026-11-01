# Run policy and approvals

`adaptor.Policy`, installed through `WithPolicy`, controls sandbox, optional search/browser features, approvals, and supplemental messages. Drivers translate supported dimensions to native controls; core owns message limits and the single approval policy. All SDK imports use `github.com/agent-dance/agent-adaptor/v2`.

## Policy value and replacement rule

| Field | Type and values | Zero value |
|---|---|---|
| Sandbox | SandboxLevel: ReadOnly, WorkspaceWrite, Unrestricted | SandboxInherit |
| WebSearch | FeatureLevel: FeatureAllow, FeatureDeny | FeatureInherit |
| Browser | FeatureLevel: FeatureAllow, FeatureDeny | FeatureInherit |
| Approvals | ApprovalPolicy | Portable defaults below |
| Messages | MessagePolicy | Enabled, default framing, 32 pending messages, 20 turns |

The sandbox-only presets are PolicyReadOnly, PolicyWorkspaceWrite, and PolicyUnrestricted. They do not select approval modes.

WithPolicy is a SharedOption for construction defaults or one execution. The nearer value replaces the **entire** policy, including Messages and Approvals:

```go
agent := adaptor.New(codex.Driver(codex.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
		Messages:  adaptor.MessagePolicy{MaxTurns: 5},
	}),
)

result, err := agent.Send(ctx, "Update the tests",
	adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite}),
).Result()
if err != nil {
	return err
}
fmt.Println(result.Text)
```

That call inherits Driver search behavior and SDK message defaults, because the call-site policy replaces the constructor value. An all-zero policy leaves non-approval provider controls unspecified, applies SDK approval defaults, and enables supplemental messages.

`WithTimeout` bounds an execution across its provider turns. For Standby, its lifetime context bounds the conversation; per-send execution options such as WithTimeout queue a separate execution instead of reconfiguring active work. Agent/Thread Send contexts govern their execution; Stream.Send contexts can withdraw unclaimed messages; Standby.Send contexts bound admission only.

## Approval policy

ApprovalPolicy aliases the Driver's HumanDecisionPolicy:

| Field | Values / default |
|---|---|
| Permission, PlanReview | ApprovalInherit, ApprovalAsk, ApprovalAutoApprove, ApprovalAutoDeny; default Ask |
| Question | QuestionInherit, QuestionAsk, QuestionAutoDeny; default auto-deny |
| Timeout | time.Duration; default 30 seconds, negative means no practical deadline |
| OnTimeout, OnReject | FallbackInherit, FallbackAbort, FallbackContinue, FallbackRetry; default abort |
| MaxRetries | Default 3; negative is invalid |

Questions have no auto-approve mode: a question needs an actual answer. ApprovalsAutoDeny explicitly selects auto-deny for all three kinds and requires all three capabilities; it is not portable across every Driver.

FallbackAbort terminates the execution with a business error. FallbackContinue forwards the rejection/timeout so the Driver can continue. FallbackRetry creates a fresh request ID and asks again within MaxRetries. If the Driver does not support retry, a NoticeLifecycle with `Data["warning"] == "human_decision_retry_unsupported"` reports degradation to abort.

If an Ask request arrives when Events has never been called and there is no OnApproval handler, core immediately applies the timeout fallback and emits a lifecycle notice. A Result-only caller does not wait for an approval timeout. Call Events early for a UI, install a callback, or select a supported automatic mode.

Standby internally drains its execution streams. When no external UI will answer, configure a handler or automatic policy explicitly; the Result-only shortcut is not an unattended Standby policy.

## Message policy

```go
policy := adaptor.Policy{
	Messages: adaptor.MessagePolicy{
		MaxPending: 32,
		MaxTurns:   20,
	},
}
agent := adaptor.New(codex.Driver(codex.Config{}), adaptor.WithPolicy(policy))
```

MessagePolicy fields:

- Disabled: omit the Driver's supplemental message port and reject Stream.Send with ErrMessageUnsupported. Starting prompts still execute.
- Format: optional `func(adaptor.MessageContext) string`; nil uses built-in framing. Context contains ID, original Text, From, and Timing.
- MaxPending: unclaimed queue capacity; zero means 32. Full queues block Send until space or cancellation, never silently drop input.
- MaxTurns: provider turns per execution; zero means 20. Negative limits are invalid.

Framing distinguishes AuthorHuman, AuthorAgent, and AuthorSystem, preserves the original objective, and asks the model to incorporate input into one final answer. Only supplemental input is framed; a starting prompt remains raw. A custom Format controls that text and must retain whatever author/authority guidance the host requires. Driver size limits apply to the framed text.

Near 80% of the turn limit, core emits `Notice{Kind: "limit.approaching"}` and, when continuation is available, adds a system wrap-up message. At the limit, it starts no more turns. Pending input fails with MessageLimitExceeded and remains in Result.Undelivered; the execution itself can succeed. MaxTurns does not bound one provider turn's internal tool/model steps; use WithTimeout and provider configuration where appropriate.

When selects Soon, Interrupt, or AfterTurn per message; it does not replace Policy. Unsupported Interrupt requests fail explicitly rather than degrading. See the [Driver message matrix](./streaming.md#12-message-delivery-capabilities).

## Capability validation

Descriptor.RunPolicyCaps is authoritative for explicit provider controls. Preserve the Driver value to inspect its declaration before construction:

```go
d := claude.Driver(claude.Config{})
caps := d.Descriptor().RunPolicyCaps
agent := adaptor.New(d)
fmt.Println(caps.Permission.Ask, caps.Question.Ask)
```

Before Driver dispatch, invalid enum/action/retry/message-limit values produce ErrInvalidPolicy and `*InvalidPolicyError{Driver, Field, Value}`. A valid unsupported sandbox/search/browser selection produces ErrPolicyCapabilityUnsupported and `*PolicyCapabilityUnsupportedError{Driver, Dimension, Value}`. An explicit unsupported approval mode produces ErrHumanDecisionModeUnsupported and `*HumanDecisionModeUnsupportedError{Driver, Kind, Mode}`.

Inherit is not an explicit capability request. A zero approval policy therefore remains usable on Drivers that never emit a given kind. Callbacks do not override these capability checks.

Current built-in declarations:

| Driver | Sandbox | Web search | Browser | Permission | Plan review | Question | Approval retry |
|---|---:|---:|---:|---|---|---|---:|
| Codex | yes | yes | no | auto-approve | none | none | no |
| Claude | no | no | yes | ask, auto-approve, auto-deny | ask, auto-approve, auto-deny | ask, auto-deny | no |
| Cursor | no | no | no | auto-approve | none | none | no |
| CodeBuddy | no | no | no | ask, auto-approve, auto-deny | ask, auto-approve, auto-deny | ask, auto-deny | no |

An unsupported SDK sandbox control does not describe the strength of an externally configured provider environment. The SDK reports only controls it can honor. Validate against the captured Driver used by the Agent.

Message capabilities are independent of this table: Codex app-server supports Interrupt; Claude, Cursor, and CodeBuddy do not. MinVersion is a Driver declaration, not an enforced Environment probe gate in this implementation.

## Callback form

OnApproval is a SharedOption. Every Ask request routed to the handler carries the live responder:

```go
agent := adaptor.New(claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		if req.Kind == adaptor.ApprovalQuestion {
			return req.Answer(ctx, "Use the existing implementation")
		}
		return req.Approve(ctx)
	}),
)
```

The handler resolves exactly once and returns nil, or returns an error to abort. A panic or nil return without a response is an agent business failure. ApproveAll approves binary requests and denies questions; DenyAll(reason) denies all kinds. Both handle requests already routed as Ask and cannot enable unsupported modes.

## Event responder form

```go
stream := agent.Send(ctx, "Review the proposed plan")
defer stream.Cancel()
for event := range stream.Events() {
	if req, ok := event.(*adaptor.ApprovalRequest); ok {
		if err := req.Deny(ctx, "The plan needs a smaller scope"); err != nil {
			stream.Cancel()
			return err
		}
	}
}
result, err := stream.Result()
if err != nil {
	return err
}
fmt.Println(result.Text)
```

A UI can retain the request and respond from another goroutine or request. Fields include ID, RunID, Kind, Title, Source, ToolCallID, Choices, Details, CreatedAt, Deadline, Attempt, and Meta. Notices broadcast approval resolution with request_id for retiring a pending card. Persisting an audit record does not persist the live responder.

Origin returns `Origin{Member, Task string}`, zero for a single Agent. Forward attributes a copy while preserving the shared exactly-once responder; answering either copy resolves both. The host handles approvals with the same methods regardless of Origin.

| Response error | Meaning |
|---|---|
| ErrApprovalResolved | Another response already won. |
| ErrApprovalExpired | Deadline or execution ended; also matches ErrApprovalResolved. |
| ErrApprovalKindMismatch | Approve on a Question or Answer on a binary kind. |
| ErrApprovalUnavailable | Nil, zero-valued, or unbound request. |

All return promptly. Approve applies to Permission/PlanReview; Answer applies to Question; Deny applies to all three. A consumer leaving the event loop should cancel and drain remaining critical events.

## Run errors

Read the same verdict through Result or terminal Done:

```go
result, err := agent.Send(ctx, prompt).Result()
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		result = runErr.Result
		fmt.Println(runErr.Reason, result.Text)
	}
	return err
}
```

RunError.Result retains available text, raw streams, transcript, usage, services, and message outcomes. ErrApprovalDenied and ErrApprovalTimeout identify aborting fallbacks. Configuration/resource/context failures remain wrapped errors unless the Driver classifies a business failure. Supplemental admission errors use MessageError; later failed message outcomes do not necessarily fail the execution.

Standby execution retries are separate from approval retries. Only classified rate-limit/network/provider-server failures with undelivered work retry once; run_failed input carries to the next execution under the same IDs and can later report delivered. See [Streaming](./streaming.md#13-standby-conversations) and [Public errors](./public-errors.md).

Structured output with an explicit Ask mode additionally needs WorksWithHITL. No built-in Driver currently advertises that combination; see [Structured output](./structured-output.md).
