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
`activityType="subagent"` messages; the assistant-ui page renders each as a
collapsible worker card with its status, text and result. Leader turns produce
`TurnEnd`; the team's final `Done` and `Stream.Result()` agree. Web mode serves
the Team through `bridges/agui/aguisession` in request-scoped task mode, with
in-memory history for reload, and in-run guidance to the leader through
`POST /message` (`Stream.Send`); it does not create a Standby.

The read-only plan role has a per-call `WithSchema[planFileArtifact]` contract.
It returns structured `PLAN.md` data (`filename`, `media_type`, `summary`,
Markdown `content`) without writing files. The completion notification carries
its text to the leader, which includes it in the implementation objective.
Full member results remain in the Team TaskStore.

The Team event projection includes each role's configured provider display name,
live worker text, final text and decoded structured result. The worker card
shows the member name, status, text and the decoded result (including the
`PLAN.md` content). Full raw output, runtime metadata and transcripts stay in TaskStore;
the CLI audit reads the review result from there.

Team owns one authenticated loopback MCP listener with execution-scoped tokens;
workers get only messaging/status tools. The host closes the Team and then its
Agents. Task history uses memory here, so the example provides no crash recovery.
See the [team guide](../../../docs/team.md) for continuity, Standby, approvals,
limits, durable recovery, Fork and Remote members.

## One-command assistant-ui verification

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
4. starts the maintained [assistant-ui page](../../web-chat/assistant-ui) on
   `127.0.0.1:3000`, whose same-origin Vite proxy forwards to the backend
   (`AGUI_BACKEND_URL`), so no CORS origin is needed; and
5. shuts the backend down when the frontend exits.

Open <http://127.0.0.1:3000> and describe the change to plan, implement and
review. Loading the page and probing `/health` are free. Every submitted
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
| `TEAM_BACKEND_BASE_URL` | `http://127.0.0.1:8080` | Backend base URL the page's dev proxy forwards to |
| `TEAM_UI_PORT` | `3000` | assistant-ui page port |
| `TEAM_TIMEOUT` | `2h` | Lifetime of the backend process |
| `TEAM_ROLE_TIMEOUT` | `4m` | Maximum duration of one delegation |
| `KEEP_WORKSPACE` | `0` | Set to `1` to retain the temporary workspace on shutdown |

When changing the listen address, also set the matching backend base URL. Do not
expose this paid endpoint publicly; for a separately served browser use
`-web-cors` with one exact origin, never a wildcard.

## CLI-only verification

To run the prescribed workflow once and print its JSON audit verdict:

```bash
go run ./examples/showcases/team-agent-workflow -leader=claude -keep-workspace
```

The terminal run validates delegation order, ensures only `impl` changes the
workspace, requires the review approval sentinel, and checks the final
`SOLUTION.md` fixture.
