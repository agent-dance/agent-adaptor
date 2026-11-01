# team-agent-workflow

This live showcase runs one leader with three delegated local roles:

```text
leader -> plan (read-only) -> impl (workspace-write) -> review (read-only)
```

The read-only leader uses `hosttools/team`'s four scoped tools:
`team_delegate`, `team_send`, `team_status` and `team_cancel`. It delegates each
stage, ends its turn, and wakes on completion to dispatch the next stage.
`team_delegate` returns a queued task handle immediately. The roles are local
Runners; this showcase does not start separate A2A servers.

One `team.Thread("workflow").Send` execution contains the CLI workflow. Worker
state and text progress appear as typed `SubagentUpdate` in the leader's Event
stream. The AG-UI bridge projects worker lifecycles to
`activityType="subagent"` messages; the CopilotKit page groups them in the right
sidebar. Leader turns produce `TurnEnd`; the team's final `Done` and
`Stream.Result()` agree. The web handler publishes the Team as a Runner in
request-scoped task mode; it does not create a Standby or an input relay.

The read-only plan role has a per-call `WithSchema[planFileArtifact]` contract.
It returns structured `PLAN.md` data (`filename`, `media_type`, `summary`,
Markdown `content`) without writing files. The completion notification carries
its text to the leader, which includes it in the implementation objective.
Full member results remain in the Team TaskStore.

The Team event projection includes each role's configured provider display name,
live worker text, final text and decoded structured result. The CopilotKit sidebar
uses these Activity fields for provider badges and a downloadable `PLAN.md`
attachment. Full raw output, runtime metadata and transcripts stay in TaskStore;
the CLI audit reads the review result from there.

Team owns one authenticated loopback MCP listener with execution-scoped tokens;
workers get only messaging/status tools. The host closes the Team and then its
Agents. Task history uses memory here, so the example provides no crash recovery.
See the [team guide](../../../docs/team.md) for continuity, Standby, approvals,
limits, durable recovery, Fork and Remote members.

## One-command CopilotKit verification

Choose the local CLI explicitly and run:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
./examples/showcases/team-agent-workflow/start-all.sh codex
./examples/showcases/team-agent-workflow/start-all.sh codebuddy
```

The script:

1. installs the lockfile-pinned frontend dependencies when needed;
2. builds and starts this Go backend on `127.0.0.1:8080`;
3. waits for `/health` without starting an Agent run;
4. builds and starts the maintained [CopilotKit frontend](../../web-chat/copilotkit) on
   `127.0.0.1:3000` in team-workflow mode; and
5. shuts the backend down when the frontend exits.

Open <http://127.0.0.1:3000>. The request is already filled in, so click Send
to begin. Loading the page and probing `/health` are free. Every submitted
message can start real leader and worker CLI calls and incur provider charges.
The intended workflow has three delegated role tasks; leader executions/turns
vary as it coordinates and integrates their results.

Additional role flags are passed to the Go command:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude \
  -plan=codex -impl=claude -review=codex
```

Useful environment overrides:

| Variable | Default | Purpose |
| --- | --- | --- |
| `TEAM_ADDR` | `127.0.0.1:8080` | Backend listen address |
| `TEAM_BACKEND_BASE_URL` | `http://127.0.0.1:8080` | Browser-reachable backend base URL |
| `TEAM_UI_PORT` | `3000` | CopilotKit server port |
| `TEAM_UI_ORIGIN` | `http://127.0.0.1:$TEAM_UI_PORT` | Exact allowed browser origin |
| `TEAM_TIMEOUT` | `2h` | Lifetime of the backend process |
| `TEAM_ROLE_TIMEOUT` | `4m` | Maximum duration of one delegation |
| `KEEP_WORKSPACE` | `0` | Set to `1` to retain the temporary workspace on shutdown |

When changing the listen address for remote access, also set the corresponding
browser-reachable URL and exact UI origin. Do not expose this paid endpoint with
a wildcard CORS policy.

## CLI-only verification

To run the prescribed workflow once and print its JSON audit verdict:

```bash
go run ./examples/showcases/team-agent-workflow -leader=claude -keep-workspace
```

The terminal run validates delegation order, ensures only `impl` changes the
workspace, requires the review approval sentinel, and checks the final
`SOLUTION.md` fixture.
