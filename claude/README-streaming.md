# Claude provider streaming

Claude implements `driver.StreamSupport` and advertises native, token-level,
reasoning, tool-argument, and HITL event fidelity. Applications still use the
public `Runner.Send` API and consume one Event pipeline. The resolved
invocation selects the Claude provider transport.

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

## In-run messages

`Descriptor.Messages` declares the `tool` and `stop` boundaries, continuation
inside `Driver.Run`, and a minimum Claude Code version of **2.1.110**. Interrupt
and persistence of delivered hook context across resume are unverified and are
both reported as false. The driver does not currently probe the installed CLI
version; `MinVersion` is a requirement, not evidence of a successful probe.

`ResolveMessageCapability` reports these capabilities only for stream-json
input. A non-nil `Request.Messages` negotiates that input for ordinary calls
and native schema calls already using stream-json output. Batch native schema
calls keep JSON output and report the zero capability (`ContinueResume`).
Message-only negotiation leaves permission policy and partial-message selection
intact. Explicit HITL retains `--permission-prompt-tool stdio` and its normal
`can_use_tool` responses.

When messages are enabled, an acknowledged SDK `initialize` request registers
`UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, and `Stop`.
Only the registered main-agent callbacks open the delivery window; callbacks
with an `agent_id` or `parent_tool_use_id` receive an empty success response.
Tool callbacks inject claimed text as `additionalContext`. Stop claims both
`stop` and `turn_end` messages and returns `decision: block` with the combined
reason when input remains. An empty Stop closes the window.

Claims occur synchronously while parsing; a separate writer serializes actual
pipe writes and acknowledges each item afterwards. Failed writes requeue items;
cancellation fails them without requeueing. A write already in progress decides
its own outcome. Both per-run and resident processes use this same mechanism.
Residents initialize hooks once per process and bind a fresh MessagePort for
each call; changing whether messages are enabled replaces the resident process.

Initialization waits at most **10 seconds** for the control response. A rejected
or unanswered registration disables hooks for that process and emits a
`RunEventLifecycle` notice with code `claude_hooks_unavailable` and the reason.
The ordinary prompt and HITL control transport continue, the message window
stays closed, and core delivers queued messages at turn end through checkpoint
resume. Late success responses or callbacks cannot re-enable hooks. Resident
calls retain this outcome instead of retrying registration on every turn.
Cancellation interrupts the handshake; process exit and pipe failures remain
run failures.

Every continued Stop segment and the final provider result emits one
`RunEventTurnCompleted` with an increasing index. The final response retains
the whole call's Raw and Transcript, the final segment's result text, and the
provider result's Usage. Interim Stop events have no Usage because that hook
does not report per-segment token accounting.

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
