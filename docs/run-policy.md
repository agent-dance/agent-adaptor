# Run policy and approvals

`adaptor.Policy` is the public execution-guardrail value used by
`adaptor.WithPolicy`. It controls the provider sandbox, optional features,
and the single human-in-the-loop approval model. Drivers translate this value
to their native controls; it is never a list of CLI flags.

## Policy value and replacement rule

```go
type Policy struct {
	Sandbox   SandboxLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Approvals ApprovalPolicy
}
```

| Dimension | Values | Zero value |
|---|---|---|
| `Sandbox` | `ReadOnly`, `WorkspaceWrite`, `Unrestricted` | `SandboxInherit` |
| `WebSearch` | `FeatureAllow`, `FeatureDeny` | `FeatureInherit` |
| `Browser` | `FeatureAllow`, `FeatureDeny` | `FeatureInherit` |
| `Approvals` | per-kind modes and timeout/fallback settings | portable defaults described below |

The sandbox-only presets are `PolicyReadOnly`, `PolicyWorkspaceWrite`, and
`PolicyUnrestricted`.

`WithPolicy` is a `SharedOption`: it can establish the Agent default or
override one invocation. The nearer policy replaces the entire farther
policy; fields are not merged independently.

```go
agent := adaptor.New(d,
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
	}),
)

// This call has WorkspaceWrite and inherited WebSearch, because the whole
// call-site value replaces the Agent default.
result, err := agent.Run(ctx, prompt,
	adaptor.WithPolicy(adaptor.Policy{Sandbox: adaptor.WorkspaceWrite}),
)
```

An all-zero `Policy` delegates the non-approval dimensions to the Driver and
uses the SDK approval defaults.

## Approval policy

`ApprovalPolicy` contains three routing modes and the common fallback rules:

```go
type ApprovalPolicy struct {
	Permission ApprovalMode
	PlanReview ApprovalMode
	Question   QuestionMode
	Timeout    time.Duration
	OnTimeout  FallbackAction
	OnReject   FallbackAction
	MaxRetries int
}
```

Permission and plan review accept:

- `ApprovalInherit`
- `ApprovalAsk`
- `ApprovalAutoApprove`
- `ApprovalAutoDeny`

Questions accept `QuestionInherit`, `QuestionAsk`, or `QuestionAutoDeny`.
There is no question auto-approve mode because a question requires an actual
answer.

Zero-valued fields materialize to these portable defaults:

| Field | Default |
|---|---|
| `Permission` | `ApprovalAsk` |
| `PlanReview` | `ApprovalAsk` |
| `Question` | `QuestionAutoDeny` |
| `Timeout` | 30 seconds |
| `OnTimeout` | `FallbackAbort` |
| `OnReject` | `FallbackAbort` |
| `MaxRetries` | 3 |

A negative timeout means no approval deadline. A negative `MaxRetries` is
invalid. `ApprovalsAutoDeny` is a strict capability-dependent preset, not a
portable unattended default: it explicitly selects auto-deny for all three
kinds and therefore requires the bound Driver to advertise all three modes.
For a more portable unattended policy, explicitly select auto-approve for
permission and plan review where the Driver advertises it, and leave questions
at `QuestionInherit`; the SDK default for questions then materializes to
auto-deny.

`FallbackAbort` ends the run with a business failure. `FallbackContinue`
forwards the reject/timeout outcome so the Driver can continue. `FallbackRetry`
renews the request ID and asks again up to `MaxRetries`. If the Driver does not
advertise retry for that kind, the SDK emits one lifecycle `Notice` with
`Data["warning"] == "human_decision_retry_unsupported"` and safely degrades to
abort.

## Capability validation

The public `driver.Descriptor.RunPolicyCaps` is the source of truth. Keep the
Driver value if the host wants to disable unsupported controls before Agent
construction:

```go
d := claude.Driver(claude.Config{Model: "claude-sonnet-4"})
caps := d.Descriptor().RunPolicyCaps
agent := adaptor.New(d)

if !caps.Question.Ask {
	// Do not offer a QuestionAsk control for this Driver.
}
```

Before acquiring run resources or invoking the Driver, the SDK rejects:

- out-of-domain sandbox, feature, approval-mode, fallback-action, or retry
  values with `adaptor.ErrInvalidPolicy` and
  `*adaptor.InvalidPolicyError` (`Driver`, `Field`, `Value`);
- a valid, explicitly selected sandbox, web-search, or browser value which the
  Driver does not model with `adaptor.ErrPolicyCapabilityUnsupported` and
  `*adaptor.PolicyCapabilityUnsupportedError` (`Driver`, `Dimension`,
  `Value`);
- an explicitly selected approval mode absent from the capability matrix with
  `adaptor.ErrHumanDecisionModeUnsupported` and
  `*adaptor.HumanDecisionModeUnsupportedError` (`Driver`, `Kind`, `Mode`).

Unset approval modes are not treated as explicit capability requests. This
keeps a zero policy usable for a Driver which never emits that request kind.
The same explicit-value rule applies to non-approval dimensions: inherit
values are portable, while selected values require the corresponding
`Isolation`, `WebSearch`, or `Browser` capability. Hosts can inspect those
booleans before running to keep unsupported choices out of their UI.

Current built-in approval declarations are:

| Driver | Permission | Plan review | Question | Retry |
|---|---|---|---|---:|
| Codex | auto-approve | none | none | no |
| Claude | auto-approve, auto-deny | ask, auto-approve, auto-deny | ask, auto-deny | no |
| Cursor | auto-approve | none | none | no |
| CodeBuddy | ask, auto-approve, auto-deny | ask, auto-approve, auto-deny | ask, auto-deny | no |

These are descriptor declarations, not promises that every future provider
version has the same matrix. Validate against the Driver value used to build
the Agent.

## Callback form

`OnApproval` is a `SharedOption`. It installs an Agent default handler or a
nearer per-invocation override. Every `Ask` request invokes the handler with a
live `*ApprovalRequest`.

```go
agent := adaptor.New(d,
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk,
		},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "proceed")
		default:
			return req.Approve(ctx)
		}
	}),
)
```

The callback must call exactly one of `Approve`, `Deny`, or `Answer` and then
return nil. Returning an error aborts the invocation with that infrastructure
error. A panic or a nil return without resolving the request is classified as
an agent business failure. `ApproveAll()` and `DenyAll(reason)` provide common
handlers; `ApproveAll` denies questions because it cannot synthesize an answer.

## Event responder form

Without an `OnApproval` handler, an `Ask` request is a reliable event on the
same Stream as all other typed events:

```go
stream := agent.Stream(ctx, prompt,
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{PlanReview: adaptor.ApprovalAsk},
	}),
)

for event := range stream.Events() {
	switch req := event.(type) {
	case *adaptor.ApprovalRequest:
		if err := req.Approve(ctx); err != nil {
			stream.Cancel()
			return err
		}
	}
}
result, err := stream.Result()
```

The event includes `ID`, `RunID`, `Kind`, `Title`, `Source`, `ToolCallID`,
`Choices`, `Details`, `CreatedAt`, `Deadline`, `Attempt`, and the standard
`Event.Meta()` envelope. Approval events are not eligible for drop-mode loss.
Consumers must keep draining the stream; a consumer that abandons it must call
`Cancel`.

The responder is exactly-once and shared by copies of the request:

| Response error | Meaning |
|---|---|
| `ErrApprovalResolved` | A response already won. |
| `ErrApprovalExpired` | The deadline or owning invocation ended; it also matches `ErrApprovalResolved`. |
| `ErrApprovalKindMismatch` | `Approve` was used for a question, or `Answer` for a binary request. |
| `ErrApprovalUnavailable` | The request is nil, zero-valued, or detached from its run-owned responder. |

These methods always return promptly for invalid or expired requests; they do
not send to a nil channel.

For `Agent.Run`, no external consumer sees drained events. An invocation that
can ask must therefore install `OnApproval`, use an auto mode, or intentionally
accept the timeout fallback. Interactive hosts normally use `Stream`.

## Run errors

Approval denial and timeout are business failures on the single Go error path:

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		switch {
		case errors.Is(err, adaptor.ErrApprovalDenied):
			result = runErr.Result
		case errors.Is(err, adaptor.ErrApprovalTimeout):
			result = runErr.Result
		}
	}
	return err
}
```

`RunError.Result` retains the available text, raw streams, transcript, usage,
and service reports. Handler errors, process/protocol failures, and context
cancellation remain ordinary wrapped infrastructure errors unless the Driver
classifies them as a business failure. See [Public errors](./public-errors.md)
for the complete matching matrix.

Structured output combined with an explicit `Ask` mode also requires the
Driver's structured-output `WorksWithHITL` capability. The current built-in
Drivers do not advertise that combination; see
[Structured output](./structured-output.md).
