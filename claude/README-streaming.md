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

Native structured output uses Claude's `--json-schema`. Interactive HITL plus
native schema output is rejected explicitly; prompt validation remains the
portable combination.

## Event and result contract

The protocol parser maps official Claude stream-json frames to
`driver.StreamPayload` values for text, thinking, tool calls, tool results,
lifecycle, and HITL. The same parse pass builds the final Text, Summary, Raw
streams and terminal payload, Transcript, Usage, failure, and checkpoint.
Provider user frames are acknowledgements for the control transport; they are
not replayed as assistant output. `ResultMessage.result` is the sole authority
for final Text; intermediate assistant frames and deltas remain in the Event
stream and Transcript.

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
