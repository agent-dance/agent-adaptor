# SDLC Team with independent host acceptance

This runnable showcase uses `hosttools/team.New`, one real Codex leader and four
real Codex members. It builds a deliberately small standard-library Go package:
UTF-8-safe `@mention` extraction and comment routing. It does not implement a
production issue tracker or Multica service.

The workflow is:

```text
planner → host plan checkpoint → engineer → frozen candidate
                                            ├─ reviewer ─┐
                                            └─ verifier ┤
                                                       ↓
                                               host acceptance gate
                                                       ↓
                                                    integrate
```

Only the leader schedules tasks, using the actual Team tools. All four members
use `PerTask`; each task is independent. A rejecting review or failing host check
permits at most two engineer reworks. Every revised candidate needs a fresh
reviewer and verifier. The SDK is responsible for execution, tools, typed events,
cancellation and task persistence; all SDLC rules live in this example.

## Run

From the repository root, the default invocation is offline and makes no model
calls:

```sh
go run ./examples/showcases/sdlc-team
go test -count=1 ./examples/showcases/sdlc-team
go vet ./examples/showcases/sdlc-team
go test -race -count=10 ./examples/showcases/sdlc-team
```

Live execution requires a Codex CLI, an authenticated external `CODEX_HOME`, Go
on `PATH`, and explicit paid-run flags. The report directory must not exist.
For example, in PowerShell:

```powershell
$env:CODEX_HOME = 'C:\approved\normalized-codex-profile'
go run ./examples/showcases/sdlc-team `
  -agent=codex -model=gpt-6.1-sol -effort=xhigh `
  -report="$env:TEMP\sdlc-evidence-new"
```

`-command` selects a Codex executable if needed. `-timeout` defaults to 35
minutes for the complete workflow; `-role-timeout` defaults to 12 minutes for
one task. Interrupting the command cancels the Team and its members. An actual
check is bounded to 90 seconds, with a 60-second Go test timeout, process-tree
termination and a bounded pipe-drain wait. The generic process management helper
comes from this repository's `internal/processx`; model execution itself uses
the public SDK throughout.

The harness creates separate disposable role profiles under its temporary
workspace. `profile.CloneFrom(..., CopySettings(), LinkAuth())` uses the external
profile directly as the source and shares auth by link; the harness never reads,
prints or copies auth contents. Profiles are separate from both engineer source
and candidate snapshots. Cleanup failures are included in `report.json` and
cause failure. Never place the report in an authenticated profile directory.

## What the host enforces

The engineer can edit only `router.go` in its own project. The host-owned
`go.mod` and `acceptance_test.go` must keep their exact bytes. The complete
manifest must cover all three files, with no extra files, directories, symlinks,
invalid UTF-8, traversal or unbounded artifacts. The host reads actual bytes,
computes SHA-256 identities and copies them into a separate candidate snapshot.
Checks, gate acceptance and integration recheck the complete snapshot identity.

The fixed task limits imports and rejects `init`, `TestMain`, compiler directives
and package-variable function calls. This prevents trivial acceptance bypasses
such as `init(){os.Exit(0)}`. These are narrow task constraints, not a general
Go security sandbox. The generated `spec.md` states every constraint and the
eight requirements, including literal Unicode keys, Markdown/code/email/escape
handling, order, deduplication and human/agent routing permissions.

Role-bound Runner decorators enforce stage and duplicate-launch rules. A
rejected launch still calls its real Agent with an already cancelled context.
Valid launches forward all Team call options and add the host-selected
workspace through the public `WithWorkspace` option. Each host tool has a stable
semantic revision so it is compatible with named SDK conversations.

Both reviewer and verifier must actually reach their role's `workflow_begin`
tool before a cancellation-safe rendezvous releases either. The host checks
their actual running tasks and records task IDs and times. Acceptance requires
their task lifetimes to overlap. There are no sleeps to simulate parallel work.
The reviewer then reads the complete frozen files through its own
`workflow_read_candidate` capability, which issues a task/hash-bound read
receipt. The gate requires that exact receipt and candidate.

Only the verifier receives `workflow_run_checks`. Its command names map to fixed
argv, without a shell:

- `test`: `go test -json -timeout=60s -count=1 ./...`
- `vet`: `go vet ./...`

The check environment excludes provider credentials and disables network module
resolution. The host records argv, exit status, task, candidate, timestamps and
the stored log SHA-256. A zero test exit status alone is insufficient: every
fixed acceptance test must emit actual `run` and `pass` events, followed by a
package pass. Each check can run once per verifier/candidate. Both receipts must
be reported; hiding a failed receipt, inventing one, changing logs or reusing an
old candidate fails acceptance.

Every member has a strict output schema with no additional fields. The output
includes all requirement IDs, one bounded assessment/evidence entry per
requirement, findings, verdict, candidate identity and role-specific receipts or
manifest. Every string and array has a finite schema bound. Team delivers this
bounded structured final text to its leader according to its ordinary inline /
large-artifact contract. Full Raw and Transcript stay in the host evidence.
The leader is instructed to keep coordination summaries short and not reproduce
full member results.

The host reads succeeded tasks from `TaskStore.Tasks`, calls each
`Task.Result.Decode`, and independently checks the current role, specification,
candidate, evidence and receipts. Only `workflow_integrate` after an accepted
gate can produce an integrated result. Final success also requires successful
Team execution, terminal successful tasks, no protocol violations, intact
candidate/integration hashes and successful cleanup.

## Evidence and limits

The report directory contains:

- `spec.md`, the exact host acceptance source, and all role prompts;
- `tasks/*.json`: task/member/run identity, decoded output, usage, full Result,
  Raw streams and normalized Transcript;
- `candidates/NN/` and canonical manifests for each frozen candidate;
- `checks/*.log`, with command receipts in `report.json`;
- `events.json`: typed SDK events with their SDK envelopes;
- `leader-result.json`, including the partial Result from `RunError` on failure;
- `report.json`: actual host stage history, receipts, violations and final status;
- `integrated/` only after host acceptance.

Evidence is written through credential-field, bearer-token and secret-environment
redaction. The evidence tree never includes cloned profiles or auth files.
Recorded log hashes cover the stored redacted logs. Credentials must not be
deliberately put into the task source or prompts.

The host can establish which candidate bytes were read and tested, observable
overlap, command completion and schema/protocol compliance. A read receipt and
per-requirement explanation cannot prove the model's reasoning was correct.
The read-only leader and reviewer/verifier policies constrain filesystem writes;
instructions such as “do not run shell commands as leader” remain model
instructions, while role output and gate requirements prevent the leader from
substituting its own acceptance result. This example is not a containment system
for malicious generated Go code, a production authorization system, a durable
distributed scheduler or a complete test of a production Multica deployment.

Offline adversarial tests cover path escape, symlinks, missing/extra files,
forged manifests, hash drift, test skipping and forged test events, output
schemas, old candidates, wrong roles, missing reads, forged/hidden receipts,
gate order, rework limits, actual rendezvous cancellation, child-process cleanup,
partial failure evidence and redaction that preserves usage accounting.

## Sources

The separation of generation from evaluation and the use of structured handoffs
are informed by Anthropic's [Harness design for long-running application
development](https://www.anthropic.com/research/harness-design-long-running-apps).
The explicit stages, programmatic gates and parallel independent evaluations
adapt the composable patterns described in [Building effective
agents](https://www.anthropic.com/research/building-effective-agents). This is a
small SDK demonstration, not a reproduction of either article's experiments.
See the repository's [Team contract](../../../docs/team.md) for SDK semantics.
