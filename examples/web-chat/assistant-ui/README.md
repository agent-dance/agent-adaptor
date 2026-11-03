# Direct assistant-ui chat

A local, single-process example of `HttpAgent` + `useAgUiRuntime` talking to
`bridges/agui/aguisession`. Fake mode is deterministic and needs no credentials,
model calls or shell commands. It runs through the real Agent/Thread pipeline:
turn 1 emits reasoning, markdown and 13 simulated shell calls (13 fails with exit
code 1); turn 2 reports that it resumed checkpoint 1. Turn 3 after a server restart
resumes checkpoint 2. The failed shell call does not make the overall demo run fail.

From the repository root (Go 1.26+, Node 20.19+):

```bash
# Enable read-only joining endpoints; execution still cancels on disconnect.
go run ./examples/web-chat/assistant-ui -observation -data-dir=.local/assistant-ui/demo
```

In another terminal:

```bash
cd examples/web-chat/assistant-ui/web
npm ci
npm run dev
```

Open the URL printed by Vite (normally http://127.0.0.1:5173). Its proxy targets
127.0.0.1:8092. Send two messages, expand the commands and inspect command 13, then
reload. Its failure badge and result remain visible. The collapsed group also
indicates a failed command. The URL's `?thread=` identifies the conversation;
New thread allocates a new ID. Without a query parameter the tab's sessionStorage
is used, then the ID is written into the URL.

To demonstrate refresh while running, restart the Go command with:

```bash
go run ./examples/web-chat/assistant-ui -observation -unattended -pace=100ms \
  -data-dir=.local/assistant-ui/demo
```

Send on a new thread and reload while commands are running. Snapshot supplies
completed history and the active boundary; Observe reconstructs that entire run
from its cursor. The active user is loaded from RUN_STARTED.input. Disconnecting
the browser leaves unattended execution running. Cancel run on host calls the
cancel endpoint, which waits for drain/recording cleanup. In request-scoped mode
the ordinary assistant-ui Cancel aborts the POST. After cancellation or another
failed/incomplete run, allocate a new thread: the old healthy checkpoint cannot
attest the newly recorded partial turn (`agui.history_unrecoverable`). Errors,
including this code, 409 divergence, duplicate run and active run, appear in the
banner. Cancelled runs display a separate “Run cancelled” status live and after
reload; the process-exit diagnostic remains in ordered events. Reload reads those
events to recover the latest terminal outcome.

## In-run guidance

Restart with a visible pace (`-observation -pace=300ms`) and send a message.
While it runs, the page shows **Add guidance while the agent works**. Guidance
is POSTed to `/message` (`aguisession.Session.MessageHandler`) and calls the
active run's `Stream.Send`; it never starts another run, and a second chat
message sent with the composer while a run is active is rejected with 409
`agui.run_active`. The panel shows "Accepted · waiting for delivery", then
"Delivered" from the run's `message.update` events. The fake driver has no
mid-turn delivery boundary, so the SDK delivers guidance at the end of the turn
by resuming the checkpoint inside the same execution: a "Turn 2 continues…"
answer follows. When the run ends the page imports `/history`, which shows the
guidance as a user message between the two turns, exactly as after a reload.

## Human approval

Send a message containing "approval" (for example "deploy with approval"). The
fake driver asks for a Permission decision before a simulated deploy. The
decision appears as a `dec.permission.shell` tool call with **Approve** and
**Deny** buttons. Approve lets the run finish; Deny follows the SDK default
`OnReject` action and ends the run with `RUN_ERROR approval_denied`. Answers go
to the host's own `/decision/resolve` route ([`approvals.go`](approvals.go)),
which keeps the live `ApprovalRequest` captured through
`aguisession.Options.Observe` and calls exactly one of Approve/Deny/Answer;
repeated or late answers return 410 `approval.resolved`. Who may decide is host
policy: the example authorizes it through the same resolver as in-run input
(`OpMessage`). The default approval timeout is 30 seconds.

## Real CLI smoke (explicit opt-in)

Install and authenticate the CLI separately. These commands can execute tools
and incur model charges. The host chooses the provider; the SDK does not route.
Use a **different data directory** for each provider/configuration/workspace:

```bash
AGUI_AGENT=codex AGUI_MODEL=gpt-6.1-sol go run ./examples/web-chat/assistant-ui \
  -observation -unattended -data-dir=.local/assistant-ui/codex
# AGUI_AGENT=claude|cursor|codebuddy is also supported via exampleutil.
# CODEX_COMMAND/CLAUDE_COMMAND/... and provider *_MODEL variables also work.
```

Real mode explicitly reuses the operator's CLI profile through
`exampleutil.OperatorProfileEnv`: Config.Env receives HOME (and USERPROFILE when
set), plus CODEX_HOME from the environment or HOME/.codex for Codex. Your CLI
login and custom-provider config.toml are used. The helper points the Driver at
that profile; it does not copy profile credentials. To select another existing Codex profile:

```bash
CODEX_HOME=/path/to/operator/codex-profile AGUI_AGENT=codex AGUI_MODEL=gpt-6.1-sol \
  go run ./examples/web-chat/assistant-ui -observation -unattended \
  -data-dir=.local/assistant-ui/codex-custom
```

This explicit example choice avoids the 401 caused by expecting the SDK's
managed CODEX_HOME default to read your CLI login. Driver defaults are unchanged.
Claude's CLAUDE_CONFIG_DIR and Cursor's CURSOR_HOME overrides are captured when
provided. Changing profile bindings changes Thread compatibility; use a fresh
Thread/data directory when switching CLI profiles.

Ask for a short answer, then ask what you just requested. Reload, restart with
the same working directory/configuration/data directory, and ask again. Confirm
continued context and history. A real CLI is not expected to produce the scripted
13-command trace. The example uses `exampleutil.NonInteractive` with WorkspaceWrite;
production approval/grant policy belongs to the host.

## Configuration and endpoints

Flags override environment defaults. Listen defaults to **127.0.0.1:8092**;
CORS is disabled. Vite's development proxy avoids CORS entirely. For a separately
served browser use `-cors-origin=http://localhost:5173` (one exact origin).
The UI uses relative paths; deploy its built assets behind the same origin or
configure a reverse proxy. `vite preview` alone does not provide the API.

| Flag | Environment | Default |
| --- | --- | --- |
| `-addr` | `ADDR` | `127.0.0.1:8092` |
| `-data-dir` | `AGUI_DATA_DIR` | `.local/assistant-ui` relative to Go working directory |
| `-agent` | `AGUI_AGENT` | `fake` |
| `-pace` | `AGUI_PACE` | `0s` (per fake event) |
| `-observation` | `AGUI_OBSERVATION` | `false` |
| `-unattended` | `AGUI_UNATTENDED` | `false` |
| `-cors-origin` | `CORS_ORIGIN` | empty |

The page needs `-observation` for its Snapshot-based history adapter. This does
not enable unattended execution; the two switches are independent.

| Endpoint | Contract |
| --- | --- |
| `POST /agent` | AG-UI 1.0 RunAgentInput → SSE; `HttpAgent` supplies complete messages |
| `POST /message` | `{threadId,messageId,text}` in-run guidance → 202 `{threadId,runId,messageId,state:"accepted"}`; 409 without an active run |
| `GET /decision/pending?thread_id=T` | Live approval requests awaiting an answer |
| `POST /decision/resolve` | `{threadId,requestId,result:"approve"\|"deny"\|"answer",text?,choice?}` → 204; 410 when already resolved |
| `GET /history?thread_id=T` | Materialized AG-UI Message[] |
| `GET /events?thread_id=T&after=N` | Ordered projected records (`host_seq`, `events`) for exact interleaving |
| `GET /snapshot?thread_id=T` | `{messages,cursor,activeRun}`; requires `-observation` |
| `GET /observe?thread_id=T&after=N` | Catch-up + follow SSE; requires `-observation` |
| `POST /cancel?thread_id=T` | Host cancellation and cleanup (204); requires `-observation` |
| `GET /health` | Public `mode` (`fake`/`real`), `agent` name and unattended/observation flags; no paths |

No Last-Event-ID or POST replay is used. Snapshot's cursor is a **record** cursor:
one HostSeq can produce several wire events. The page always joins from a full
run boundary; it never retries from an arbitrary partial SSE frame. An interrupted
join requires a fresh Snapshot. Observation-only seq metadata is not conversation
content and is removed when comparing with materialized history.

## The necessary front-end glue

[`web/src/session.ts`](web/src/session.ts) is the complete history/observation
adapter. It does not implement an AG-UI parser or reducer:

- `load()` fetches Snapshot and calls
  `ExportedMessageRepository.fromArray(fromAgUiMessages(snapshot.messages))`.
  An active run sets `unstable_resume`. Before returning it buffers RUN_STARTED's
  user seed, because the snapshot deliberately excludes the in-flight turn.
- `resume()` redirects an official HttpAgent to the GET Observe endpoint from
  that cursor, using the runtime's abort signal. HttpAgent folds AG-UI events;
  `fromAgUiMessages` converts the active assistant/tool messages to content
  parts yielded as assistant-ui run updates. A single coalescing mailbox bounds
  buffering. Terminal failure/cancellation becomes an incomplete status.
- assistant-ui 0.1.1's resume contract updates one assistant message. After it
  settles, `App.tsx` imports canonical history while idle to restore all wire
  message IDs before the next send. This also preserves reasoning/tool owners
  for real drivers that emit multiple assistant messages.
- `agent.subscribe` reads TOOL_CALL_RESULT.metadata.agentAdaptor.status/exitCode.
  `Shell` uses that live map and, after reload, `isError` from ToolMessage.error.
  Bare HttpAgent does not set ToolMessage.error from live result metadata.
- `withoutUserEchoes` wraps the page's live POST stream. assistant-ui 0.1.1
  folds a live run into assistant messages, so the run's echo of in-run
  guidance (a user-role text message) would render as assistant output. The glue
  drops only user-role text lifecycles from that SSE body; the guidance panel
  tracks status through `message.update`, and the canonical `/history` import
  after the run shows the guidance in place. Bare HttpAgent folds the echo
  correctly and needs no glue.
- `Decision` renders `dec.*` tool calls: Approve/Deny while the result is
  missing, the outcome afterwards. A pending decision keeps its tool group open.
- `checkedFetch` preserves HTTP status and structured error codes in the banner;
  an idle load consults the ordered event view for terminal outcomes, which
  Message[] alone cannot represent. `append()` is intentionally a no-op: the
  server's one drain records both sides of the turn.

Reasoning is optional during next-turn history verification: assistant-ui 0.1.1
omits empty plain reasoning on export, and showThinking=false can omit all plain
reasoning. The server still checks user/assistant/tool IDs and order. Source
text/reasoning START/END pairs with no content are never projected, live or on
history reads. This supports both thinking settings without loosening the
execution transcript check for required message roles.

`App.tsx` supplies product renderers: markdown, collapsible reasoning, adjacent
commands grouped into one details element, failure status, and generic data
parts for known protocol names. Unknown CUSTOM/data names are suppressed, so
operational payloads cannot become arbitrary top-level blocks. Default bridge
projection also filters invocation/runtime/transcript/provider pass-through
notices; explicit `WithNoticePassthrough` is available to a host that needs
reviewed/redacted extras. Protocol diagnostics remain available in ordered event
history. Turn 1 also projects a simulated command-review activity as an
ACTIVITY_SNAPSHOT plus ACTIVITY_DELTA. `Activity` renders the allowlisted
`agui-activity/command-review` data part, including its completion and text;
content.parentToolCallId retains the scoped command-13 tool ID. The pinned
converter attaches it to the preceding assistant/tool owner, identically live
and after reload. This card reports scripted demo content, not delegated model
work. Other Activity/card renderers still belong to a product. Its export omits
raw activity messages from the next-turn input; server history keeps them.
assistant-ui may combine adjacent reasoning
and assistant/tool records into one visible bubble; wire IDs, tool associations
and ordered process history remain available. The page reads /health for
mode-appropriate copy; real CLI mode does not describe the fake 13-command scenario.

## What the host still implements

`demoResolve` **permits everyone** and maps IDs with `aguisession.ThreadKey`.
Replace it with authenticated principal and per-operation grants for start,
read, follow, cancel and in-run message/decision; encode tenant/user/business dimensions without collisions.
The demo default remains permissive. `TestAuthenticatedScopedThreadSurvivesReconstruction`
injects a test-only resolver using a verified-principal stand-in header, an
allowlist for every operation and an opaque hash of a JSON tenant/principal/Thread
tuple. It reconstructs the persistent store between authenticated turns and
checks 403 for other principals on start, read, follow and cancel. The test header
is not a production authentication mechanism. Production also owns:

- Durable backends, secrets, retention, redaction, backup and migration.
- Worker lifetime, concurrency limits, routing, revocation and cancellation.
  On grant changes call Session.Revoke and update Resolve; no public revoke
  endpoint is exposed by this permissive demo.
- Domain facts, approval grants, business decisions, custom activities and their UI.

The injected JSONL recorder (`events/`) syncs each append. The injected Store
(`checkpoints/threads.json`) persists records via fsync + atomic rename and
process-local fenced leases. On POSIX the store also fsyncs the directory after
replacement and fails closed on sync errors. On native Windows it skips directory
fsync: a read-only directory handle cannot portably FlushFileBuffers. File data
is still synced before replacement, but directory-entry persistence across power
loss is not guaranteed by this example. The Windows-tagged replacement/reopen
test exercises successive saves without poisoning the store. Both backends are
**single-process**. Stop the old process before opening the same directory; there is no cross-process writer lock.
A storage failure is returned, never replaced by memory. After an abrupt crash,
a run lacking a durable terminal is diagnosed incomplete; checkpoint and event
storage are separate, so an uncertain/failed recorded turn fails closed on
continuation. SIGINT/SIGTERM shuts down the Session, then Agent/recorder and HTTP.

## Checks and dependency decisions

```bash
# From repo root; no sockets needed:
go test -count=1 ./examples/web-chat/assistant-ui ./internal/aguiversion
go vet ./...
cd examples/web-chat/assistant-ui/web
npm run test:offline
npm run build
# Requires loopback sockets; builds Go once, restarts the same binary:
npm run test:integration
```

Integration tests use only fake mode and a temporary data directory/free port;
they make no model calls. `AGUI_TEST_NO_SOCKETS=1 npm run test:integration` explicitly
skips the HTTP test while retaining the offline checks. The existing Go golden
fixtures and standalone `node bridges/agui/testdata/verify-client.mjs` remain
usable and are included in this test command.

All npm dependencies are pinned to exact versions, including
**@ag-ui/client@1.0.3** in both manifest and lockfile; use `npm ci` on a fresh
checkout. This page is the repository's only web front end (the earlier
CopilotKit and plain `@ag-ui/client` examples were removed), and
`internal/aguiversion` guards its pins against the Go AG-UI module pin.
`npm audit --omit=dev --audit-level=high` runs in CI.

## What is verified automatically

- `TestScriptedThreadSurvivesReconstruction` proves checkpoint/history persistence
  and continued third turns; `TestSessionCancellationOverridesDriverFailure`
  covers host cancel, shutdown, request disconnect and an independent failure.
- `TestRunCancellationReturnsPartialRawWithoutCheckpoint`,
  `TestPersistentCodexCloseCancelAndIdleReapProcesses`, and
  `TestCodexBatchRunPreservesProcessOutcomeAndCancellation` use fake processes
  to check cancellation classification and checkpoint safety in Codex transports.
- The HTTP official-client integration test validates raw AG-UI frames, public
  identities, two thinking-settings reloads, process restart, observation cursor
  delivery, cancellation, duplicate requests and history divergence.
- Offline Node tests exercise the actual history adapter and renderers, sanitized
  real Codex history, failed tools, cancelled status, partial observation EOF,
  the live echo filter and guidance receipts.
- The in-run guidance integration test checks 202 admission, `message.update`
  accepted→delivered, the live user echo, live/history equality, the next turn's
  verification, and 409 for inactive runs and reused IDs.
- `TestApprovalRoundTripThroughCompanionEndpoint` approves and denies through the
  companion endpoint; `aguisession` tests cover supplement ordering before Done,
  concurrent admission racing run end, recording failure and an approval
  requested before any event is recorded.

Run `npm run test:integration` for the complete client suite or `npm run test:offline`
for the checks that do not start an HTTP server. Go and Node must be on PATH.
