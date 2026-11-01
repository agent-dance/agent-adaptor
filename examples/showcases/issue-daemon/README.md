# Issue daemon acceptance scenario

This executable models multica issue comments, continuity, chat and squad
routing from proposal §8–§9. It exercises the public SDK; it does not implement
a daemon server or modify library packages.

```sh
# Offline; never invokes any agent CLI.
go run ./examples/showcases/issue-daemon -agent=fake -report=report.json

# Paid inference through an installed and authenticated local Codex CLI.
# Run this explicitly outside ordinary CI.
go run ./examples/showcases/issue-daemon -agent=codex -effort=low -report=report.json
```

Codex mode checks PATH before starting. Every Codex Agent has an explicit
`codex.Config{ReasoningEffort: "low"}`. Other effort values are rejected.
`-timeout=3m` bounds each independent scenario group. `-report=-` writes JSON to
stdout; diagnostics go to stderr. Exit codes are 0 for all checks passed, 1
for a report containing failed checks, and 2 for command/setup/report-write
errors. A failing check does not suppress later independent scenarios.

Each disposable workspace contains a tiny Go module and an empty Git repository.
No git command mutates the checkout. Steps 1–7 and 10 deliberately share one
workspace, Thread key and store so file effects and conversation continuity
can be compared. The other independent groups each use a separate workspace;
team members share only their group's workspace. `-keep-workspaces` retains
these directories (their paths appear in the report). Live mode copies local
Codex auth/config files into a private profile in each workspace; retained
workspaces therefore contain credentials. Reports never include capability
tokens or profile contents.

Cleanup retries Windows sharing violations for at most two seconds after
closing the Agents. A persistent lock or any other filesystem error still
fails the cleanup check.

The host releases bounded shell waits through `.release-*` files after it has
sent the required inputs. The scripted Driver uses channels for those same
tool-completion points; tests use no sleeps for synchronization. Interrupt uses
a deliberately long pair of shell sleeps, expecting completion within 60 seconds
of the interrupt. The fake implements the message SPI rather than generating
public MessageUpdate, TurnEnd or Done events: core must produce them.

The report contains all 15 numbered steps:

| Step | Acceptance |
|---|---|
| 1 | Issue task and first tool event |
| 2 | Human comment admission/delivery and file effect |
| 3 | Duplicate MessageID has one provider delivery |
| 4 | Peer delivery, file effect and transcript authorship |
| 5 | AfterTurn continuation, TurnEnd count and Result.Turns |
| 6 | Done/Result object, Text, Turns, Usage and error identity |
| 7 | Post-Done rejection retains text, then follow-up continuity |
| 8 | Task-mode busy Thread rejects a second task |
| 9 | Native interrupt mechanism, final token and elapsed time |
| 10 | New Agent over the same store/workspace, resume and core.dedupe |
| 11 | Cancel returns pending AfterTurn text and cancelled Done |
| 12 | MaxTurns=2, approaching notice and two limit_exceeded outcomes |
| 13 | Subscribe-first Standby, joined turns, MessageIDs, Cancel and reuse |
| 14 | Two delegations, idle-leader host wake, completion wake, Usage, files and four tools |
| 15 | Team Standby routing, joined messages, Cancel and subsequent execution |

Step 14 also asserts the task-mode Team busy contract, replacing the original
standalone reproduction. Team tool-count auditing makes an authenticated
`tools/list` call directly to the actual `Team.ServeHTTP` handler using the
leader invocation's capability. It counts the returned tools; it does not trust
a model's assertion. The offline leader delegates through that same handler.
The live leader Driver forwards Codex's session, message, profile and streaming
contracts unchanged while performing this audit.

In step 15 a leader's message-associated turn can report delegation progress.
Each reply must appear exactly in that turn or as a complete line in the
successful final answer after the workers finish. The report retains both
texts and still checks message IDs, turn order and shared RunID independently.

`started`/`ended` are UTC wall-clock timestamps. Every host call records exact
text, IDs, authors and timing; checks retain expected and observed values.
The compact event timeline records receive order, provider-independent event
type, RunID/sequence, message state/boundary/mechanism/reason, TurnEnd excerpts
and MessageIDs, task/member/state, Notice kind and Done error class. Failed
message text is retained in full. Large raw stdout/text deltas are represented
by their event type instead of copied into the report. Shared-execution checks
may finish after a later step releases the tool gate; timestamps reflect that.

Fake native mechanisms are named `fake.turn_steer` and `fake.turn_interrupt`;
live checks require `codex.turn_steer` and `codex.turn_interrupt`. Core resume
is reported as `core.resume`. The report preserves whichever mechanism actually
occurred, including an unexpected one as a failed check.

The restart step recreates the Agent over the same injected in-memory store;
it tests SDK reconstruction and provider resume, not process-kill durability
of a production database. A real daemon must supply a durable store and resend
unacknowledged comments using their original IDs. This scenario does not claim
to implement that host database.

Run the offline contract test:

```sh
GOPROXY=off /home/cc-sandbox/toolchains/bin/go test -count=1 -v ./examples/showcases/issue-daemon/...
```

The public `team.New` requires a loopback listener. If a sandbox denies that
socket, steps 14–15 remain **failed environment checks in JSON**; only those
test subtests explicitly skip. No Team acceptance is claimed in that case.
Other failed contracts fail the test normally, even if library fixes are pending.
No live Codex call is made by the tests.

Steps 12 and 14 retain regressions for the corrected turn-limit warning and
task-mode Team busy behavior. A two-turn budget must emit `limit.approaching`
before the final turn; a second task-mode send must return `ErrThreadBusy`.
