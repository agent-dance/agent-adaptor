# Multica-inspired host routing with real Codex

This standalone acceptance program uses the public SDK: one read-only Team
leader, two identity-bound workers (`alpha`, `beta`), named TeamThreads, typed
messages, a host queue, and private localhost MCP tools. It demonstrates a
small product mapping; it does **not** connect to a Multica server, migrate
Multica, or validate Multica's entire mention parser.

The reference is the official Multica source at commit
[`0d627e54e1bd4d2521359f293d04521246495ce2`](https://github.com/multica-ai/multica/tree/0d627e54e1bd4d2521359f293d04521246495ce2):
[comment routing](https://github.com/multica-ai/multica/blob/0d627e54e1bd4d2521359f293d04521246495ce2/server/internal/handler/comment.go),
[comment queue tests](https://github.com/multica-ai/multica/blob/0d627e54e1bd4d2521359f293d04521246495ce2/server/internal/handler/comment_thread_queue_test.go),
[comment steer tests](https://github.com/multica-ai/multica/blob/0d627e54e1bd4d2521359f293d04521246495ce2/server/internal/handler/comment_agent_steer_test.go),
and [supplement delivery](https://github.com/multica-ai/multica/blob/0d627e54e1bd4d2521359f293d04521246495ce2/server/internal/daemon/task_supplement.go).
Product terminology follows
[mentions](https://multica.ai/docs/mentioning-agents#steer-a-running-agent)
and [chat](https://multica.ai/docs/chat#send-messages-while-a-reply-is-in-progress).

## Run deliberately

The command refuses to run unless `-agent=codex`, `-model`, `-effort`, and
`-report` are all supplied. Tests are offline; there is no default fake run
that could be mistaken for live evidence. An explicit run uses your Codex
account and can incur model usage. Use an approved, CLI-compatible source
profile (including any required auth timestamp normalization) prepared outside
the program:

```powershell
go run ./examples/showcases/multica-routing `
  -agent=codex -model=gpt-6.1-sol -effort=xhigh `
  -codex-home=$env:CODEX_HOME `
  -report="$env:TEMP/multica-routing-report.json" `
  -timeout=5m
```

`-codex-command` can select a particular CLI binary. Each Agent gets its own
disposable profile through `profile.CloneFrom(CopySettings(), LinkAuth())` and
its own ThreadStore. Worker workspaces are temporary repositories. The leader
has a read-only sandbox; workers can write their own workspace. `host_gate` is
read-only, has a semantic revision, and binds its role in the host closure;
its input cannot assert another identity. No external host service is started.
Team and Agent close operations have individual deadlines. Workspaces and
profiles are removed after close; `-keep-workspaces` retains them explicitly.
Audit files never contain a copied auth file. Known auth/API-key values are
redacted from all JSON strings while preserving valid JSON.

## What the checks prove

| Mapping | Actual operation and evidence |
| --- | --- |
| Structured mention/comment | A host-owned envelope contains exact roster target, opaque ID, original text, and human author. Every input enters the leader. Only the leader delegates or `team_send`s to a worker. This example accepts structured targets, not free-text mention syntax. Unknown targets are rejected by the host before SDK delivery. |
| Running comment and duplicate | The host waits for alpha's real `host_gate` invocation, injects the same comment ID twice into the Team stream with `Soon`, and waits for the correlated leader `team_send` result before releasing alpha. The marker is absent from the initial objective. Checks use the single durable human receipt, worker peer-author delivery, exact worker identity, task state, and actual file bytes. |
| Start after this run | Two inputs stay in the **host** FIFO until authoritative `Done`; each starts a new `TeamThread.Send`, with a different RunID and unchanged envelope/ID/author. This is not SDK `AfterTurn`. |
| Stop and start over | Beta writes an artifact and enters its real gate. The host calls `Cancel`, drains authoritative `Done`, retains the cancelled task and available stdout/transcript, then starts a **new TeamThread** with exactly one successful beta task and a new artifact. The following ordinary run must retain that exact task without additional delegation. This is not SDK `Interrupt`. |
| Chat Steer | Combined with the previous case to reduce calls: cancel current reply, wait for Done, dispatch the oldest priority input before ordinary queued input. An undispatched input is withdrawn and never reaches the SDK. This queue is host policy, not a new SDK timing mode. |
| Isolation | Length-prefixed opaque keys prevent tuple collisions. Two real scopes reuse the same message ID independently; offline adversarial tests additionally exercise same-thread duplicates, conflicting duplicates, FIFO, priorities, withdrawal, authors, Unicode, and concurrent admission. |

There are six outer Team runs. A Team run may contain multiple actual provider
turns and worker calls. No fixed token or cost estimate is claimed.

## Evidence and failure behavior

The summary contains every check, actual Done observations, RunIDs, original
host inputs, complete `TaskStore.Tasks` snapshots, typed inbox messages, Team
artifacts, event envelopes, gate observations, timings, and close errors.
`-output` selects the audit directory (default: `<report>.artifacts`). Each run
also saves complete team and worker `Text`, `Decode` result/error, `Raw`,
provider terminal payload, `Transcript`, `Usage`, services, metadata, and
messages, plus workspace artifact contents and SHA-256 digests. The Team's
Raw/Transcript represent leader activity; worker audits are saved separately
from TaskStore rather than inferred from the leader's text.

Failures stay failures. Every delegated task in a successful run must have
actually succeeded; an artifact written before task failure cannot pass.
`RunError.Result` is extracted for partial audit
without clearing its error. Cancellation is an expected check in its original
run, while missing terminal events, incorrect receipts, wrong artifacts,
schema failure, timeouts, or cleanup failures fail the overall summary and
exit status. No check is replaced with a leader assertion. This program has
not itself been presented as a passing live run; retain the resulting report
when executing it.

```powershell
go test ./examples/showcases/multica-routing
go vet ./examples/showcases/multica-routing
```
