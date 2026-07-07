# Public Errors Reference

The single complete catalogue of `agentadaptor` public errors, with the
recommended host integration response per error. The inline godoc on
[`errors.go`](../errors.go) mirrors the same matrix; this document is
the longer-form reference hosts should link to from their own runbooks.

> Source-of-truth invariant: every public error in the `agentadaptor`
> package MUST appear here. New errors that don't update this table will
> drift the integration matrix. CI does not yet enforce this; reviewers
> please check it manually until a `go vet`-style linter is wired up.

## How to read this matrix

| Column | Meaning |
|---|---|
| **Sentinel** | The exported `Err*` variable. Use `errors.Is(err, ErrXxx)` to match. |
| **Typed?** | Whether a `*Error` typed error wraps the sentinel and carries structured fields hosts may extract via `errors.As`. |
| **HTTP** | Recommended HTTP status code when the host's outbound API surfaces this error. *Recommended*, not normative — hosts with their own error code conventions should map accordingly. |
| **Log level** | Recommended log level. `info` = expected user error, `warn` = misconfiguration, `err` = SDK or backend bug. |
| **Alert** | Whether the error usually warrants paging (yes/no). |
| **Predicate** | If a typed predicate exists in `errors.go`, it appears here. Otherwise use `errors.Is` directly. |

## Agent / binding errors

These errors surface during SDK construction (`agentadaptor.New(...)`)
or when invoking `Admin().Agent(...)` / `SDK.Agent(...)`. Most of them
indicate misconfiguration that should never reach production.

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrAgentBindingRequired` | — | 400 | warn | no | `errors.Is` | `WithAgent` / `WithDefaultAgent` called with `nil` binding |
| `ErrAgentNameRequired` | — | 400 | warn | no | `errors.Is` | `WithAgent("")` |
| `ErrAgentNotFound` | — | 404 | info | no | `errors.Is` | `SDK.Agent(name)` / `Admin.Agent(name)` for an unregistered name |
| `ErrDefaultAgentAlreadyConfigured` | — | (panic) | — | — | — | `New` invoked with two `WithDefaultAgent`. Panics — never reaches HTTP. |
| `ErrDefaultAgentMissing` | — | (panic) | — | — | — | `New` invoked with no `WithDefaultAgent`. Panics — never reaches HTTP. |
| `ErrInvalidDriverConfig` | — | 400 | warn | no | `errors.Is` | Adapter `ValidateConfig` rejected the binding's config |
| `ErrReservedAgentName` | — | 400 | warn | no | `errors.Is` | `WithAgent("default", ...)` (`default` is reserved for the default binding) |
| (typed) `*DuplicateAgentError` | yes | 400 | warn | no | `errors.As` | `WithAgent(name, ...)` where `name` was already registered |

## Session errors

Errors related to session resume, lease coordination, and compatibility
fingerprinting. See `AGENTS.md §6` for the session ontology.

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrResumeRejected` | yes (`*ResumeRejectedError`) | 409 | warn | no | `errors.Is` / `errors.As` | Adapter rejected the resume attempt; `Reason` carries the adapter-side message |
| `ErrSessionBusy` | yes (`*SessionBusyError`) | 409 | info | no | `IsSessionBusy` | Another worker holds the session lease; retry after backoff |
| `ErrSessionCheckpointMissing` | — | 404 | info | no | `errors.Is` | The store has the SessionRecord but no `DriverState` to resume from |
| `ErrSessionIncompatible` | yes (`*SessionIncompatibleError`) | 409 | warn | no | `IsSessionIncompatible` | Stored fingerprint differs from current; UI should suggest `start_new` |
| `ErrSessionLeaseLost` | yes (`*SessionLeaseLostError`) | 409 | warn | **yes** | `errors.Is` | Lease holder lost the lease mid-run (clock skew, store timeout); investigate |
| `ErrSessionNotFound` | — | 404 | info | no | `errors.Is` | `SessionStore.Resolve` returned no record |
| `ErrSessionStoreRequired` | — | 500 | err | **yes** | `errors.Is` | Session-aware Run on an SDK with no `SessionStore`; must be a config bug |

## MCP errors

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrInvalidMCPConfig` | — | 400 | warn | no | `errors.Is` | `WithMCP` / `WithDefaultMCP` got malformed `MCPConfig` |
| `ErrMCPUnsupported` | — | 400 | info | no | `errors.Is` | Adapter declares no MCP support in its descriptor |
| `ErrMCPTransportUnsupported` | — | 400 | info | no | `errors.Is` | Adapter declares MCP support, but not for the requested transport |

## HITL errors (see `docs/workstream-hitl-v2.md`)

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrHumanDecisionModeUnsupported` | — | 400 | warn | no | `errors.Is` | `Start` invoked with a HumanDecisionPolicy mode the adapter's `RunPolicyCaps` does not advertise |
| `ErrDecisionRequestExpired` | — | 409 | info | no | `IsDecisionExpired` | `ResolveDecision` after the request was already resolved or its deadline elapsed |
| `ErrDecisionResultKindMismatch` | — | 400 | warn | no | `errors.Is` | `DecisionResponse.Kind` doesn't match `DecisionRequest.Kind` |
| `ErrRunEnded` | — | 409 | info | no | `IsRunEnded` | Operation invoked on a `RunHandle` whose run already terminated; usually a benign race |

## Structured-output errors (see `docs/structured-output.md`)

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrStructuredOutputUnsupported` | yes (`*StructuredOutputUnsupportedError`) | 400 | warn | no | `errors.Is` / `errors.As` | A run requested a structured-output mode the adapter capability matrix cannot honor, for example Cursor with `NativeStrictOutput()` |
| `ErrInvalidOutputSchema` | yes (`*InvalidOutputSchemaError`) | 400 | warn | no | `errors.Is` / `errors.As` | The JSON Schema document cannot be parsed/compiled, an SDK-owned schema helper cannot derive a safe schema, or adapter `ExtraArgs` conflict with SDK-managed schema flags |

## Skill errors (see `docs/skill-api-design.md` / `docs/v0.5.0-host-integration-plan.md` §A1)

| Sentinel | Typed? | HTTP | Log | Alert | Predicate | Trigger |
|---|---|---|---|---|---|---|
| `ErrSkillKeyConflict` | yes (`*SkillKeyConflictError`) | 500 | err | **yes** | `IsSkillKeyConflict` | Two skill candidates share Key but differ structurally; usually means the catalogue source is out of sync |
| `ErrSkillMaterializationFailed` | yes (`*SkillMaterializationError`) | 500 | err | no | `IsSkillMaterializationFailed` | A selected skill was resolved but could not be materialized into a local `SKILL.md` directory before the adapter started |
| `ErrSkillSourceMissing` | — | 500 | err | no | `errors.Is` | A `Skill` value reached resolution without a `Source` |
| `ErrSkillKeyMissing` | — | 500 | err | no | `errors.Is` | A `Skill` value was constructed with empty Key |
| `ErrSkillNotFound` | — | 404 | info | no | `errors.Is` | A bare `SkillKey` referenced via `WithSkills` / `WithDefaultSkills` cannot be resolved against any configured source |

## Why no aggregate predicates

The SDK exports six typed-error predicates and that is by design. We
do **not** export aggregate predicates such as `IsExpired(err)` (would
match `ErrDecisionRequestExpired` + `ErrRunEnded`) or `IsConflict(err)`
(would match `ErrSkillKeyConflict` + `*DuplicateAgentError` + ...).
Reasons:

1. **Aggregation is irreversible.** Once a host writes
   `if IsExpired(err) { ack409() }`, the SDK can never split
   `ErrRunEnded` into more specific sentinels (e.g. `ErrRunEndedNormally`
   vs `ErrRunEndedAborted`) without silently changing the host's
   behaviour — even if the new sentinels are harmless.
2. **Cross-sentinel "semantic similarity" is the SDK author's view, not
   the host's.** A host that gets `ErrSkillKeyConflict` typically
   triggers a "rebuild catalogue" workflow; a host that gets
   `*DuplicateAgentError` triggers a "fix construction-time config"
   workflow. Aggregating them into `IsConflict` would actively mislead.
3. **`errors.Is(err, ErrXxx)` is enough.** The standard pattern is one
   line, well-known, and respects the wrapping chain. Predicates exist
   only when there is real value — typed-error syntactic sugar where
   `errors.Is` and `errors.As` would otherwise both be needed.

If you find yourself writing
`if errors.Is(err, ErrA) || errors.Is(err, ErrB) { ... }` more than 2-3
times in your codebase, define your own host-side predicate; do not
expect the SDK to add one upstream.

## Error chains and `errors.Is` / `errors.As`

Typed errors either unwrap to their corresponding sentinel or implement
`Is` for that sentinel while unwrapping a lower-level cause:

```go
var typedErr *agentadaptor.SessionBusyError
if errors.As(runErr, &typedErr) {
    log.Printf("session %q is busy", typedErr.Target)
}

if errors.Is(runErr, agentadaptor.ErrSessionBusy) {
    // Same condition, but no access to the typed fields.
}
```

`*ResumeRejectedError` is the only typed error that joins two errors
in its `Unwrap()` chain (`ErrResumeRejected` and the adapter-supplied
`Cause`). `errors.Is` / `errors.As` walk both branches:

```go
err := &ResumeRejectedError{Reason: "auth", Cause: io.ErrUnexpectedEOF}

errors.Is(err, ErrResumeRejected)        // true
errors.Is(err, io.ErrUnexpectedEOF)      // true (joined branch)
```

`*SkillMaterializationError` matches `ErrSkillMaterializationFailed`
through `Is` and unwraps the underlying materializer cause. This lets hosts
branch on the SDK-level failure while still preserving lower-level matches:

```go
var matErr *agentadaptor.SkillMaterializationError
if errors.As(runErr, &matErr) {
    log.Printf("skill %q failed to materialize: %v", matErr.Key, matErr.Cause)
}
```

## See also

- [`errors.go`](../errors.go): inline godoc with the same matrix
- [`docs/v0.5.0-host-integration-plan.md`](./v0.5.0-host-integration-plan.md) §A4: rationale for the matrix and the deliberate absence of aggregate predicates
- [`AGENTS.md`](../AGENTS.md) §6: session ontology that the session errors reference
- [`docs/workstream-hitl-v2.md`](./workstream-hitl-v2.md): HITL contract that the HITL errors reference
