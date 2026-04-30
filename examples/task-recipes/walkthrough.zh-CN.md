# task-recipes · walkthrough

[English Version](./walkthrough.md)

> 这一份是**静态走查**（标准应该长什么样）。每次跑 spotlight 还会生成
> `.spotlight/task-recipes/last-run.md` 作为**动态事实**（这次实际看到了什么）。
> PR review 看本文件；事后排错对开两份对照。

## 1. 对位场景

凡是"产品里有 N 个固化任务，每个任务对应一组 instructions + skills + agents + hooks + config，宿主只下发 task_kind"的形态：

- incident hotfix bot：值班响应任务自动激活 incident-diagnostics + 严格审批
- scheduled review：定期 PR / 文档 review 自动配 reviewer agent + 项目级指令
- data migration：数据迁移任务挂 schema-validator + dry-run hooks + 严沙箱
- nightly scan / security scan：夜间安全扫描挂 security-reviewer + 只读隔离
- customer triage：客服分流任务挂 customer-context skill + 答复模板指令

每个固化任务在你家产品里其实就是一张"剧本卡"。本 spotlight 演示如何把卡声明出来 → SyncProfile 落到磁盘 → 用 binding-level + per-run 两种方式触发。

## 2. 一条命令

```bash
go run ./examples/task-recipes -agent=codex -keep-workspace
```

`-agent=claude` / `-agent=cursor` 也能跑。`-keep-workspace` 让临时 clone profile 不被清掉（方便手动 inspect）。

## 3. 终端产物

跑完后终端按这个顺序输出四块独立的可截图区域：

### 3.1 剧本卡片（`+` / `↻` 标记区分 additive vs replace）

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

读法：

- `+` 在 binding-level 默认剧本上 = 这条 resource 由本剧本声明（additive）
- `↻` 在 per-run override 上 = 这条 resource 在当次 run 中**取代**了同位 binding 默认（replace 语义，对应 §4.5）
- 6 行字段对齐宿主自家"任务定义表"：description / skills / agents / hooks / instructions / config / trigger

### 3.2 ProfileSnapshot diff（控制面真值）

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

每个 resource kind 一个块，`+` 表示 SyncProfile 落进 desired state 的条目。空块（如 `mcp/`）说明该 kind 在这条剧本中无声明，不报告漂移。

### 3.3 Profile 目录树（diff 视角；clone profile 真磁盘）

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

读法：

- `+ ...` = 由 recipe 触发新增的目录或汇总（带"new files"计数）
- 没有 `+` 前缀的行 = 已存在、SyncProfile 未改动
- `· [N pre-existing subdirectories collapsed]` = 一行折叠掉用户已有的不相关 skill 子目录（避免淹没 spotlight 信号）

这是**磁盘真值视图**：宿主能直接 `cd` 进 clone profile 目录里 `ls -la`，看到 reviewer.toml、AGENTS.md、hooks.json 等真实物件。

### 3.4 Run outcomes（同 prompt × 两条剧本）

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

每条剧本一行 driver_type / exit_code / output 头：宿主可观察"剧本切换在 agent 行为上的可见差异"。

### 3.5 三段 banner（统一收尾）

```
━━━ Story / Artifacts / Try next ━━━
（同其它 spotlight 一致，省略；见 last-run.md）
```

## 4. 文件系统产物

```
examples/task-recipes/
├── main.go                # 渲染 + 编排
├── recipes.go             # ★ 复制走的剧本字典
├── walkthrough.md         # 静态走查（本文件）
└── recipes-cookbook.md    # 6 条剧本范式

.spotlight/task-recipes/
└── last-run.md            # 本次 run 的真实快照（动态，不入 git）
```

`recipes.go` 是宿主**直接复制走**的资产：≤ 120 行，仅 import `agentadaptor` 公开包，零 SDK 内部依赖。新增一条剧本只需要在 `Recipes(...)` map 里加一行 + 一个 ~30 行函数。

## 5. 落到我家产品的哪里

| 这边的物件 | 对应你家产品的什么 panel |
| --- | --- |
| **剧本卡片** | 内部 wiki / 团队 README："我们家定义了这 N 个任务，每个对应一组 resources" 的视觉表达 |
| **ProfileSnapshot diff** | 后台"Profile 同步报告"页：本次 push 让 desired state 改了哪些 resources |
| **clone profile 目录树（diff 视角）** | 运维 / 合规：剧本不是空概念，它真的在磁盘上落了文件，ETL / backup / audit 都能直接消费 |
| **Run outcomes** | 任务执行 dashboard：同一个 agent 在不同剧本下行为差异的对比 |
| **`recipes.go`** | 你家代码库里的 `recipes.go`，每个剧本对应一行 + 一个函数；新增任务只要 PR ~30 行 |
| **`recipes-cookbook.md`** | onboarding 文档："想加一条新剧本？看这 6 条范式选最像的复制改名" |

集成模板（伪代码）：

```go
// 你家代码：把 Recipes 字典做成 task_kind 路由
recipes := myproduct.Recipes()

sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        agentadaptor.WithCloneProfile(profileDir, agentadaptor.CloneProfileOptions{...}),
        agentadaptor.WithDefaultProfileResources(recipes["base-coding"].Resources),
    )),
    agentadaptor.WithSkillSet(myproduct.SkillSet()),
)

// 收到一个 task_kind=incident-hotfix 的请求
recipe := recipes[req.TaskKind]
result, _ := sdk.Run(ctx, req.Prompt,
    agentadaptor.WithProfileResources(recipe.Resources),
    agentadaptor.WithMetadata("task_kind", req.TaskKind),
    agentadaptor.WithMetadata("request_id", req.ID),
)
```

剧本切换 = 1 行代码 + 1 行字典查找。其它都被 SDK 接管。

---

## 附录 · 不展示什么

- HITL 决策审批流 → `examples/human-in-the-loop`
- 多 driver 路由 / 多租户身份 / Admin 控制面 → `examples/multi-agent-platform`
- 流式 token / SSE 网关 / AG-UI 前端集成 → `examples/web-chat-stream`
- 30 秒最短路径 → `examples/quickstart-cli`
