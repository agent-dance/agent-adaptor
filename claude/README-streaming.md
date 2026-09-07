# Claude provider streaming

Claude implements `driver.StreamSupport` and advertises native, token-level,
reasoning, tool-argument, and HITL event fidelity. Applications still use the
same public `Runner.Run` or `Runner.Stream` API: both execute through one Event
pipeline, and the resolved invocation—not the public verb—selects the Claude
provider transport.

## Provider transport

For ordinary observational runs, the Driver invokes Claude Code with
`--print --output-format stream-json --verbose` and, when the resolved request
selects rich provider events, adds `--include-partial-messages`.

Explicit Permission, PlanReview, or Question `Ask` policy selects Claude's bidirectional
control transport. It adds `--input-format stream-json`,
`--replay-user-messages`, and `--permission-prompt-tool stdio`; approval
requests are resolved through the same decision-capable Event sink used by all
Agent runs. Ordinary Bash/Write/Edit permission requests use the same typed,
exactly-once decision path; the CLI remains responsible for executing an
approved tool.

Native structured output uses Claude's `--json-schema`. Question and PlanReview
Ask can share a bidirectional stream-json process with native schema output.
Native schema does not advertise Permission Ask; core selects prompt validation
for that combination, omitting `--json-schema` and validating final JSON text.
The Driver executes the source resolved by core and does not change the policy.

The precise per-mechanism matrices account for effective defaults: unset
Permission and PlanReview inherit Ask. Setting only QuestionAsk therefore also
requires Permission Ask support and selects prompt validation. To request native
schema with Question/PlanReview Ask, the caller must explicitly choose a
compatible Permission auto policy. This is a caller decision; the SDK never
auto-approves Permission merely to retain native schema. `WorksWithHITL=false`
remains a conservative legacy summary; the non-nil matrices describe each
mechanism's actual supported kinds.

Schema eligibility and control transport activation have distinct rules. A zero
raw policy still uses the existing observational transport, while its effective
Permission Ask excludes native schema and selects prompt validation. This does
not prove that a Permission request occurred or was answered. Explicit Ask (or
the existing auto decision settings requiring control) activates bidirectional
stdin; ordinary Permission Ask without schema retains its existing behavior.

Native schema remains a temporary process shape for Thread calls: the previous
resident writer stops before this turn starts; a healthy checkpoint may then be
prewarmed. `WithSpawn` suppresses registering or prewarming a resident writer.
A Thread key alone does not imply that native schema rounds reuse one PID.

A one-shot bidirectional run closes stdin exactly once when the parser sees a
formal `type:result`, even if no `message_stop(end_turn)` preceded it. A terminal
root message can also close stdin. Empty or `tool_use` stop reasons and nested
subagent messages keep input open for control responses. Resident Thread turns
release only their per-turn handle; the underlying stdin remains open for the
next turn. A result does not bypass draining stdout/stderr or the final process
and checkpoint checks.

## Event and result contract

The protocol parser maps official Claude stream-json frames to
`driver.StreamPayload` values for text, thinking, tool calls, tool results,
lifecycle, and HITL. The same parse pass builds the final Text, Summary, Raw
streams and terminal payload, Transcript, Usage, failure, and checkpoint.
Provider user frames marked as replay are acknowledgements for the control
transport; they are not replayed as assistant output. `ResultMessage.result` is the sole authority
for final Text; intermediate assistant frames and deltas remain in the Event
stream and Transcript.

A resident disconnect, cancellation, deadline, or decision error preserves the
same parser's available Raw stdout/stderr, terminal payload, Transcript, final
text, Usage and service reports. `Run` and `Stream.Result` expose them through
`RunError.Result`, and `errors.Is/As` retain the original cause. Partial assistant
frames do not replace missing final Text. Terminal usage, including observed
zero values, is authoritative; otherwise formally observed stream usage remains
available. No output or session ID alone makes an interrupted checkpoint valid.
The previous healthy Thread record stays unchanged, and a possibly delivered
prompt is never automatically replayed.

A `DecisionCapableSink` error also stops a resident writer when the sink leaves
its caller context active. The resident reader drains the available stdout and
stderr before returning the original decision error, and a buffered success
result cannot register that aborted process for reuse or validate a checkpoint.
Normal result-only turns keep resident stdin available for the next turn.

The complete Driver obligations and consumer behavior are documented in
[`docs/streaming-adapter-contract.md`](../docs/streaming-adapter-contract.md)
and [`docs/streaming.md`](../docs/streaming.md).

## Verification

- `go test ./claude` runs fixture-based protocol, lifecycle, output, session,
  and HITL contracts without calling a provider. Live conformance clauses stay
  disabled unless `AGENT_ADAPTOR_LIVE_CONFORMANCE=1` and the CLI is available.
- `go test -tags=claude_live -run '^$' ./claude` compile-checks the live suite.
  Running a live test additionally requires its explicit environment-variable
  gate, an installed Claude CLI, and an authenticated local profile.
