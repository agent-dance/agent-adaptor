# task-recipes · walkthrough

[简体中文 / Chinese Version](./walkthrough.zh-CN.md)

> This is the **static walkthrough** (what the output should look like). Each spotlight
> run additionally writes `.spotlight/task-recipes/last-run.md` as the **dynamic factual mirror**
> (what was actually observed this run). Use this file for PR review; diff the two for post-mortems.

## 1. Host scenarios

Any shape where "the product has N fixed task kinds, each binding a set of instructions + skills + agents + hooks + config, and the host only routes a `task_kind`":

- incident hotfix bot: on-call response tasks auto-activate incident-diagnostics + strict approval
- scheduled review: recurring PR / docs review auto-binds the reviewer agent + project-level instructions
- data migration: schema-validator + dry-run hooks + a tight sandbox for migration tasks
- nightly scan / security scan: nightly security audits binding security-reviewer + read-only isolation
- customer triage: customer-context skill + reply-template instructions for triage tasks

In your product, every fixed task is essentially a "recipe card". This spotlight demonstrates how to declare that card → land it on disk via `SyncProfile` → trigger it through binding-level + per-run paths.

## 2. One-liner

```bash
go run ./examples/task-recipes -agent=codex -keep-workspace
```

`-agent=claude` / `-agent=cursor` work too. `-keep-workspace` keeps the temporary clone profile around (handy for manual inspection).

## 3. Terminal artifacts

After a successful run, the terminal prints four independently-screenshotable blocks in this order:

### 3.1 Recipe cards (`+` / `↻` markers distinguish additive vs replace)

```
┌─ Recipe · base-coding ─────────────────────────────────────────────
│ description : Team default; safe coding tasks.
│ skills      : + repo-map, write-proof
│ agents      : + reviewer
│ hooks       : + pre-tool-audit (disabled)
│ instructions: + team-defaults · scope=project · mode=additive
│ config      : + model/model-default
│ trigger     : default (binding-level)
└─

┌─ Recipe · incident-hotfix ─────────────────────────────────────────
│ description : Override for incident response.
│ skills      : ↻ incident-diagnostics
│ agents      : ↻ incident-reviewer
│ hooks       : ↻ post-run-summary (disabled)
│ instructions: ↻ incident-hotfix · scope=run · mode=additive
│ config      : ↻ approval/hotfix-approval
│ trigger     : per-run via WithProfileResources
└─
```

How to read it:

- `+` on the binding-level default recipe = this resource is declared by the recipe (additive)
- `↻` on a per-run override = this resource **replaces** the binding default for the current run (replace semantics, see §4.5)
- The 6 fields line up with the host's own "task definition table": description / skills / agents / hooks / instructions / config / trigger

### 3.2 ProfileSnapshot diff (control-plane truth)

```
ProfileSnapshot diff (after SyncProfile)
agents/
  + reviewer
config/
  + model-default
hooks/
  + pre-tool-audit
instructions/
  + team-defaults
mcp/
skills/
  + repo-map
  + write-proof
```

One block per resource kind; `+` marks entries `SyncProfile` wrote into the desired state. An empty block (e.g. `mcp/`) means this kind has no declarations in the recipe and no drift is reported.

### 3.3 Profile directory tree (diff view; clone profile, real disk)

```
before SyncProfile (root level only):
  auth.json
  config.toml
  skills/  [129 nested entries]

after SyncProfile (+ = added by recipe):
+ [3 new files at root]
  [2 pre-existing files at root]
+ agents/  [+1 new files]
  skills/  [+2 new files]
  · [31 pre-existing subdirectories collapsed]
```

How to read it:

- `+ ...` = directory or summary added by the recipe (with a "new files" count)
- Lines without a `+` prefix = pre-existing, untouched by `SyncProfile`
- `· [N pre-existing subdirectories collapsed]` = a single line collapsing the user's pre-existing, unrelated skill subdirectories (so they don't drown out the spotlight signal)

This is the **on-disk truth view**: the host can `cd` into the clone profile directory and `ls -la` to see real artifacts like reviewer.toml, AGENTS.md, hooks.json.

### 3.4 Run outcomes (same prompt × two recipes)

```
Run outcomes
─ base-coding
  driver_type = codex
  exit_code   = 0
  output      = The base-coding recipe is active.
─ incident-hotfix
  driver_type = codex
  exit_code   = 0
  output      = I'm loading the scoped instructions and the incident-hotfix profile file now ...
```

One driver_type / exit_code / output header per recipe: the host can observe "the visible behavioral diff caused by switching recipes".

### 3.5 Three-banner footer (unified closing)

```
━━━ Story / Artifacts / Try next ━━━
(same as the other spotlights; omitted, see last-run.md)
```

## 4. Filesystem artifacts

```
examples/task-recipes/
├── main.go                # Rendering + orchestration
├── recipes.go             # ★ Recipe dictionary, copy-and-take
├── walkthrough.md         # Static walkthrough (this file)
└── recipes-cookbook.md    # 6 recipe patterns

.spotlight/task-recipes/
└── last-run.md            # This run's actual snapshot (dynamic, gitignored)
```

`recipes.go` is a **copy-and-take** asset for the host: ≤ 120 lines, importing only the public `agentadaptor` package, zero SDK-internal dependencies. Adding a new recipe takes a single entry in the `Recipes(...)` map plus a ~30-line function.

## 5. Where this lands in your product

| What's here | The matching panel in your product |
| --- | --- |
| **Recipe cards** | Internal wiki / team README: a visual presentation of "here are the N tasks we define, each bound to a set of resources" |
| **ProfileSnapshot diff** | Backend "Profile sync report" page: which resources this push changed in the desired state |
| **Clone profile directory tree (diff view)** | Ops / compliance: a recipe is not an abstraction — it really lands files on disk that ETL / backup / audit can consume directly |
| **Run outcomes** | Task execution dashboard: the behavioral diff of the same agent under different recipes |
| **`recipes.go`** | The `recipes.go` in your codebase: each recipe is one map entry + one function; adding a task is a ~30-line PR |
| **`recipes-cookbook.md`** | Onboarding doc: "want to add a new recipe? Pick the closest of these 6 patterns and copy-rename" |

Integration template (pseudocode):

```go
// Your code: turn the Recipes dictionary into a task_kind router
recipes := myproduct.Recipes()

sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{...}),
        agentadaptor.WithDefaultProfileResources(recipes["base-coding"].Resources),
    )),
    agentadaptor.WithSkillSet(myproduct.SkillSet()),
)

// On a request with task_kind=incident-hotfix
recipe := recipes[req.TaskKind]
result, _ := sdk.Run(ctx, req.Prompt,
    agentadaptor.WithProfileResources(recipe.Resources),
    agentadaptor.WithMetadata("task_kind", req.TaskKind),
    agentadaptor.WithMetadata("request_id", req.ID),
)
```

Recipe switching = 1 line of code + 1 dictionary lookup. Everything else is handled by the SDK.

---

## Appendix · What this spotlight does not show

- HITL decision approval flow → `examples/human-in-the-loop`
- Multi-driver routing / multi-tenant identity / Admin control plane → `examples/multi-agent-platform`
- Token streaming / SSE gateway / AG-UI frontend integration → `examples/web-chat-stream`
- 30-second shortest path → `examples/quickstart-cli`
