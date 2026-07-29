# team-agent-workflow

This live showcase runs one leader with three delegated local roles:

```text
leader -> plan (read-only) -> impl (workspace-write) -> review (read-only)
```

The leader receives one `delegate_to_agent` MCP tool from
`hosttools/a2adelegation`. Role progress rejoins the leader's single typed
event stream as `SubagentUpdate`, and the AG-UI bridge exposes that same stream
to a browser as live `activityType="subagent"` messages. The CopilotKit page
keeps the leader chat on the left and renders plan, implementation, and review
cards in the right sidebar. Each card identifies its real provider base
(`Claude Code`, `Codex`, `Cursor`, or `CodeBuddy`) rather than displaying the
shared A2A transport label.

The read-only plan role has a per-call `WithSchema[planFileArtifact]` contract.
It returns a structured `PLAN.md` value (`filename`, `media_type`, `summary`,
and Markdown `content`) without writing into the workspace. The AG-UI terminal
activity preserves that value, and the plan card renders it as a previewable,
downloadable file attachment.

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
message makes one real leader call plus three real role calls and can incur
provider charges.

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

To run the deterministic workflow once and print its JSON audit verdict:

```bash
go run ./examples/showcases/team-agent-workflow -leader=claude -keep-workspace
```

The terminal run validates delegation order, ensures only `impl` changes the
workspace, requires the review approval sentinel, and checks the final
`SOLUTION.md` fixture.
