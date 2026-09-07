# T10 documentation fragment for G03

This fragment covers the T10 core delivery against canonical revision 15,
including R011, R012, R013 and R014. G03 must integrate the user-facing paragraphs below
before batch acceptance. Provider implementation and native/live verification
remain assigned to their later owners; this core delivery does not assert that
any built-in Driver already supports native append.

## API reference: option scopes, Policy, Thread, Inspect, errors

Add `WithAppendSystemPrompt(text string) SharedOption` to the dual-scope table in
`docs/api-reference.md` §3.1. It appends instructions through a Driver's native
system/developer channel while retaining the provider's default prompt. It is
independent of the user prompt, `WithInstructions`, profile resources and schema
prompt validation. The closest value replaces the farther value; the last option
in one scope wins. Only the empty string clears the SDK's configured append
value. Spaces, Unicode, line endings and quotes are preserved byte for byte.

For an explicitly append-capable `d driver.Driver`:

```go
agent := adaptor.New(d,
    adaptor.WithAppendSystemPrompt("Use the repository's terminology.\n"),
)
_, err := agent.Run(ctx, "Review this change") // construction default
_, err = agent.Run(ctx, "Review this change",
    adaptor.WithAppendSystemPrompt("Answer in Chinese.\n")) // replacement
_, err = agent.Run(ctx, "Review this change",
    adaptor.WithAppendSystemPrompt("")) // clear this SDK channel
_ = err
```

This example is conditional on the configured Driver capability. At T10's
integration point, built-in descriptors continue to report `Append: false`.
A nonempty unsupported request returns `ErrSystemPromptUnsupported` through
`*SystemPromptUnsupportedError` before resources, Thread leases or Driver.Run.
Invalid UTF-8 is rejected before embedded NUL, before capability checking; no
invalid text is repaired or silently discarded. The typed error contains only
the static Driver name and a controlled Reason, never the submitted text.

`Inspect().Environment(ctx)` performs the same read-only validation of the
construction default before using the configured Driver's probe. Inspect does
not materialize append files or run the Driver. ConfigSchema and ProfileState
gain no append resource/configuration mirror.

In §5, document that append content contributes its exact-byte hash in addition
to all existing Thread compatibility dimensions. Empty append preserves the
pre-existing base fingerprint. A changed or cleared nonempty append rejects
`ResumeOnly` and incompatible Fork; default continue-or-start replaces the old
record only after a new healthy checkpoint is atomically saved. The resolved
per-call `Request.Streaming` boolean remains outside persistent compatibility
as required by R011. Budgets do not alter session compatibility.

In §4 and `docs/run-policy.md` “Policy value and replacement rule”, add:

```go
agent := adaptor.New(d, adaptor.WithPolicy(adaptor.Policy{
    ActiveExecutionTimeout: 2 * time.Minute,
}))
_, err := agent.Run(ctx, "Review this change", adaptor.WithTimeout(10*time.Minute))
// Replacing the whole Policy with its zero value disables the active budget.
_, err = agent.Run(ctx, "Review another change", adaptor.WithPolicy(adaptor.Policy{}))
_ = err
```

`ActiveExecutionTimeout` is zero for unlimited active time, positive for a limit,
and negative for an `ErrInvalidPolicy` preflight failure. Every positive duration,
including less than one millisecond, is valid. `WithPolicy` still replaces the
whole Policy; zero does not inherit a construction budget. Existing presets use
zero. `WithTimeout` and parent deadlines remain absolute wall-clock bounds.

The active budget starts before resource preparation and covers workspace,
runtime services, Thread lease acquisition, safe resume fallback, Driver work,
observers and final schema/health checks. At a healthy candidate, the SDK settles
and permanently seals the budget immediately before its unique atomic Thread
persistence call; stateless execution seals at the corresponding health check.
Elapsed time is checked even if the timer callback has not been scheduled.
The seal indicates budget health only. Finalize must still succeed using the
original cancelable context; its work and delayed return do not spend active
budget. A sealed timer cannot later reclassify that call as active timeout.
Existing failures stop timing before cleanup, so cleanup does not manufacture a
late budget failure. Finalize and cleanup errors remain observable with the
available partial Result.

In §7, `docs/run-policy.md` “Approval policy” and `docs/streaming.md` §7, add:
Each Ask attempt pauses only its own run before queueing the approval/notice or
invoking OnApproval. A full event queue is therefore human wait, bounded by the
approval's independent wall-clock deadline. Overlapping Ask requests use
independent tokens: resolving one does not resume time while another remains.
Retries release the previous token and acquire a new one; automatic decisions
do not pause. Rejection, timeout, callback panic/error, cancellation and late
responses retain existing approval/error semantics. Parent cancellation,
Agent.Close and lease renewal continue during Ask. A nested member's Ask does
not pause its leader's run.

In §8, `docs/run-policy.md` “Run errors” and `docs/streaming.md` §4, add:
`ReasonActiveExecutionTimeout` is `"active_execution_timeout"` and matches
`ErrActiveExecutionTimeout`; `errors.As` can retrieve
`*ActiveExecutionTimeoutError` with the exact local `Limit time.Duration`.
After Driver entry, failure returns a single `*RunError` carrying all available
Text, Summary, Raw (including official terminal payload), Transcript, Usage and
service reports. Before Driver entry, errors retain their wrapped identity and
there is no invented Result. The first confirmed terminal reason is authoritative;
later cancellation/process/lease/cleanup errors remain inspectable causes.
Consumers must select `RunError.Reason` before generic `errors.Is(ctx error)`
checks, because multiple causes may match. Approval timeout, parent deadline,
explicit cancellation and active exhaustion are distinct reasons.

An inherited parent cause may itself be an ActiveExecutionTimeoutError (for
example, a leader's budget canceling a nested member). Its type alone does not
mean the current run exhausted its own budget: the parent Err determines
Cancelled/DeadlineExceeded, while the original cause remains inspectable.
Conversely, a confirmed local budget expiry remains authoritative if the parent's
cancellation notification is delivered afterwards. Sealing checks the direct
parent state even when its AfterFunc cancellation delivery to the child lags.
R014 fixes the earlier boundary too: the controller's locked cause selection is
the confirmation point, before the lock-free context notification. A later parent
may legitimately win the standard context cause while the run's primary Reason
remains the already-selected local expiry; both causes remain inspectable. The
private SelectedCause method only reads that existing selection and cannot charge
time or infer a new parent cause. Core reads and binds its controller under the
terminal lock, with a one-way terminal-to-controller lock order.

R013 clarification for the Approval and Event sections: constructing an
ApprovalRequest and copying it with WithEventMeta creates independent Choices
and Details descriptions. Details copies JSON containers recursively, including
interface-held maps, slices and arrays; it does not promise deep copying arbitrary
pointer/struct object graphs. Mutating one consumer's description cannot alter
the Driver's request, another consumer's description or an original Question's
choice classification. Live copies still share the run-owned responder: only one
Approve/Deny/Answer succeeds, Kind checks use its original kind, and cancellation
or expiry makes every copy unavailable for a new response. Recorder replay has
the separate responsibility to remove the responder; live event copying does
not strip response authority.

## Store contract: API reference §13

Finalize checks cancellation before its atomic commit, including after acquiring
any locks. The memory store checks both before its mutex and again immediately
before the first write: cancellation while waiting cannot save, archive or
rebind records. Once a healthy atomic commit has happened, a later cancellation
during return delay does not roll it back. Store's error-only interface is not a
commit-acknowledgment protocol; the SDK does not infer commit time from return
time and does not use detached persistence.

## Driver SPI and transport handoff

The new SPI declarations are `SystemPromptCapability{Append bool}`,
`Descriptor.SystemPrompt`, `Request.AppendSystemPrompt`, the typed unsupported
error and sentinel, and `FailureActiveExecutionTimeout`. Descriptor zero means
unsupported. Core passes one resolved exact string to one Driver.Run; the Driver
owns native mapping, extra-argument conflict checks, persistent process signatures
and checkpoint guards. The SDK does not append this text to diagnostic events,
metadata, service declarations, profile manifests or user Prompt.

T14–T16 must consume `internal/systemprompt.Fingerprint` in their persistent
startup signatures and checkpoint guards and hold a materialized File for the
actual process lifetime, including prewarm/replacement/Close. T10 implements
the complete frozen helper API but cannot claim those unimplemented provider
calls as coverage of native support. W11-R03's T10 portion is exact-byte core
compatibility and the deterministic hash/helper contract; provider portions
remain in W11-R05–R07 and the C04 acceptance matrix.

`internal/systemprompt` uses the existing TOML encoder with decode round-trip
checking. Inline text is limited to 32768 UTF-8 bytes; complete Windows argv is
checked separately including executable, spaces, quote/backslash expansion and
terminal NUL, with 32767 UTF-16 units for native execution and an additional
8191-character cmd-shim bound. Unsafe cmd shell text is rejected rather than
reinterpreted. Native file and RPC paths do not receive the inline text limit.
Future CodeBuddy/Codex-exec usage docs must explain OS-visible argv, without
adding text to SDK diagnostics or truncating provider Raw audit output.

File materialization creates a random owned temporary directory with requested
0700 and a hash-named file with requested 0600, writes/Syncs/Closes before atomic
publication, and verifies full bytes/hash, regular type, object identity and
permissions. POSIX checks exact created modes; Windows preserves the modes
actually reported after restrictive creation, without widening read-only or ACL
settings to simulate POSIX bits. Symlink/reparse-point, replacement, same-length
tamper and mode drift fail closed. Close is idempotent and retryable, never adopts
a successor object and never recursively deletes unknown siblings. Cancellation
at creation/verification stages cleans created objects. There is no global
cache, profile manifest entry or sweep of another process's temporary directory.

## Golden and dependency review

Root additions: one `WithAppendSystemPrompt`, `RunSettings.SetAppendSystemPrompt`
(including its existing AgentSettings promotion), the reused Driver error alias
and sentinel, `Policy.ActiveExecutionTimeout`, active timeout reason/sentinel and
typed Limit error. Root With-function count changes from 26 to 27, counting
WithEventMeta as before. No additional execution verb, Config mirror, budget
option, public controller, mode selector or internal-type alias is added.

Both AST goldens were reviewed and updated only for those root/SPI declarations.
The fake clock seam and controller remain private. No dependency was added;
activebudget uses only the standard library and TOML reuses the existing library.

Rejected source behaviors: public PausableContext helpers, single paused bool,
Policy zero-inherits/negative-disables merging, size-only cache trust and implicit
provider support. The current repository's stronger single-pipeline, schema,
observer, ownership and partial-result contracts are retained.

## README and CHANGELOG paragraphs for G03

Update README.md and its zh-CN/ja/ko/de translations in “Options and resources”,
“Human approval” and “Results and errors” using the public paragraphs above.
Describe append as capability-gated and retain the accurate current provider
matrix until B04 integration. Update the count to 27, not an additional default
append option. The six consumer nouns and Run/Stream verbs are unchanged.

CHANGELOG.md Unreleased / Added: add the native append SharedOption and Driver
capability/request/error contract; add the Ask-aware Policy active budget and
typed timeout error. Unreleased / Fixed: distinguish parent deadline from an
approval's own timeout, preserve first confirmed terminal reason with partial
results and secondary causes, and prevent memory Finalize writes after
cancellation while waiting for its mutex. Also record R013's independent
approval description snapshots with preserved exactly-once response authority.
Unreleased / Changed: record the
explicit budget seal before atomic persistence, whole-value Policy semantics
and append compatibility behavior. Provider-specific support claims belong to
the later validated provider changes.

## Validation boundary

The delivery runs on Go 1.26.5, macOS arm64, with paid-provider/E2E/golden-update
gates disabled during ordinary checks. Baseline independent fixtures failed for
the missing append/budget contract, parent-deadline classification and memory's
canceled Finalize writes before the corresponding fixes. Final committed-SHA
command logs, executed pass/subtest counts, actual skips and artifact SHA-256
values are recorded in the adjacent result.json/evidence handoff after commit.
R013 also retains C01's fixed-G02 public red fixture evidence (11 failures) and
the same regression's local pre-repair red run (11 failures). The committed
regression derives from that independent fixture, covering all three approval
Kinds via callback/event, nested containers, 24 concurrent responder copies,
positive/deny, kind mismatch, nil/zero values and cancellation expiry.
R014 preserves the prior d623b71 evidence and adds a timer-Stop propagation barrier
for Driver and pre-Driver paths, a controller selected-cause/Finish control, and
binding versus Cancel/Close race coverage. The precise local propagation red
fixture produced four failed test/subtest outcomes before repair. Final evidence
belongs to the later committed SHA, not either earlier candidate.

Required commands retain their complete scopes and repetitions:

```text
go test -count=1 . ./driver ./adaptertest ./internal/activebudget ./internal/systemprompt ./memory ./threadstore
go test -race -count=10 . -run TestAlignmentActiveBudget
go test -race -count=10 ./internal/activebudget ./memory
go test -race -count=10 . -run TestAlignmentApprovalSnapshot
```

Windows command-line tests are pure mapping tests; any Windows cross-compilation
is build evidence only. No native Windows process round-trip, provider CLI/live
call or paid conformance is asserted by T10. Those remain the assigned native
and provider verification tasks, not silently permitted skips in T10 evidence.
