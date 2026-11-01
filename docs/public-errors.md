# Public errors

This reference covers the stable error identities owned by the root
`adaptor` package and the public leaf packages that define Driver, Tool, skill, MCP,
Thread storage, A2A client, and hosttool contracts.

Use `errors.Is` for a category and `errors.As` when the table names a typed
error. Error strings are diagnostics, not matching contracts.

All SDK imports use `github.com/agent-dance/agent-adaptor/v2`.

## One execution error path

`Stream.Result()` and terminal `Done` use one verdict model:

- success returns `*Result, nil`;
- a completed business failure returns `*RunError`, whose `Result` retains
  the available audit data;
- configuration, context, process, protocol, store, and resource failures are
  ordinary wrapped errors.

```go
result, err := runner.Send(ctx, prompt).Result()
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		result = runErr.Result
		log.Printf("business failure %s: %s", runErr.Reason, runErr.Message)
		return err
	}
	return err
}
_ = result
```

`Runner.Send` returns its Stream immediately; setup and execution failures use
`Stream.Result()` and the final `Done{Result, Err}` event. Result can be read directly without subscribing; its pair is identical to Done, including pointer identity. A business failure has a nil direct Result and a non-nil RunError.Result. Programmer-contract
violations documented as panics, such as constructing an Agent with a nil
Driver or creating a Thread with an empty key, are not error sentinels.

## Agent lifecycle

`ErrAgentClosed` means `Agent.Close` has started. New `Send` calls on
the Agent or any Thread derived from it fail with this sentinel and do not
restart the Driver's process pool. `Close` itself is idempotent; its context
error reports a bounded cleanup failure rather than changing this sentinel.

## Root business failures

Every row is a `*adaptor.RunError`. `errors.As` exposes `Reason`, `Message`,
`Details`, and the non-nil `Result`; `errors.Is` selects one category.

| Sentinel | `FailureReason` | Meaning |
|---|---|---|
| `ErrApprovalDenied` | `ReasonApprovalDenied` | A host or auto policy denied an approval and the fallback aborted. |
| `ErrApprovalTimeout` | `ReasonApprovalTimeout` | An approval deadline elapsed and the fallback aborted. |
| `ErrAgentFailed` | `ReasonAgentError` | The Driver classified an agent-level failure, such as a bad terminal protocol or non-zero exit. |
| `ErrRunCancelled` | `ReasonCancelled` | The Driver returned a classified cancellation business failure. |
| `ErrPolicyViolation` | `ReasonPolicyViolation` | A completed invocation violated a run policy, including default fail-on-invalid structured output. |

`context.Canceled` and `context.DeadlineExceeded` are infrastructure errors;
they do not imply `ErrRunCancelled`. Likewise, malformed host policy values
match `ErrInvalidPolicy`, not `ErrPolicyViolation`.

Unknown Driver failure codes remain available as `RunError.Reason` but do not
silently match one of the five sentinels above.

## Root pre-invocation errors

These failures occur before `Driver.Run` is invoked. The root variables are
the exact same identities as their owner-package variables.

| Root sentinel | Canonical leaf identity | Typed error for `errors.As` | Meaning |
|---|---|---|---|
| `ErrSkillNotFound` | `skill.ErrSkillNotFound` | — | A requested catalogue key was not resolved. |
| `ErrSkillKeyConflict` | `skill.ErrSkillKeyConflict` | `*adaptor.SkillKeyConflictError` (`Key`, `Sources`, `Detail`) | Structurally different skill declarations use the same key. |
| `ErrSkillMaterializationFailed` | `skill.ErrSkillMaterializationFailed` | `*adaptor.SkillMaterializationError` (`Key`, `RuntimeName`, `Cause`) | A resolved skill could not be staged for the Driver. |
| `ErrSkillSourceMissing` | `skill.ErrSkillSourceMissing` | — | A concrete skill has no source. |
| `ErrSkillKeyMissing` | `skill.ErrSkillKeyMissing` | — | A concrete skill has an empty key. |
| `ErrInvalidMCPConfig` | `mcp.ErrInvalidConfig` | — | An MCP declaration has a missing/duplicate key, missing command/URL, unknown transport, or transport-field mismatch. |
| `ErrMCPUnsupported` | `mcp.ErrUnsupported` | — | The Driver declares no MCP support. |
| `ErrMCPTransportUnsupported` | `mcp.ErrTransportUnsupported` | — | The Driver does not support the requested MCP transport. |
| `ErrInvalidOutputSchema` | `driver.ErrInvalidOutputSchema` | `*adaptor.InvalidOutputSchemaError` (`Reason`, `Cause`) | A schema cannot be derived, parsed, normalized, or compiled. |
| `ErrStructuredOutputUnsupported` | `driver.ErrStructuredOutputUnsupported` | `*adaptor.StructuredOutputUnsupportedError` (`Driver`, `Reason`) | Neither native enforcement nor Prompt plus local validation can honor the request. |
| `ErrInvalidDriverConfig` | `driver.ErrInvalidDriverConfig` | `*adaptor.InvalidDriverConfigError` (`Driver`, `Cause`) | `Driver.ValidateConfig` rejected the captured configuration. |
| `ErrInvalidPolicy` | `driver.ErrInvalidPolicy` | `*adaptor.InvalidPolicyError` (`Driver`, `Field`, `Value`) | A policy enum, action, retry value, or message limit is out of domain. |
| `ErrPolicyCapabilityUnsupported` | `driver.ErrPolicyCapabilityUnsupported` | `*adaptor.PolicyCapabilityUnsupportedError` (`Driver`, `Dimension`, `Value`) | A valid, explicitly selected sandbox, web-search, or browser value is unsupported. |
| `ErrHumanDecisionModeUnsupported` | `driver.ErrHumanDecisionModeUnsupported` | `*adaptor.HumanDecisionModeUnsupportedError` (`Driver`, `Kind`, `Mode`) | An explicit approval mode is absent from `Descriptor.RunPolicyCaps`. |

Typed errors preserve lower-level causes where their contracts say so.
`InvalidDriverConfigError` and `InvalidOutputSchemaError` join their sentinel
with `Cause`. `SkillMaterializationError` matches its SDK sentinel through an
`Is` method while unwrapping the materializer cause.

```go
var invalid *adaptor.InvalidPolicyError
if errors.As(err, &invalid) {
	log.Printf("driver=%s field=%s value=%s", invalid.Driver, invalid.Field, invalid.Value)
}

if errors.Is(err, adaptor.ErrInvalidPolicy) {
	// Stable category without typed detail.
}
```

## Message admission and delivery errors

Stream.Send and Standby.Send return nil on admission, not on delivery. Rejected input is a `*adaptor.MessageError` with ID, original Text, Reason, and wrapped Err. Use errors.As to retain the text for rerouting; use errors.Is for the cause.

| Cause/category | Meaning |
|---|---|
| ErrRunFinished | The Stream has sealed; send on a new execution or a live Standby instead. |
| ErrMessageUnsupported | Messages are disabled, the Driver cannot continue, or requested Interrupt is unsupported. |
| context.Canceled / context.DeadlineExceeded | Admission was cancelled or its context ended while waiting for queue space. |
| MessageLimitExceeded reason | Framed text exceeds the Driver's size limit; no separate size sentinel exists. |
| MessageProviderRejected reason | Invalid author/timing or unsupported admission; inspect the wrapped cause. |
| ErrAgentClosed | A Standby send was attempted after Agent shutdown started. |

Once admitted, outcomes arrive on reliable MessageUpdate events and final Result.Messages()/Undelivered(), rather than as a second send return. Delivery failures do not necessarily fail the execution.

| MessageFailure | Wire value | Meaning |
|---|---|---|
| MessageCancelled | cancelled | Execution cancellation or withdrawal of an unclaimed Stream message. |
| MessageProviderRejected | provider_rejected | Provider rejection or no safe continuation path. |
| MessageTimeout | timeout | A delivery attempt timed out without a successful fallback. |
| MessageLimitExceeded | limit_exceeded | Execution cannot start another provider turn. |
| MessageRunFailed | run_failed | Execution failed before delivery. |

Boundary-specific failures can requeue for checkpoint continuation, reporting Accepted again; an ambiguous earlier attempt is marked as a possible duplicate. A Driver-claimed item is settled by its acknowledgement, even when its sender context ends. Stream send contexts can withdraw unclaimed input; Standby send contexts govern admission only, so an accepted HTTP message survives request completion.

A Standby remains alive after an execution failure. Only classified rate-limit/network/provider-server failures with undelivered work retry automatically, at most once. A run_failed message can carry to the next execution and report failed, then delivered under the same ID. Without an eligible retry, carried input waits for a host message. Cancel clears pending input instead.

MessageID deduplicates within the Thread (up to 1000 delivered receipts, seven days). A crash after provider acceptance but before receipt persistence can replay the message. A third-party in-run-only Driver without resume reports failure for input admitted at its final boundary race. See the [delivery matrix and limits](./streaming.md#12-message-delivery-capabilities).

## Host-defined Tool errors

Package `tool` owns three stable categories. Invalid definitions are retained
by `tool.Define` and surface before `Driver.Run`; input and output failures are
validated at the host-handler boundary.

| Sentinel | Meaning |
|---|---|
| `tool.ErrInvalidDefinition` | The name, description, handler, annotations, Go types, or schemas cannot form a valid Tool. |
| `tool.ErrInvalidInput` | Provider arguments fail JSON/schema validation or cannot decode into the declared Go input type. |
| `tool.ErrInvalidOutput` | A handler result cannot encode as JSON or fails its output schema. |

`tool.Reject(code, message)` is deliberately a constructor rather than an
additional public error type. It marks an expected, model-visible Tool failure;
all other handler errors and panics are sanitized by the internal runtime.
`tool.AsRejection(err)` recognizes only errors minted by `tool.Reject`, even
through wrapping; an application-defined error cannot forge safe-delivery
status by implementing a similarly named method.

## Root Thread errors

The root Thread API translates store/coordinator failures into application
sentinels. Root consumers should match these names rather than depending on a
particular store implementation.

| Sentinel | Meaning | Typical host action |
|---|---|---|
| `ErrThreadNotFound` | A resume-only key or fork parent has no active record. | Return not-found or offer a new Thread. |
| `ErrThreadBusy` | A task execution or live Standby already owns this key, or another process holds its lease. | Route extra input to the existing Stream/Standby; route cross-process input to the owner. |
| `ErrThreadIncompatible` | Driver/config/identity/resolved-environment fingerprint or codec compatibility failed. | Keep the old record; explicitly create a new Thread if desired. |
| `ErrThreadLeaseLost` | The run lost lease ownership, so its state was not persisted. | Treat the outcome as non-authoritative and investigate the store. |
| `ErrThreadCheckpointMissing` | A nominally successful Thread run did not prove a healthy resumable checkpoint. | Preserve the previous healthy record and inspect the Driver. |
| `ErrThreadAlreadyExists` | A fork target key already has an active conversation. | Choose another target key; parent and target remain unchanged. |
| `ErrResumeRejected` | The Driver rejected a resume and the selected mode did not allow a fresh retry. | Inspect compatibility/auth state or explicitly create a new Thread. |

Named Threads use a private in-memory store without WithThreadStore. Reopening Standby on the same Agent/key returns the existing live handle; it does not raise ErrThreadBusy. Standby creation during a task returns ErrThreadBusy directly, while task-mode sends report it through Result/Done.

The root contract promises `errors.Is` for these rows, not store-specific
typed details. A `driver.SessionConfigFingerprintError` can remain discoverable
with `errors.As` inside an `ErrThreadIncompatible` chain when strict config
canonicalization caused the incompatibility.

## Approval responder errors

These are method errors from `ApprovalRequest.Approve`, `Deny`, and `Answer`,
not run verdicts. A request returned by Forward shares the original responder; answering either copy resolves both. Origin adds attribution, not another response channel.

| Sentinel | Meaning |
|---|---|
| `ErrApprovalResolved` | A response already won the exactly-once race. |
| `ErrApprovalExpired` | The deadline or owning invocation ended before this response. It also matches `ErrApprovalResolved`. |
| `ErrApprovalKindMismatch` | The response method does not fit the request kind. |
| `ErrApprovalUnavailable` | The request is nil, zero-valued, or has no run-owned responder. |

```go
if err := request.Approve(ctx); err != nil {
	switch {
	case errors.Is(err, adaptor.ErrApprovalExpired):
		// The UI response arrived too late.
	case errors.Is(err, adaptor.ErrApprovalResolved):
		// A duplicate response lost the race.
	}
}
```

## `threadstore` errors

Store implementors and direct store consumers use the leaf identities. The
root Thread API translates them to the root Thread categories above.

| Sentinel | Typed error | Produced by |
|---|---|---|
| `threadstore.ErrBusy` | `*threadstore.BusyError{Target}` | `AcquireLease` while another owner has a live lease. |
| `threadstore.ErrLeaseLost` | `*threadstore.LeaseLostError{Target}` | `RenewLease` or `Finalize` after owner/token/expiry validation fails. |
| `threadstore.ErrAlreadyExists` | `*threadstore.AlreadyExistsError{Key}` | Conditional `Finalize` when a key was required to be absent. |

All three typed errors unwrap to their sentinel. `ReleaseLease` is idempotent
and does not turn a stale release into `ErrLeaseLost`.

## Driver extension errors

Extension authors should return the canonical `driver` identities listed in
the pre-invocation table. Their typed forms are:

- `*driver.InvalidDriverConfigError`
- `*driver.InvalidPolicyError`
- `*driver.PolicyCapabilityUnsupportedError`
- `*driver.HumanDecisionModeUnsupportedError`
- `*driver.InvalidOutputSchemaError`
- `*driver.StructuredOutputUnsupportedError`

`*driver.SessionConfigFingerprintError` has no sentinel. Match it with
`errors.As`; its `Path`, `Type`, `Kind`, and `Why` fields describe only the
unsupported Go shape and intentionally do not expose configuration values or
map keys.

## A2A client errors

Package `clients/a2a` owns transport/client categories:

| Sentinel | Meaning |
|---|---|
| `a2a.ErrInvalidAgentCard` | The card or selected interface is incomplete or invalid. |
| `a2a.ErrProtocol` | A request, response, part, or event violates the supported protocol shape. |
| `a2a.ErrUnauthorized` | The remote endpoint rejected authentication/authorization. |
| `a2a.ErrNotFound` | The remote task does not exist. |
| `a2a.ErrUnsupported` | The requested operation or content type is unsupported. |
| `a2a.ErrUntrustedOrigin` | Credentials would cross an origin not explicitly trusted by client options. |

`*a2a.ProtocolError` exposes `Op`, `Reason`, `Cause`, and sanitized `Raw`, and
unwraps its cause (or `ErrProtocol` when no cause is set).
`*a2a.StreamRecoveryError` exposes `TaskID` and unwraps the disconnection or
recovery cause; it has no dedicated sentinel.

## Hosttool errors

`hosttools/sessionrecorder` exports:

| Sentinel | Meaning |
|---|---|
| `sessionrecorder.ErrInvalidSessionKey` | A recorder/backend key validator or mandatory path-containment check rejected the key. |
| `sessionrecorder.ErrJSONLEventBackendClosed` | An operation was attempted after the durable event backend closed. |
| `sessionrecorder.ErrJSONLEventLogCorrupt` | A malformed, truncated, or inconsistent JSONL audit log could not be replayed faithfully. |

For team/delegation and remote A2A error contracts, see [A2A integration](./a2a.md) and the relevant package godoc.

The `profile`, `memory`, and bridge packages currently define no additional
SDK-owned stable error sentinels. They return documented standard errors,
wrapped root/leaf errors, or external protocol-library errors as appropriate.

## Matching rules

- Match categories with `errors.Is`, never string comparison.
- Use `errors.As` only for a documented typed error and keep the pointer target
  form (`var typed *T; errors.As(err, &typed)`).
- Do not collapse unrelated categories into a global `IsConflict` or
  `IsExpired` helper. Hosts can define domain-specific groupings without
  freezing them into the SDK.
- Preserve the full chain with `%w` when adding host context.
- Treat recommended retries and user-facing status codes as host policy; the
  SDK defines error identity and semantics, not an HTTP response matrix.

See [Run policy](./run-policy.md), [Structured output](./structured-output.md),
and [`AGENTS.md`](../AGENTS.md) for the associated behavioral contracts.
