# Public errors

This reference covers the stable error identities owned by the root
`adaptor` package and the public leaf packages that define Driver, skill, MCP,
Thread storage, A2A client, and hosttool contracts.

Use `errors.Is` for a category and `errors.As` when the table names a typed
error. Error strings are diagnostics, not matching contracts.

## One execution error path

`Runner.Run` and `Stream.Result` use one verdict model:

- success returns `*Result, nil`;
- a completed business failure returns `*RunError`, whose `Result` retains
  the available audit data;
- configuration, context, process, protocol, store, and resource failures are
  ordinary wrapped errors.

```go
result, err := runner.Run(ctx, prompt)
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

`Stream` itself returns immediately; setup and execution failures are read
from `Stream.Result()` after its event channel closes. Programmer-contract
violations documented as panics, such as constructing an Agent with a nil
Driver or creating a Thread with an empty key, are not error sentinels.

## Agent lifecycle

`ErrAgentClosed` means `Agent.Close` has started. New `Run`/`Stream` calls on
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
| `ErrStructuredOutputUnsupported` | `driver.ErrStructuredOutputUnsupported` | `*adaptor.StructuredOutputUnsupportedError` (`Driver`, `Mode`, `Reason`) | The structured-output capability matrix cannot honor the request. |
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
with `Code`, `Message`, `Retryable`, `RemoteStatus`, and `Metadata`. It
implements `error` but has no sentinel and no unwrap contract.

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
