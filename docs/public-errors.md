# Public errors

This reference covers the stable error identities owned by the root
`adaptor` package and the public leaf packages that define Driver, Tool, skill, MCP,
Thread storage, A2A client, and hosttool contracts.

Use `errors.Is` for a category and `errors.As` when the table names a typed
error. Error strings are diagnostics, not matching contracts.

## One execution error path

`Runner.Run` and `Stream.Result` use one verdict model:

- success returns `*Result, nil`;
- after `Driver.Run` has been entered, every failure returns `nil, *RunError`,
  whose non-nil `Result` retains the available audit data and whose `Cause`
  preserves original and secondary errors;
- failures before that boundary, including configuration, resource preparation
  and Thread acquisition, remain ordinary wrapped errors.

```go
result, err := runner.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		result = runErr.Result
		log.Printf("run failure %s: %s", runErr.Reason, runErr.Message)
		return err
	}
	return err
}
_ = result
```

`Stream` itself returns immediately; setup and execution failures are read
from `Stream.Result()` after its event channel closes. Programmer-contract
violations documented as panics, such as constructing an Agent with a nil
Driver or creating a Thread with an empty key, are not error sentinels.

Native append validation returns ErrSystemPromptUnsupported and
*SystemPromptUnsupportedError with controlled Driver/Reason diagnostics.
Invalid UTF-8/NUL or unsupported nonempty append is rejected before resources.
Active budget zero is unlimited, negative matches ErrInvalidPolicy. A positive
budget exhausted before Driver entry remains a wrapped typed timeout without
inventing a Result; afterward it is the primary RunError when it wins the
terminal race. Parent/approval deadlines and secondary causes stay distinct.

For an executable partial-result example, run `go run ./examples/offline`.
It uses a scripted Driver to exhaust a local active budget and reads the resulting
`RunError.Result.Text` and `Raw()` without calling a provider.

## Agent lifecycle

`ErrAgentClosed` means `Agent.Close` has started. New `Run`/`Stream` calls on
the Agent or any Thread derived from it fail with this sentinel and do not
restart the Driver's process pool. `Close` itself is idempotent; its context
error reports a bounded cleanup failure rather than changing this sentinel.

## Root execution failures

Every row is a `*adaptor.RunError`. `errors.As` exposes `Reason`, `Message`,
`Details`, `Cause`, and the non-nil `Result`. `Reason` identifies the primary
outcome; `errors.Is` can also match secondary errors in `Cause`.

| Sentinel | `FailureReason` | Meaning |
|---|---|---|
| `ErrActiveExecutionTimeout` | `ReasonActiveExecutionTimeout` | Core active budget exhausted; `*ActiveExecutionTimeoutError.Limit` retains the local duration. |
| `ErrApprovalDenied` | `ReasonApprovalDenied` | A host or auto policy denied an approval and the fallback aborted. |
| `ErrApprovalTimeout` | `ReasonApprovalTimeout` | An approval deadline elapsed and the fallback aborted. |
| `ErrAgentFailed` | `ReasonAgentError` | The Driver classified an agent-level failure, such as a bad terminal protocol or non-zero exit. |
| `ErrRunCancelled` | `ReasonCancelled` | Execution was canceled after Driver entry, or the Driver classified cancellation. |
| `ErrPolicyViolation` | `ReasonPolicyViolation` | A completed invocation violated a run policy, including default fail-on-invalid structured output. |

`ReasonDeadlineExceeded` identifies an observed execution deadline and preserves
`context.DeadlineExceeded`; `ReasonInfrastructure` identifies other unclassified
transport, protocol, store or cleanup failures and preserves their typed cause.
These two reasons add no new sentinels. A primary approval/provider failure can
also match a cancellation or cleanup cause: bridges must inspect `Reason` first.
Before Driver entry, a context error remains an ordinary error. Malformed host
policy values match `ErrInvalidPolicy`, not `ErrPolicyViolation`.

Cleanup can fail after a healthy checkpoint was atomically committed. Such a
failure returns the Result and cleanup cause, retains the committed state, and
does not roll back or repeat persistence. Interrupted or unhealthy executions
otherwise preserve the preceding healthy checkpoint.

Unknown Driver failure codes remain available as `RunError.Reason` but do not
silently match one of the listed sentinels above.

Claude and CodeBuddy resident failures build available Response data from their
formal parser before returning the cause, including partial/short-write failures
and stderr tails. EOF does not replace an observed original *exec.ExitError:
both remain reachable with errors.Is/As. A natural positive exit has agent_error
when there is no more specific failure; caller cancellation/deadline and prior
approval/provider failures keep priority over cleanup causes. Internal process
termination is not evidence that the caller canceled.

Usage is retained only from formal valid observations. Per-message cumulative
increments are aggregated without replay double-counting; authoritative terminal
zero remains observed zero. Absent/invalid counts do not invent non-nil zero
Usage. Error paths do not validate incomplete schema output as a replacement
failure or upgrade unhealthy checkpoints.

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
| `ErrInvalidPolicy` | `driver.ErrInvalidPolicy` | `*adaptor.InvalidPolicyError` (`Driver`, `Field`, `Value`) | A policy enum, action, or retry value is out of domain. |
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

Invalid Tool arguments also carry the private `invalid_input` rejection, so
`errors.Is(err, tool.ErrInvalidInput)` and `tool.AsRejection` both work. These
corrections omit values, schemas and underlying validation errors; extra field
names are bounded and escaped. Cancellation retains priority over a correction.

## Hosted profile errors

Package `profile` owns these categories for explicit Dedicated profiles with Tools:

| Sentinel | Meaning |
|---|---|
| `profile.ErrInUse` | Another Agent/process holds the namespace ownership lock. |
| `profile.ErrUnsafe` | Path, permissions, ownership records or file identity could not be verified. |
| `profile.ErrRecoveryRequired` | A previous active generation lacks proof of clean writer and gateway shutdown. |
| `profile.ErrUnsupportedFilesystem` | The filesystem cannot provide the required local lock/ownership guarantees. |

An OS lock released by process exit does not authorize takeover of an active
generation. See [profile lifetime and recovery](./tools.md#persistent-dedicated-profiles).

## Root Thread errors

The root Thread API translates store/coordinator failures into application
sentinels. Root consumers should match these names rather than depending on a
particular store implementation.

| Sentinel | Meaning | Typical host action |
|---|---|---|
| `ErrThreadStoreRequired` | A stateful Thread operation was requested without `WithThreadStore`. | Fix Agent construction. |
| `ErrThreadNotFound` | A resume-only key or fork parent has no active record. | Return not-found or offer a new Thread. |
| `ErrThreadBusy` | Another owner holds the required lease. | Retry with bounded backoff. |
| `ErrThreadIncompatible` | Driver/config/identity/resolved-environment fingerprint or codec compatibility failed. | Keep the old record; explicitly create a new Thread if desired. |
| `ErrThreadLeaseLost` | The run lost lease ownership, so its state was not persisted. | Treat the outcome as non-authoritative and investigate the store. |
| `ErrThreadCheckpointMissing` | A nominally successful Thread run did not prove a healthy resumable checkpoint. | Preserve the previous healthy record and inspect the Driver. |
| `ErrThreadAlreadyExists` | A fork target key already has an active conversation. | Choose another target key; parent and target remain unchanged. |
| `ErrResumeRejected` | The Driver rejected a resume and the selected mode did not allow a fresh retry. | Inspect compatibility/auth state or explicitly create a new Thread. |

The root contract promises `errors.Is` for these rows, not store-specific
typed details. A `driver.SessionConfigFingerprintError` can remain discoverable
with `errors.As` inside an `ErrThreadIncompatible` chain when strict config
canonicalization caused the incompatibility.

## Approval responder errors

These are method errors from `ApprovalRequest.Approve`, `Deny`, and `Answer`,
not run verdicts.

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

`hosttools/a2adelegation.DelegationError` is a typed remote/business failure
with `Code`, `Message`, `Retryable`, `RemoteStatus`, `Metadata` and `Cause`.
Code is authoritative; Unwrap exposes Cause, which is excluded from JSON. Local
failures retain original wrapped/joined causes and RunError's partial Result.
Remote errors promote only the [closed A2A controls](./a2a.md#stable-failure-classification).
A valid active code without limit matches ErrActiveExecutionTimeout but invents
no typed Limit. A positive wire limit rounds up to milliseconds and saturates
on conversion to nanoseconds. Unknown/invalid controls remain safe remote
failures. Parent context/active causes cannot override the primary Code.

The `memory` and bridge packages currently define no additional
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

## Optional observation and replay components

capabilityrecorder.ErrStoreRequired rejects nil/typed-nil Store; ErrInvalidQuery
rejects missing run scope or invalid pagination. Custom Store errors remain
observable without fallback. Append observer failure uses the existing safe
observation_disabled notice and leaves Result/HITL/checkpoint unchanged.

subagentstream.ErrEventInjectionUnsupported means a non-nil Merge bus lacks a
valid completed-run binding proof. Its RunError preserves the complete parent
Result, primary Reason and original error graph, including outer wrappers and
joined causes. It is not a second execution error channel.

Session JSONL corrupt/invalid replay returns sessionrecorder.ErrJSONLEventLogCorrupt.
Persistent append/write/sync errors remain visible; failed writes do not advance
HostSeq or silently succeed in memory. Replayed approvals cannot answer live
requests.
