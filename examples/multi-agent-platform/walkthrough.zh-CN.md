# multi-agent-platform · walkthrough

[English Version](./walkthrough.md)

> 这一份是**静态走查**（标准应该长什么样）。每次跑 spotlight 还会生成
> `.spotlight/multi-agent-platform/last-run.md` 作为**动态事实**（这次实际看到了什么）。
> PR review 看本文件；事后排错对开两份对照。

## 1. 对位场景

凡是"一个进程里要挂多 driver、按场景路由、每个调用方独立身份独立 profile、运维要看健康度 / 配额 / 模型 / skills"的产品：

- 内部 dev platform：把 codex / claude / cursor 三家包装成"我家平台"，下游团队只对一个 SDK 编程
- 多租户 SaaS AI 助手：每个租户对应一份 clone profile + 一份 identity，互不干扰
- 团队级 AI ops 后台：同一个产品里"默认 agent / review agent / autopilot agent"分别调用不同 driver
- "我们家产品要上 codex/claude/cursor 三家"：先用本 spotlight 把控制面字段过一遍，再决定后台 schema 怎么设计

每个 named agent 在你家平台里就是一行"agent registry"。本 spotlight 演示三件事：1) 路由真的生效（同 prompt 走不同 driver 拿到不同输出）；2) 每个 named agent 真的有一个磁盘上的 clone profile；3) Admin read-only API 把运维后台需要的所有字段一次性给齐。

## 2. 一条命令

```bash
go run ./examples/multi-agent-platform \
    -default-agent=codex -review-agent=claude -autopilot-agent=cursor
```

flag 默认值已经是 `codex / claude / cursor`，不传也行。三家 CLI 任一不 healthy 时该 named agent 自动 SKIP，不 panic；只要 default 这家 healthy，整个 spotlight 就能跑出有意义的产物。

环境变量覆盖（CI 友好）：

- `MULTIAGENT_DEFAULT_AGENT` / `MULTIAGENT_REVIEW_AGENT` / `MULTIAGENT_AUTOPILOT_AGENT`
- `CODEX_COMMAND` / `CLAUDE_COMMAND` / `CURSOR_COMMAND`（沿用既有 `exampleutil` 约定）

## 3. 终端产物

跑完后终端按这个顺序输出五块独立的可截图区域。

### 3.1 Agents Overview（运维仪表盘原型）

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

读法：

- `name` = 命名 agent（`default` / `review` / `autopilot`），跟宿主代码里 `sdk.Default()` / `sdk.Agent("review")` 严格对齐
- `driver@model` = 运维一眼看穿的"哪家 CLI · 哪个模型"
- `tenant` = `WithDefaultIdentity{TenantID: "acme"}` 注进去的租户字段
- `env` = `Admin().Agent(name).CheckEnvironment()` 的 healthy 状态
- `models` = `ListModels()` 数量，运维后台 model picker 的下拉源
- `quota` = `GetQuota()` 的简化标签（`ok` / `90%!` / `n/a`）
- `skills` = `ListSkills().Selected` 的数量，宿主 skills selector UI 的当前选择

如果某个 named agent 不 healthy，它会以一行 SKIPPED 出现，并在表下方给一行 `skipped reason · <name>: <reason>` 解释为什么——比如 cursor CLI 不在 PATH，或 `CLAUDE_COMMAND` 指错了路径。

### 3.2 同 prompt 路由对比（路由真的生效的物理证据）

```
Same-prompt routing comparison
prompt: "Reply with one short sentence acknowledging this request."
─ default   (codex)  ── I’ve read the repository instructions and will follow them.
─ review    (claude)  ── Not logged in · Please run /login
```

读法：

- 两条线收到**完全相同的 prompt**，输出却天差地别——这就是"`sdk.Default()` 和 `sdk.Agent("review")` 真的走了不同 driver"的物理证据，不是 SDK 内部 struct 的口头宣称。
- 第一行（codex）输出真实回答；第二行（claude）输出 `Not logged in · Please run /login` —— 这是 task-recipes 同款的 `stderr_head` fallback：当 CLI healthy 但未登录时，宿主直接看到运行时报错的第一行，不会被 SDK 吞掉。
- 路由组合多了，宿主就可以做"按 task_kind 切 named agent"——比如 review 任务永远走 claude / autopilot 任务永远走 cursor。

### 3.3 Clone profile 目录树（每个 named agent 独立 profile 的磁盘证据）

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

读法：

- 三个子目录在物理磁盘上**真实分开**：default 是 codex 的 native profile clone（`config.toml` / `auth.json` / `sessions/`）；review 是 claude 的（`.claude.json` / `projects/` / `settings.json`）；autopilot 是 cursor 的（`mcp.json` / `skills/`）。
- `WithCloneProfile(dir, CloneProfileOptions{IncludeSettings:true, IncludeMCP:true, IncludeSkills:true, IncludeAuth:true})` 把宿主用户级 profile 复制进各自子目录，下游可以独立 backup / 独立审计 / 独立销毁。
- 每行的 `id=...` 是 `WithDefaultIdentity{ID, ProfileID, ...}` 注入的稳定身份，对应宿主自家的"agent registry 主键"。

### 3.4 Admin sweep summary（运维后台所有字段的一次性报告）

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

每个 healthy named agent 一行 `Info() / CheckEnvironment / ListModels / GetProfile / ConfigSchema / GetQuota / ListSkills` 的关键字段。完整结构（含 ConfigSchema fields、Models 列表、Environment checks 等）落在 `admin-snapshot.json` 里。

`admin-snapshot.json` 头部样本：

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

整份 JSON 大约 80KB，三家 healthy 时三个 top-level key（`default / review / autopilot`），任一 SKIP 时对应 key 改成 `{"status":"skipped","reason":"..."}`。宿主可以**直接用这份 JSON 反推 SaaS 后台的 schema**，不需要读 SDK 源码。

### 3.5 Selection isolation evidence（多租户隔离的物理证据）

```
Selection isolation evidence (process-local override is per-agent)
default.skills.selected (before override): [review-note write-proof]
default.skills.selected (after  override): [write-proof]
+ default skills changed
review.skills.selected  (unchanged):       [review-note]
+ review unchanged · override on default did not bleed across
```

读法：

- 在 default 上调用 `SetSelectedSkills(ctx, ["write-proof"])` 把它从 2 个 skill 缩到 1 个。
- 同进程里 review 的 `ListSkills()` 仍然返回 `[review-note]`——不受 default override 的污染。
- 这是"per-agent process-local override"的现场证据：宿主在多租户 SaaS 里给租户 A 改 skills 不会泄漏给租户 B。

### 3.6 三段 banner（统一收尾）

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

## 4. 文件系统产物

```
examples/multi-agent-platform/
├── main.go                # 渲染 + 编排（≤ 600 行目标）
└── walkthrough.md         # 静态走查（本文件）

.spotlight/multi-agent-platform/
├── admin-snapshot.json    # 三家 driver × Admin 全套 read-only API 的合并 JSON（动态，不入 git）
└── last-run.md            # 本次 run 的真实快照（动态，不入 git）

/tmp/agent-adaptor-multi-*/  # 三个 named agent 各自的 clone profile 子目录
├── default/                 # codex 的 clone，含 config.toml / auth.json / sessions/ / skills/ ...
├── review/                  # claude 的 clone，含 .claude.json / settings.json / projects/ ...
└── autopilot/               # cursor 的 clone，含 mcp.json / skills/ ...
```

`-keep-profiles=true` 让 spotlight 跑完不删 clone profile 目录，宿主可以 `cd` 进去手动 inspect 三家 driver 的 native 配置实际长什么样。

## 5. 落到我家产品的哪里

每件 Admin read-only API 的产物都对应宿主自家 SaaS 后台的一个 panel：

| 这边的物件 | 对应你家产品的什么 panel |
| --- | --- |
| **`Admin().Agents()` / `Info()`** | "Agent registry" 页：显示三个 named agent 的列表 + driver 类型 + 是否默认 |
| **`CheckEnvironment()`** | "Health check" 页：每行 agent 的红绿灯（`pass` / `warn` / `error`），点开看 Checks 列表 |
| **`ListModels()`** | "Model picker" 下拉源：每个 named agent 在自家 settings UI 里能选哪些模型 |
| **`GetProfile()`** | "Profile inspector" / "Per-agent config" 页：显示 agent 的 native profile 真磁盘路径、是否 host-managed |
| **`ConfigSchema()`** | "Edit profile" 页的表单生成器：根据 Fields 列表渲染 text / select / toggle 控件，不需要为每家 driver 各画一份 UI |
| **`GetQuota()`** | "Usage / Cost" 面板：显示每个 agent 的 quota 窗口（按天 / 按月）、当前已用百分比、何时重置 |
| **`ListSkills()` / `SetSelectedSkills()`** | "Skills selector" UI：管理员勾哪些 skill 进入选中态；本 spotlight 已经证明 process-local 不跨 agent 污染 |
| **`SetSelectedSkills` per-agent isolation** | "多租户安全保证"段（你家安全文档）：写 SDK level 的硬合同，跨 agent 不串味 |
| **clone profile 目录树** | 运维 / 合规：剧本 / 配置 / 认证落到磁盘上 = ETL backup / 审计 / 销毁都能直接走文件系统操作 |
| **同 prompt 路由对比** | 内部演示：让 stakeholder 一眼看到"我们路由真的生效"——不是 PPT 上画的箭头，而是不同 driver 真给出了不同输出 |

集成模板（伪代码）：

```go
// 1. 宿主在启动时挂三个 named agent
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

// 2. 后端把 Admin API 的字段拿出来喂前端 ops 仪表盘
admin := sdk.Admin()
for _, agent := range admin.Agents() {
    a, _ := admin.Agent(agent.Name)
    panel := myProductOpsPanel{
        Name:        agent.Name,
        DriverType:  agent.DriverType,
        Environment: must(a.CheckEnvironment(ctx)),
        Models:      must(a.ListModels(ctx)),
        Profile:     must(a.GetProfile(ctx)),
        ConfigSchema: must(a.ConfigSchema(ctx)),  // 直接喂表单生成器
        Quota:       must(a.GetQuota(ctx)),
        Skills:      must(a.ListSkills(ctx)),
    }
    renderToFrontend(panel)
}

// 3. 路由：按 task_kind 调对应 named agent
runner, _ := sdk.Agent(routeTo(req.TaskKind))   // "review" / "autopilot" / fall back to default
result, _ := runner.Run(ctx, req.Prompt, agentadaptor.WithMetadata("tenant_id", req.TenantID))
```

整个 SaaS ops 后台 ~80% 字段从 Admin API 直接来，宿主只需要做三件事：1) 决定每个租户挂几个 named agent；2) 写 task_kind → name 的路由表；3) 在 settings UI 上把 ConfigSchema 转成自家 design system 的表单控件。

---

## 附录 · 不展示什么

为了把"多 agent 平台"故事讲清楚，spotlight #3 故意**不**演这些（去对应 spotlight 看）：

- 30 秒最短路径 → `examples/quickstart-cli`
- 流式 token / SSE 网关 / AG-UI 前端集成 / 多轮 session → `examples/web-chat-stream`
- HITL 决策审批流 / 三幕话剧 / 审计 ndjson → `examples/human-in-the-loop`
- 任务剧本（skills + instructions + agents + hooks + config 叠加）→ `examples/task-recipes`
