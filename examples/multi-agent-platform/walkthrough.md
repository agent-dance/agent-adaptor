# multi-agent-platform · walkthrough

[简体中文 / Chinese Version](./walkthrough.zh-CN.md)

> This is the **static walkthrough** (what the standard should look like). Every spotlight run also produces
> `.spotlight/multi-agent-platform/last-run.md` as the **dynamic factual mirror** (what was actually observed this time).
> Use this file for PR review; pull both up side-by-side when troubleshooting after the fact.

## 1. Host scenarios

Any product where "one process needs to mount multiple drivers, route by scenario, give every caller its own identity and profile, and surface health / quota / model / skills to ops":

- Internal dev platform: wrap codex / claude / cursor as "our own platform" so downstream teams program against a single SDK
- Multi-tenant SaaS AI assistants: each tenant gets its own clone profile + identity, with no cross-talk
- Team-scoped AI ops backend: "default agent / review agent / autopilot agent" inside a single product, each routed to a different driver
- "We need to ship codex/claude/cursor across our product line": run this spotlight first to walk the control-plane fields once, then decide how the backend schema should look

Each named agent is one row in your platform's "agent registry". This spotlight demonstrates three things: 1) routing actually works (the same prompt going through different drivers gives different output); 2) every named agent really does have a clone profile on disk; 3) the Admin read-only API hands you all the fields your ops backend needs in one shot.

## 2. One-liner

```bash
go run ./examples/multi-agent-platform \
    -default-agent=codex -review-agent=claude -autopilot-agent=cursor
```

The flag defaults are already `codex / claude / cursor`, so you can omit them. If any of the three CLIs is unhealthy, that named agent is automatically SKIPPED instead of panicking; as long as the default CLI is healthy, the spotlight produces meaningful output.

Environment variable overrides (CI-friendly):

- `MULTIAGENT_DEFAULT_AGENT` / `MULTIAGENT_REVIEW_AGENT` / `MULTIAGENT_AUTOPILOT_AGENT`
- `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND` (follows the existing `exampleutil` convention)

## 3. Terminal artifacts

After the run, the terminal prints these five independent screenshot-ready blocks in order.

### 3.1 Agents Overview (ops dashboard prototype)

```
Agents Overview
┌─────────────┬────────────────────┬──────────┬───────────┬─────────┬─────────┬──────────┐
│ name        │ driver@model       │ tenant   │ env       │ models  │ quota   │ skills   │
├─────────────┼────────────────────┼──────────┼───────────┼─────────┼─────────┼──────────┤
│ default     │ codex@gpt-5.4      │ acme     │ healthy   │ 3       │ n/a     │ 2 sel    │
│ review      │ claude@sonnet-4    │ acme     │ healthy   │ 2       │ n/a     │ 1 sel    │
│ autopilot   │ cursor@gpt-5       │ acme     │ healthy   │ 2       │ n/a     │ 1 sel    │
└─────────────┴────────────────────┴──────────┴───────────┴─────────┴─────────┴──────────┘
```

How to read it:

- `name` = named agent (`default` / `review` / `autopilot`); strictly aligned with `sdk.Default()` / `sdk.Agent("review")` in host code
- `driver@model` = "which CLI · which model", at a glance for ops
- `tenant` = the tenant field injected via `WithDefaultIdentity{TenantID: "acme"}`
- `env` = the healthy state from `Admin().Agent(name).CheckEnvironment()`
- `models` = count from `ListModels()`, the dropdown source for the ops backend's model picker
- `quota` = the simplified label from `GetQuota()` (`ok` / `90%!` / `n/a`)
- `skills` = count of `ListSkills().Selected`, the current selection in the host's skills selector UI

If a named agent is not healthy, it shows up as a single SKIPPED row, with a `skipped reason · <name>: <reason>` line under the table explaining why — for example, the cursor CLI is not on PATH, or `CLAUDE_COMMAND` points at the wrong path.

### 3.2 Same-prompt routing comparison (physical evidence that routing works)

```
Same-prompt routing comparison
prompt: "Reply with one short sentence acknowledging this request."
─ default   (codex)  ── I’ve read the repository instructions and will follow them.
─ review    (claude)  ── Not logged in · Please run /login
```

How to read it:

- The two lines receive **the exact same prompt**, but the output is dramatically different — that's the physical evidence that "`sdk.Default()` and `sdk.Agent("review")` really go through different drivers", not just an internal SDK struct claim.
- The first line (codex) prints the real answer; the second line (claude) prints `Not logged in · Please run /login` — that's the same `stderr_head` fallback used by task-recipes: when the CLI is healthy but unauthenticated, the host sees the first line of the runtime error directly, no swallowing by the SDK.
- Once you have more routing combinations, the host can do "switch named agent by task_kind" — e.g. review tasks always go through claude, autopilot tasks always go through cursor.

### 3.3 Clone profile directory tree (disk evidence that each named agent has its own profile)

```
Clone profile directory trees (tree -L 2)
root: /var/folders/.../agent-adaptor-multi-859738119

default/  (id=default-codex · profile=/var/folders/.../default)
  .tmp/
    plugins/
    plugins.sha
  auth.json
  config.toml
  installation_id
  logs_2.sqlite
  memories/
  models_cache.json
  sessions/
    2026/
  skills/
    .system/
    ai-slop-cleaner/
    · …27 more entries
  state_5.sqlite
  tmp/
    arg0/

review/  (id=review-claude · profile=/var/folders/.../review)
  .claude.json
  projects/
  sessions/
  settings.json
  skills/
    algorithmic-art/
    brand-guidelines/
    canvas-design/
    · …14 more entries

autopilot/  (id=autopilot-cursor · profile=/var/folders/.../autopilot)
  mcp.json
  skills/
```

How to read it:

- The three subdirectories are **physically separated on disk**: default is the codex native profile clone (`config.toml` / `auth.json` / `sessions/`); review is claude's (`.claude.json` / `projects/` / `settings.json`); autopilot is cursor's (`mcp.json` / `skills/`).
- `WithCloneProfile(dir, CloneProfileOptions{IncludeSettings:true, IncludeMCP:true, IncludeSkills:true, IncludeAuth:true})` copies the host user-level profile into each subdirectory; downstream you can backup / audit / destroy each one independently.
- The `id=...` on each row is the stable identity injected via `WithDefaultIdentity{ID, ProfileID, ...}`, matching your "agent registry primary key".

### 3.4 Admin sweep summary (one-shot report of every ops backend field)

```
Admin sweep summary (read-only API surface · per role)
─ default
  environment  : status=pass · healthy=true · checks=8
  models       : 3 listed
  profile      : supported=true · dir=/var/folders/.../default · source=profile_option
  config_schema: 10 fields
  quota        : available=false · provider=openai · windows=0
  skills       : supported=true · selected=[review-note write-proof]
─ review
  environment  : status=warn · healthy=true · checks=4
  models       : 2 listed
  profile      : supported=true · dir=/var/folders/.../review · source=profile_option
  config_schema: 10 fields
  quota        : available=false · provider=anthropic · windows=0
  skills       : supported=true · selected=[review-note]
─ autopilot
  environment  : status=warn · healthy=true · checks=5
  models       : 2 listed
  profile      : supported=true · dir=/var/folders/.../autopilot · source=profile_option
  config_schema: 9 fields
  quota        : available=false · provider=cursor · windows=0
  skills       : supported=true · selected=[write-proof]
```

For each healthy named agent, one line summarises the key fields from `Info() / CheckEnvironment / ListModels / GetProfile / ConfigSchema / GetQuota / ListSkills`. The full structures (including ConfigSchema fields, the Models list, Environment checks, etc.) live in `admin-snapshot.json`.

Header sample of `admin-snapshot.json`:

```json
{
  "autopilot": {
    "info": {
      "Name": "autopilot",
      "Default": false,
      "DriverType": "cursor",
      "DisplayName": "Cursor Agent",
      "Descriptor": {
        "Type": "cursor",
        "DisplayName": "Cursor Agent",
        "Models": [
          { "ID": "gpt-5", "Label": "gpt-5" },
          { "ID": "claude-sonnet-4", "Label": "claude-sonnet-4" }
        ],
        "ConfigSchema": {
          "Fields": [
            { "Name": "command", "Label": "Command", "Type": "text", "Default": "agent", "Group": "command", ... },
            { "Name": "model", "Label": "Model", "Type": "select", "Options": [...], "Group": "model", ... },
            ...
          ]
        },
...
```

The full JSON is roughly 80KB; with all three CLIs healthy it has three top-level keys (`default / review / autopilot`); when any one is SKIPped that key turns into `{"status":"skipped","reason":"..."}`. The host can **drive its SaaS backend schema directly off this JSON**, no need to read SDK source.

### 3.5 Selection isolation evidence (physical proof of multi-tenant isolation)

```
Selection isolation evidence (process-local override is per-agent)
default.skills.selected (before override): [review-note write-proof]
default.skills.selected (after  override): [write-proof]
+ default skills changed
review.skills.selected  (unchanged):       [review-note]
+ review unchanged · override on default did not bleed across
```

How to read it:

- Calling `SetSelectedSkills(ctx, ["write-proof"])` on default narrows it from 2 skills to 1.
- In the same process, review's `ListSkills()` still returns `[review-note]` — unaffected by the override on default.
- This is on-stage evidence of "per-agent process-local override": when the host changes skills for tenant A in a multi-tenant SaaS, the change does not leak to tenant B.

### 3.6 Three-banner footer (unified close-out)

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
One process, three named agents, three clone profiles, one Admin API surface — your SaaS ops dashboard already has all the fields it needs.
对位：internal dev platform · multi-tenant SaaS · team-scoped AI assistant

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/multi-agent-platform/admin-snapshot.json
- .spotlight/multi-agent-platform/last-run.md
- examples/multi-agent-platform/walkthrough.md
- /var/folders/.../agent-adaptor-multi-859738119

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/human-in-the-loop -agent=claude
```

## 4. Filesystem artifacts

```
examples/multi-agent-platform/
├── main.go                # Rendering + orchestration (≤ 600 LOC target)
└── walkthrough.md         # Static walkthrough (this file)

.spotlight/multi-agent-platform/
├── admin-snapshot.json    # Merged JSON of three drivers × the full Admin read-only API (dynamic, gitignored)
└── last-run.md            # Real snapshot of this run (dynamic, gitignored)

/tmp/agent-adaptor-multi-*/  # Per-named-agent clone profile subdirectories
├── default/                 # codex clone, contains config.toml / auth.json / sessions/ / skills/ ...
├── review/                  # claude clone, contains .claude.json / settings.json / projects/ ...
└── autopilot/               # cursor clone, contains mcp.json / skills/ ...
```

`-keep-profiles=true` keeps the clone profile directories around after the spotlight finishes, so the host can `cd` in and inspect what each driver's native config actually looks like.

## 5. Where this lands in your product

Each Admin read-only API artifact maps to one panel in your SaaS backend:

| Artifact here | Panel in your product |
| --- | --- |
| **`Admin().Agents()` / `Info()`** | "Agent registry" page: list of three named agents + driver type + whether each is the default |
| **`CheckEnvironment()`** | "Health check" page: red/green light per agent (`pass` / `warn` / `error`); click in to see the Checks list |
| **`ListModels()`** | "Model picker" dropdown source: which models each named agent can offer in its settings UI |
| **`GetProfile()`** | "Profile inspector" / "Per-agent config" page: real disk path of the agent's native profile, and whether it is host-managed |
| **`ConfigSchema()`** | Form generator on the "Edit profile" page: render text / select / toggle controls based on the Fields list — no need to hand-paint a UI per driver |
| **`GetQuota()`** | "Usage / Cost" panel: per-agent quota windows (per-day / per-month), current usage percentage, when it resets |
| **`ListSkills()` / `SetSelectedSkills()`** | "Skills selector" UI: admins toggle which skills are selected; this spotlight has already proven that process-local overrides don't pollute across agents |
| **`SetSelectedSkills` per-agent isolation** | "Multi-tenant safety guarantee" section (your security docs): document an SDK-level hard contract that no agent contaminates another |
| **clone profile directory tree** | Ops / compliance: recipes / configs / auth land on disk = ETL backup / audit / destruction can all run as filesystem operations |
| **same-prompt routing comparison** | Internal demo: let stakeholders see "our routing really works" at a glance — not a PowerPoint arrow but actual different output from different drivers |

Integration template (pseudocode):

```go
// 1. Host mounts three named agents at startup
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(codex.New(myProductCodexConfig(), 
        agentadaptor.WithCloneProfile("./profiles/default", agentadaptor.CloneProfileOptions{
            IncludeSettings: true, IncludeMCP: true, IncludeSkills: true, IncludeAuth: true,
        }),
        agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
            ID: "default-codex", TenantID: req.TenantID, ProfileID: "default-profile", Name: "default",
        }),
    )),
    agentadaptor.WithAgent("review", claude.New(myProductClaudeConfig(), 
        agentadaptor.WithCloneProfile("./profiles/review", ...),
        agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{ID: "review-claude", TenantID: req.TenantID, ...}),
    )),
    agentadaptor.WithAgent("autopilot", cursor.New(myProductCursorConfig(), ...)),
    agentadaptor.WithSkillSet(myProductSkillSet()),
)

// 2. Backend pulls Admin API fields and feeds them to the frontend ops dashboard
admin := sdk.Admin()
for _, agent := range admin.Agents() {
    a, _ := admin.Agent(agent.Name)
    panel := myProductOpsPanel{
        Name:        agent.Name,
        DriverType:  agent.DriverType,
        Environment: must(a.CheckEnvironment(ctx)),
        Models:      must(a.ListModels(ctx)),
        Profile:     must(a.GetProfile(ctx)),
        ConfigSchema: must(a.ConfigSchema(ctx)),  // Feed the form generator directly
        Quota:       must(a.GetQuota(ctx)),
        Skills:      must(a.ListSkills(ctx)),
    }
    renderToFrontend(panel)
}

// 3. Routing: dispatch to the matching named agent by task_kind
runner, _ := sdk.Agent(routeTo(req.TaskKind))   // "review" / "autopilot" / fall back to default
result, _ := runner.Run(ctx, req.Prompt, agentadaptor.WithMetadata("tenant_id", req.TenantID))
```

The whole SaaS ops backend draws ~80% of its fields directly from the Admin API, so the host only has to do three things: 1) decide how many named agents to mount per tenant; 2) write a task_kind → name routing table; 3) turn ConfigSchema into the form controls of your own design system in the settings UI.

---

## Appendix · What this spotlight does not show

To keep the "multi-agent platform" story clear, spotlight #3 deliberately **does not** demonstrate these (see the matching spotlight):

- The 30-second shortest path → `examples/quickstart-cli`
- Token streaming / SSE gateway / AG-UI frontend integration / multi-turn sessions → `examples/web-chat-stream`
- HITL decision approvals / three-act play / audit ndjson → `examples/human-in-the-loop`
- Task recipes (skills + instructions + agents + hooks + config layered) → `examples/task-recipes`
