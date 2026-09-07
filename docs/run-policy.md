# Run policy and approvals

`adaptor.Policy` is the public execution-guardrail value used by
`adaptor.WithPolicy`. It controls the provider sandbox, optional features,
and the single human-in-the-loop approval model. Drivers translate this value
to their native controls; it is never a list of CLI flags.

## Policy value and replacement rule

```go
type Policy struct {
	ActiveExecutionTimeout time.Duration
	Sandbox   SandboxLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Approvals ApprovalPolicy
}
```

| Dimension | Values | Zero value |
|---|---|---|
| `ActiveExecutionTimeout` | nonnegative `time.Duration` | unlimited active time |
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

An all-zero `Policy` delegates sandbox and optional features to the Driver and
uses the SDK approval defaults. Its active budget is unlimited.

## Active execution budget

`Policy.ActiveExecutionTimeout` is zero for unlimited active time, positive for
a limit (including sub-millisecond values), and negative for an `ErrInvalidPolicy`
preflight error. It is core-owned and does not require a provider capability.
`WithPolicy` still replaces the whole value; a zero call-site Policy disables an
Agent's budget. `WithTimeout`, parent deadlines and approval deadlines continue
to measure wall-clock time.

```go
agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{
    ActiveExecutionTimeout: 2 * time.Minute,
}))
result, err := agent.Run(ctx, prompt, adaptor.WithTimeout(10*time.Minute))
```

Timing starts before resource preparation and includes workspace/services,
Thread acquisition, safe resume fallback, Driver execution, observers, schema
validation and final health checks. A healthy candidate settles and permanently
seals its budget immediately before the sole atomic Thread persistence call;
stateless execution seals at the corresponding health check. The balance is
checked even if an expired timer callback has not run. Exhaustion prevents
persistence and preserves the preceding healthy checkpoint.

Sealing establishes only budget health. Finalize must still succeed using the
original cancellable context. Finalize work and delayed return, followed by
cleanup, do not spend active time. A late timer cannot change a sealed outcome;
parent cancellation, Finalize errors and cleanup failures remain observable.
An already committed healthy checkpoint is not rolled back after later
cancellation or failure. Error cleanup stops timing without inventing a new
timeout.

Each Ask attempt pauses its own run before approval/notice queueing or
`OnApproval`, including waiting on a full event queue. Overlapping Ask requests
have independent tokens: timing resumes only after the last ends. A retry ends
the previous token and starts a new one; time between attempts counts. Automatic
decisions do not pause. Approval deadlines, parent cancellation, `Agent.Close`
and lease renewal remain active. A member's Ask does not pause its leader.

After Driver entry, exhaustion returns `nil, *RunError` with primary
`ReasonActiveExecutionTimeout`, available partial Result and
`ErrActiveExecutionTimeout`. `errors.As` exposes `*ActiveExecutionTimeoutError`
and its exact local `Limit`. Before Driver entry, the wrapped error retains the
same identity without inventing a Result. The first confirmed terminal reason
is authoritative, including a budget cause selected before its asynchronous
cancellation notification. An inherited active-looking parent cause stays
inspectable evidence; it does not mean this run exhausted its own budget.
Inspect `RunError.Reason` before matching secondary context causes. Active exhaustion, approval timeout, parent deadline and explicit
cancellation are distinct outcomes.

## Delegation active execution budget

The optional hosttool owns DelegationRequest.ActiveExecutionTimeout and
DelegationPolicy.MaxActiveExecutionTimeout, separately from Timeout/MaxTimeout.
Zero means no active bound; negative values fail before BeforeDelegate or I/O.
Use the smaller positive request/maximum, or the only positive value. A 100ms
request and 200ms maximum therefore allow 100ms. Existing wall-clock defaults
and clamping are unchanged.

Each Delegate call, including a continuation with the same TaskID, starts a
fresh private budget. BeforeDelegate, discovery, retries, streaming, polling and
recovery share it. FinishExecution seals the first selected outcome before
cleanup. AfterDelegate and best-effort CancelTask each receive an independent
five-second context. A known input TaskID remains cancellable even if no new I/O
started; cleanup failures add a safe secondary diagnostic.

A Member's Ask pauses only the Member's budget. The delegator keeps spending its
own active time, and does not inject WithPolicy into the Member. Parent limits,
Member limits, wall-clock Timeout and approval deadlines remain independent.

DelegationError.Code selects the primary outcome; Cause and Unwrap preserve the
local original error graph and RunError's partial Result. Local Send and
SendStream consume one Runner.Stream each, with the same terminal classification
rules. Remote failures use the closed wire controls in the
[A2A failure contract](./a2a.md#stable-failure-classification). Only a trustworthy
positive primary typed limit is projected, rounded up to milliseconds; unrelated
parent limits cannot replace it. A missing limit is unknown, not zero.

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

Approval descriptions own their Choices and nested JSON Details. Live copies
share one exactly-once responder; copying metadata does not create a second
answer right. Historical recorder descriptions remove that responder.

Optional capability recording uses the existing observer boundary: its first
error/panic/timeout emits one safe notice and stops that observer for the run.
It changes neither Result, HITL nor checkpoint policy. Custom Stores must honor
callback cancellation before committing; the SDK cannot undo ignored-context
side effects. See [scoped recording](./api-reference.md#121-capability-recording).

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
| Claude | ask, auto-approve, auto-deny | ask, auto-approve, auto-deny | ask, auto-deny | no |
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
return nil. Returning an error aborts the invocation with a `RunError` retaining
that infrastructure cause and the available Result. A panic or a nil return without resolving the request is classified as
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
and service reports. After Driver entry, handler, process/protocol, context,
store and cleanup errors use the same carrier and preserve `Cause`. A primary
approval failure can also match a secondary `context.Canceled`; use `Reason`
for outcome mapping. Before Driver entry errors remain ordinary wrapped errors.
See [Public errors](./public-errors.md) for the complete matching matrix.

Structured output checks HITL independently for native and prompt validation.
A non-nil `NativeHITL` or `PromptValidateHITL` matrix checks all effective Ask
kinds, including inherited Permission/PlanReview Ask, and their ordinary policy
capability. A nil matrix preserves the published explicit-Ask `WorksWithHITL`
rule. Mixed Ask requirements must all fit one mechanism; automatic fallback
cannot discard them. Without a schema these matrices have no effect. Built-in
declarations remain listed in [Structured output](./structured-output.md).


Claude native schema permits Question and PlanReview Ask; Permission Ask uses
prompt validation. Inherited Permission Ask therefore makes a zero-policy
schema call use prompt validation. Raw zero-policy transport remains observational;
that schema decision does not activate interaction or authorize a permission.
Explicit QuestionAsk with unset Permission uses bidirectional transport and can
handle a real inherited Permission request. Native-compatible explicit policy
and temporary Thread process behavior are detailed in the structured-output
reference. A custom DecisionCapableSink error aborts and drains the Claude writer
even if the caller context remains live; it does not create a separate policy.

Observation demand cannot override approval feasibility. Core first validates
transport/source candidates using captured Driver configuration, then OR-merges
attachment demand and scores only those candidates. Skill/MCP/Subagent support
each contribute one point for capability demand, and Todo one point; ties keep
the initial transport. Zero demand preserves the initial choice. An explicit Ask
without schema is also preserved, including a third-party Driver whose initially
selected batch transport legitimately supports it. Only required facts absent
from every feasible candidate produce an observation_unavailable Notice.
