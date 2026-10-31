# Pi / OpenCode alignment with Claude Code

This workstream targets functional alignment with the current Claude Driver,
not merely successful CLI invocation. Completion is still unproven. The
reference is `claude/driver.go`, `claude/persistent.go`, its parsers, the public
Driver SPI and `AGENTS.md`. Unsupported declarations do not close parity gaps.

## Acceptance inventory

| Area | Claude baseline | Pi evidence / remaining work | OpenCode evidence / remaining work |
|---|---|---|---|
| Construction/execution | Captured Config; one Run/Stream pipeline | Implemented, hermetic result parity | Implemented, hermetic result parity |
| Streaming | Text, thinking, tool argument deltas and snapshots | Official argument deltas now parsed; malformed/lifecycle tests and live evidence | Server text/thinking deltas proven; upstream 1.14.30 discards tool argument deltas, snapshots retained |
| Permission | Ask, auto-approve/deny, callback and Event forms | Official pre-tool extension; root tests cover approve, deny/continue, timeout/continue, cancel; live denial prevents actual writes | Native server requests use core; callback/Event, reject/continue, timeout and cancel tested; live denied write prevented |
| Plan review | Explicit plan tool enters core approval policy | `adaptor_review_plan` extension; live approval verified | Native `plan_exit` correlated by tool identity; live plan review approved |
| Questions | Structured questions and answers | `adaptor_ask_user` and native RPC select/confirm/input/editor; live Event answers verified | Native question/reply; choices and free text preserved; live Event answer verified; multiple selection UX remains a public single-answer limitation |
| HITL invariants | Exactly-once responses, one Event stream, core policy, cancellation | Existing core responder; no local timeout or fallback policy; callback/Event tests | Existing core responder, no transport timeout/retry policy; native resolved deny rules preserved |
| Thread | Resume, fork, guards, atomic healthy checkpoint | Live resume/fork and hermetic failure tests | Live resume/fork and hermetic failure tests |
| Process lifecycle | Persistent Thread writer; bounded replacement; WithSpawn and Close | RPC pool; reuse, fork isolation, drift/rebind, idle reap, cancellation, startup retry, no replay after prompt and Close tested; live hosted tools/schema reuse and WithSpawn proven | Private server pool; reuse, fork isolation, replacement, idle reap, cancellation, startup retry, no replay after prompt and Close tested; live hosted tools/schema reuse and WithSpawn proven |
| Structured output | Native schema when available; validated prompt fallback | Prompt validation including HITL; native mechanism needs investigation | Native server schema and simultaneous HITL proven with GPT 5.6 luna; local validation retained |
| Audit results | Complete Raw/Transcript, terminal, usage, model/provider, bounded summary | JSON and RPC retain per-turn raw bytes, terminal, transcript and usage; RPC requires accepted prompt, agent_settled and explicit idle session state; cancellation retains error identity | Both protocols retain stdout/stderr/terminal/transcript; SSE retained as HTTP metadata; actual terminal model/provider mapped; summary intentionally empty |
| Skills | Discover, select, sync, execute | Durable skills sync, observed ownership, conflict/prune tests, native-content guards and fresh-Agent GPT 5.6 luna delivery proven | Durable skills sync, observed ownership, conflict/prune tests, native-content guards and fresh-Agent GPT 5.6 luna delivery proven |
| MCP / hosted Tools | stdio/HTTP/SSE, current bindings and credential isolation | Official extension + existing MCP SDK; durable file-managed declarations, fresh-Agent calls on all three transports and per-call clear proven | Native server API and durable JSONC; fresh/cloned-Agent calls on all three transports, required-connection checks, credential-safe replacement and per-call clear proven |
| Profiles | Shared/dedicated/clone; settings/auth; desired/observed state | Read-only six-family snapshots, MCP, skills and native instructions sync; other resource families remain | Read-only six-family snapshots, MCP, skills and native instructions sync; MCP clone/layering verified; auth clone/isolation and other resource families remain |
| Instructions | Run and persistent sync, scope/append semantics | Run flags plus default/user APPEND_SYSTEM.md and SYSTEM.md sync; fresh-Agent live evidence; persistent project/local scope remains | Per-turn additive system field, verified native agent.prompt replacement, default/user AGENTS.md sync; live replacement/default selection/reuse and fresh-Agent sync evidence; durable replacement and persistent project/local scope remain |
| Profile agents | Portable fields, native escape, field-level support | File-managed declarations and isolated native children through official extension; instructions/model/thinking/tool policy, selected/installed skill preload, MCP key selection, strict Markdown source imports, ownership/clone/clear and child HITL implemented with live evidence. Broader source syntax, permission overrides, hooks/native agent fields and child resume remain | Owned JSONC and invocation overlays; instructions/model/variant/tool policy and strict Markdown source imports with ordered permissions mapped; fresh-Agent and per-call native task delivery proven. Child HITL routed and live-tested. Broader source syntax/fields, skill preload, MCP scope, removed-field replacement, resumed-child allow gates and native saved grants remain |
| Profile hooks | Canonical events, matching, handlers and failure policy | Extension lifecycle mapping remains | Plugin lifecycle mapping remains |
| Profile config | Capability patches and native escape | Patch materialization/field capabilities remain | Patch materialization/field capabilities remain |
| Runtime services | Injection and reports from actual observations | Bindings/context only; observed-report mapping remains | Bindings/context only; observed-report mapping remains |
| Inspect | Environment, models, defaults, quota, config and skills | Model/auth/config completeness and provider quota evidence remain | Model/default/auth/config completeness and provider quota evidence remain |
| Browser | Claude-specific Chrome integration | Equivalent real browser capability requires investigation | Equivalent real browser capability requires investigation |
| Unsupported policies | Claude lacks generic sandbox, web-search and approval Retry | Preserve truthful unsupported states for these baseline limitations | Preserve truthful unsupported states for these baseline limitations |
| Integrations | Public Runner bridges/hosttools, examples/docs | Re-run integration suites as capabilities change | Re-run integration suites as capabilities change |

OpenCode agent delivery evidence: `TestLiveOpenCodeProfileAgents` passed with
GPT 5.6 luna for durable fresh-Agent loading and invocation replacement combined
with primary-agent replacement instructions. Both returned a private proof word
through a successful native `task` tool result; the calling prompt did not
contain the proof. JSONC sync tests cover desired/observed separation, host key
versus runtime name, clone ownership, explicit clear, external edits and native
definition conflicts. Invocation tests cover persistent reuse, changed agents,
clear, unchanged durable files, and managed overrides before prompt delivery.
Model variants are checked against the resolved provider because OpenCode
silently ignores unknown agent variants. No dependency was added; native
rendering/validation stays in `opencode/`, reusing the JSONC ownership helper.
This closes basic agent configuration/delivery, not the complete agent row.

Child approval evidence: `TestLiveOpenCodeDelegatedApprovals` passed GPT 5.6
luna read, question, denied-write and cancellation cases. Read/question proofs
come from actual child tool execution; denied/cancelled writes leave no file.
Hermetic tests cover nested/parallel/resumed task routing, parent identity,
colliding native IDs, duplicate requests, core timeout/continue, partial child
cleanup and one terminal run. Per-agent gates are verified before prompting;
native deny precedence survives canonicalization and incompatible overlays fail
closed. Switching gated/ungated policy replaces the process without overlapping
writers. Tool snapshots use the declared tool-call-start contract, not synthetic
argument deltas. No dependency or new public execution API was added.

Remaining child boundary: under explicit Ask/auto-deny, native child-session
allow rules are rejected before approving a resumed task; support for safely
preserving/gating them is still open. Uncorrelated task requests are rejected.
Native project-level saved grants are now read-only checked and rejected before
explicit Ask/auto-deny prompts; safely mediating them remains open. Task routing follows the pinned
[native task protocol](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/tool/task.ts),
not inferred session names or recursively scanned JSON.

Child routing gates: Windows `go test -count=1 ./...`, `go vet ./...`, Linux
`go test -race -count=1 ./...` and 20-second server parser fuzzing passed.
The final four-case live suite passed in 60.11 seconds using the local Codex
endpoint and `codex/gpt-5.6-luna`. These gates validate this change; the remaining
acceptance inventory is still required for full Claude parity.

MCP startup guards: native sanitizer aliases, including UTF-16 astral characters,
are rejected before launch or synchronization. Resolved config must keep every
replaced/cleared slot disabled before API installation. The double-gated
`TestLiveOpenCodeManagedMCPGuard` passed with an isolated native managed-config
directory and observed zero requests to the re-enabled endpoint. Per-tool name
normalization collisions remain an upstream/native MCP audit item.

## Protocol evidence

Agent-source evidence: `TestLiveProviderAgentSources` passed all four Pi/OpenCode
durable-clone/invocation cases with `codex/gpt-5.6-luna` in 85.10 seconds. Each
durable case synchronized an imported Markdown agent, removed its source,
cloned its settings and ran a fresh Agent. Both providers produced an actual
child `read` tool result and delegation result containing a proof known only
from the file. OpenCode's source included overlapping permission patterns whose
order controls whether that read is allowed. No key was stored in repository
fixtures or manifests.

Sources are captured before profile initialization, and Thread tests show
replacement on actual resolved content changes even with a constant supplied
SourceFingerprint. Hermetic tests cover invalid shapes before initialization,
deleted sources, host/runtime identity, literal templates, source-independent
clones, clear and ownership protection. The shared JSONC reconciler now has a
Driver-selected ordered mode: OpenCode agent objects retain permission order
through writes, reads, cloning, hashing and startup verification. Reordered
external rules cannot be silently treated as unchanged SDK-owned content.

Source import is explicitly bounded: strict JSON-compatible YAML, known fields,
enabled subagents and no inline role/configuration fields alongside SourcePath.
Aliases/merges, permissive malformed-YAML fallback, and OpenCode's automatic
unknown-field promotion remain unsupported. Pi's imported body uses the SDK's
existing child instruction replacement semantics; the official example's
additive-prompt behavior is not implicitly claimed. This evidence closes the
implemented import contract, not complete native source compatibility or the
remaining agent parity row.

Dependency choice: existing `go.yaml.in/yaml/v4 v4.0.0-rc.2` is promoted from
indirect to direct. Its order-preserving syntax tree handles real YAML quoting,
block scalars and collections more reliably than a handwritten parser. The
[YAML organization upstream](https://github.com/yaml/go-yaml) maintains source,
tests, releases and issue tracking; this uses the repository's existing pinned
version/checksum. It is confined to `internal/agentmarkdown`, with provider
semantics in Pi/OpenCode and no exported dependency types. No additional module
was introduced. The helper reads source syntax only and never interprets
provider process output, events or checkpoints.

Source-import gates: full Windows `go test -count=1 ./...`, `go vet ./...` and
Linux `go test -race -count=1 ./...` passed. The new Markdown parser passed
20 seconds of fuzzing (over two million executions). The tests and live proofs
validate this change; the open acceptance inventory still applies. Source import
and Thread drift regressions passed three consecutive runs, including rejection
of null Pi tools, invalid deprecated OpenCode fields and mixed source/inline
declarations before profile initialization.

A repeated Linux race gate exposed an existing test hang in
`TestThreadLeaseRenewalKeepsLongRunExclusive`: after its real-time short lease
stopped renewing, the test waited forever on the success-only renewal channel,
even though all run goroutines had exited. The test now uses the standard
library's `testing/synctest` clock, observes early run termination, and bounds
the wait. It still proves that actual renewal preserves exclusivity past the
initial TTL. Production lease behavior is unchanged.
The corrected test passed 100 consecutive Windows runs. Final full Windows
tests, vet and Linux full race tests passed after this correction and the
source-field validation hardening.

OpenCode permission reset evidence: the official
[`PATCH /session` handler](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/server/routes/instance/session.ts)
appends rules. The old fake server and empty-array reset did not model this
behavior. The Driver now appends a complete resolved last-match segment, verifies
returned native state and preserves the checkpoint's original baseline. The fake
server persists and appends rules across process replacements and forks.
Repeated unchanged policies do not grow the rule list. A native four-round
Ask/auto-approve/Ask/auto-approve test preserved the same conversation, including
WithSpawn, and checked official SSE to prove no stale native read request remained.

The saved-grant audit confirmed that OpenCode evaluates project grants after
agent/session rules. Its public permission endpoint only lists pending requests;
the declared plugin permission hook is not called by this implementation, and
Pure mode disables external plugins. The Driver now uses the official read-only
[`db` query command](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/cli/cmd/db.ts)
around initialization of the private process's native permission cache. Nonempty
saved rules and unverifiable probes fail before prompting. It does not rewrite
the permission table or create a parallel approval policy. Supporting those
profiles with host mediation remains an open acceptance item.
`TestLiveOpenCodeSavedPermissionGuard` seeded grants through OpenCode's own legacy
migration, verified Ask and auto-deny were rejected before a write, and confirmed
the grant row remained unchanged. It passed in 8.26 seconds; the final four-round
permission reset test with these probes passed in 72.84 seconds. No dependency
was added. Query stdout/stderr remains in Raw audit data.
Final permission gates passed: Windows `go test -count=1 ./...`, `go vet ./...`,
Linux `go test -race -count=1 ./...`, and three consecutive runs of the new
permission restoration/cache/probe regressions. The audit now distinguishes
verified pre-prompt rejection from full saved-grant mediation; only the former
is implemented.

Pi agent resource evidence: `TestLivePiAgentResources` passed durable fresh-Agent
and invocation-override cases with GPT 5.6 luna in 29.24 seconds. The parent prompt
contains neither proof word; the native child combines full preloaded skill
instructions with an actual selected MCP tool result. Native MCP initialization
counts prove that only the parent connects to the excluded server. Hermetic
tests additionally cover empty inherited scope, unavailable resources, invalid
skill files, key/runtime-name ambiguity, immutable captured bytes and changed
preload content replacing the Thread session. The official
[`before_agent_start` extension event](https://github.com/earendil-works/pi/blob/v0.86.1/packages/coding-agent/docs/extensions.md#before_agent_start)
delivers full content with source paths; no new dependency or execution API was
introduced. Nonempty MCP lists select resolved keys; empty lists inherit.
Native skill discovery remains independent of explicit additive preloading.
Resource gates: Windows `go test -count=1 ./...`, `go vet ./...`, and Linux
`go test -race -count=1 ./...` passed after this change. These results close the
documented Pi skill/MCP field mappings; the remaining inventory is still open.

Pi child evidence: `TestLivePiProfileAgents` passed durable fresh-Agent loading
and invocation replacement with GPT 5.6 luna in 26.16 seconds. The child alone
received the private proof word through its system instructions; the parent
obtained it through the real registered tool. `TestLivePiDelegatedApprovals`
passed child read, question, denied-write and cancellation in 63.51 seconds.
Children reuse Driver-local native transport/parser code, without another public
execution API or Driver dispatch. Core still owns approval policy and run events.
Each child's official terminal/streams and usage are retained, and the parent
terminal waits for child teardown. The official Pi example supports this process
pattern; no new dependency was introduced. Advanced agent fields in the inventory
remain required before full Claude parity can be claimed.

Pi child gates: Windows `go test -count=1 ./...`, `go vet ./...`, and Linux
`go test -race -count=1 ./...` passed. Parallel child execution, duplicate and
mismatched tool authority, partial failure, cancellation, terminal usage and
raw/transcript preservation have hermetic regressions. The tool-policy extension
is executed by Node against allowed, denied, MCP and external-tool names.
Agent ownership/clone/clear and process-drift regressions passed three runs.
Final model-inheritance checks use the parent's officially parsed model/provider,
including native changes after construction. The final full test/Linux race gates
passed again, 30-second Pi parser fuzzing passed, and the final durable/invocation
live suite passed in 23.40 seconds. Static checks passed for the repository and
again for Pi after the final parser observation change.

Pi 0.86.1 ships `docs/rpc.md`, `docs/extensions.md` and
`dist/modes/json-event.js`. Tool start/delta/end fields are explicit; `tool_call`
can block before execution. This version's CLI exits before prompting when an
extension fails to load. RPC supplies request correlation, `get_state`, session
switching/branch duplication, abort and extension UI requests. A persistent
implementation must preserve Thread leases and the single-writer contract.

OpenCode 1.14.30 is the local validation target. The official server exposes
session/message APIs, an OpenAPI document and permission/question/event APIs.
V2 documentation describes a different API; implementation must use the schema
of the installed version. References: [Pi RPC](https://pi.dev/docs/latest/rpc),
[Pi extensions](https://pi.dev/docs/latest/extensions),
[OpenCode server](https://dev.opencode.ai/docs/server/).

## Verification gates

New capability declarations require meaningful hermetic tests and real-provider
execution evidence where mocks cannot prove delivery. Paid tests keep both
explicit environment gates. Local tests use the Codex-compatible endpoint with
`gpt-5.6-luna`; credentials remain outside the repository.

Completion requires full test/vet/Linux race gates, parser fuzzing, six
adaptertest suites, repeated Thread/checkpoint/approval tests, current docs and
examples, and a row-by-row audit of this inventory. No publishing or tag action
is authorized by this workstream.

Dependency choice for Pi approvals: no new dependency. The existing MCP SDK,
standard HTTP library and core approval sink cover transport and policy;
provider extension/tool semantics remain in `pi/`.

Dependency choice for Pi RPC: the standard JSON and process libraries, the
existing raw process helper and the official JSONL command/event contract.
No separate provider SDK is needed for the small correlated-command surface;
all semantic parsing and managed session coordination stay in `pi/`.

Dependency choice for OpenCode server: standard `net/http` and a small SSE
framer, with explicit types pinned to the official 1.14.30 schema. The small
Driver-local surface keeps authentication, event framing and cancellation
auditable, uses maintained standard-library networking, and confines all
provider interpretation to `opencode/`. No new top-level require.

Profile SPI audit: the earlier internal-only ProfileResourceDriver signature
violated the extension boundary. The interface and snapshot types now belong to
`driver/`; root API declarations are unchanged. PRO-01/PRO-02 run for all six
built-ins. They reopened and fixed a read-only violation: GetProfile, skill
inspection and ProfileState had initialized or cloned profiles in the original
four Drivers. Cloning now remains on explicit synchronization/execution paths.
The previous clone tests were changed to assert the read/sync boundary.

Durable skill evidence: TestLiveProviderProfileSkills passed for Pi and OpenCode
with gpt-5.6-luna. It closes the syncing Agent and uses a fresh Agent without
WithSkills; the native skill/read tool returns a proof word only present in the
installed SKILL.md. Profile state alone is not execution evidence.

Profile/skills gate results: full Windows `go test -count=1 ./...`,
`go vet ./...`, and Linux `go test -race -count=1 ./...` passed after these
changes. Windows orphaned junctions, missing skill targets, linked content
edits, ownership preservation and persistent-writer replacement are covered.
Remaining rows above are still open; durable skill completion is not full
Claude parity.

Native instruction evidence: TestLiveProviderProfileInstructions passed with
GPT 5.6 luna for Pi additive/replacement and OpenCode additive profile files.
Each case closes the syncing Agent and invokes a fresh one with no instruction
option. Contract tests cover desired/observed state, renamed bundles, explicit
clear, undeclared preservation, external edits, symlinks, manifest path escapes,
Windows sharing violations with rollback, invalid declarations before profile
initialization, and repeated persistent-process replacement on native file drift.
No dependency was added; file locking and atomic writes use the existing
profilestate implementation, while native filenames stay in each Driver.

Replacement evidence: OpenCode 1.14.30's official
[LLM implementation](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/opencode/src/session/llm.ts)
uses `agent.prompt` instead of its provider preamble and then appends native
context and per-message system content. Invocation replacement now maps to this
field after native agent discovery, with bounded process replacement and a
before-prompt check that all other resolved settings remain equivalent. Pure
mode is supported; no plugin is required. Native skill discovery can reorder
adjacent same-action permissions, which are compared without erasing action
precedence. Tests cover custom/default agent preservation, literal config
templates, empty file replacement, malformed/unknown agents, failed delivery,
no replay after prompt, raw audit, process reuse and drift. The double-gated
TestLiveOpenCodeReplacementInstructions passed with GPT 5.6 luna for explicit
and default custom agents across Run/Stream turns. Durable replacement remains
open. Persistent project/local scope also remains open; profile synchronization
currently lacks a resolved invocation workspace. No dependency was added.

Native instruction gates before invocation replacement: full Windows
`go test -count=1 ./...`, `go vet ./...`, and Linux
`go test -race -count=1 ./...` passed, including source I/O error propagation.

Invocation replacement gates: full Windows `go test -count=1 ./...`,
`go vet ./...`, and Linux `go test -race -count=1 ./...` passed. The live
default/explicit-agent replacement suite passed in 15.99 seconds with
`codex/gpt-5.6-luna`. These results close invocation replacement only; the
remaining inventory is still open.

Durable MCP evidence: TestLiveProviderDurableMCP passed all six Pi/OpenCode
stdio/HTTP/SSE cases with GPT 5.6 luna in 59.07 seconds. Each case closes the
syncing Agent, creates a fresh Agent without WithMCP, calls a real MCP tool for
an otherwise unknown proof word, and verifies per-call clear. Remote servers
check actual bearer authorization; persisted config/metadata contains only the
environment reference. Native required-connection failure is tested before
prompt delivery. Repeated tests verify persistent-writer replacement on MCP
configuration and requirement changes.

The MCP declaration bit was missing from ProfileResourceDeclarations. It now
distinguishes undeclared from explicit clear in both Inspect/sync and the unified
invocation pipeline. It does not mark injected runtime-service/hosted-tool MCP as
a durable declaration. The original four Drivers' explicit sync paths also
preserve undeclared MCP, honor explicit shared-profile clear, and protect
externally edited or unverifiable legacy entries. Legacy ownership without a
matching rendered-content hash is not treated as proof of ownership. The Driver
SPI golden records the new field; the root public API remains unchanged.

Dependency choice for JSONC profile resources:
- `github.com/tailscale/hujson` is pinned to
  `v0.0.0-20260727124030-b80ff77dac4f`. Its syntax tree and JSON Patch support
  preserve comments, trailing commas and unrelated fields without hand-written
  JSONC tokenization. The reconciler additionally rejects duplicate keys and
  indirect native files, verifies content ownership and rolls back failed commits.
- The Tailscale-maintained upstream provides source, version history, tests and
  issue tracking. Go module checksums pin the reviewed dependency. Its Go 1.26
  requirement fits this repository's Go 1.26.5 baseline.
- Imports are confined to internal/profilejson, used by Driver profile resource
  delivery. Provider MCP rendering/interpretation remains in each Driver and no
  dependency type enters the root API or Driver SPI.

MCP cloning/layering evidence: `TestLiveProviderClonedMCP` passed all six
Pi/OpenCode transport cases with GPT 5.6 luna in 60.95 seconds. OpenCode copies
only selected MCP/settings fields; verified ownership and required policy are
retained with destination paths, without duplicating native secrets in metadata.
The two native config files are seeded as a group only when neither destination
file exists. Existing config precedence is preserved. Tests cover canceled lock
acquisition, malformed/indirect files, external ownership, exact numeric values,
exclusive file publication and failed manifest commit rollback on Windows.

OpenCode's native config uses deep merging. Invocation MCP delivery now disables
its native slots during startup and installs complete connection values through
[the official POST /mcp API](https://github.com/anomalyco/opencode/blob/v1.14.30/packages/sdk/js/src/v2/gen/sdk.gen.ts).
The response is the official server-keyed status map. Native GET /mcp status is
observed every turn; required failures stop before prompts, optional failures
emit an invocation notice. Unchanged Thread turns retain connections; changed
connection values replace the writer. No prompt replay is introduced.
`TestLiveOpenCodeMCPReplacement` passed in 15.32 seconds: changed HTTP addresses
and HTTP-to-stdio replacement never contact the old endpoint, inherit old
headers or expand literal templates. The native files remain unchanged after
seeding OpenCode's own first-load $schema migration. `TestLiveOpenCodePersistent`
also passed in 19.21 seconds with the new delivery path.

Durable JSON values preserve native string escapes; ownership distinguishes
escaped literals from active templates, while ignoring layout/object order.
Only explicit BearerTokenEnvVar references are rendered as active templates.
Clone output omits comments because comments may mention excluded settings.
No additional dependency was introduced for cloning or dynamic MCP delivery.

Combined replacement evidence: the HTTP case also passed with invocation-local
replacement instructions, including native agent discovery/restart, in 8.81
seconds. MCP literal escapes survive that restart. Structured config clones use
the same normalized source as generic profile resolution, including native
fallback and normalized explicit paths.

Clone/layering gates: full Windows `go test -count=1 ./...`, `go vet ./...`,
and Linux `go test -race -count=1 ./...` passed after the normalized-source
fix. The MCP/clone/JSONC regressions also passed three consecutive runs.
The subsequent startup-guard work above closes server-name normalization and
higher-precedence managed MCP overrides. Collisions among individual tool names,
including ambiguous server/tool concatenations, remain an MCP audit item.
Other open inventory rows remain required for full Claude parity.

Profile-agent/startup-guard gates: final Windows `go test -count=1 ./...`,
`go vet ./...`, and Linux `go test -race -count=1 ./...` passed, including finite
native numeric validation. Agent regressions also passed three consecutive runs.
The final live agent run passed in 22.71 seconds, and managed-MCP rejection
passed in 1.27 seconds. Credential/model setup remained local and unchanged;
no API key was copied into repository fixtures or ownership metadata.
