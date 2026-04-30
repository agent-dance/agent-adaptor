# human-in-the-loop · walkthrough

[English Version](./walkthrough.md)

> 这一份是**静态走查**（标准应该长什么样）。每次跑 spotlight 还会生成
> `.spotlight/human-in-the-loop/last-run.md` 作为**动态事实**（这次实际看到了什么）。
> PR review 看本文件；事后排错对开两份对照。

## 1. 对位场景

凡是"agent 想跑 shell / 想调外部 API / 想写文件，必须先问真人"的产品：

- 金融 / 医疗 / 合规系统里的危险操作审批
- 内部生产环境写操作（部署、回滚、数据迁移）
- PR auto-fix bot：自动 fix 提案要 reviewer 批准
- IT 自动化：开账号、给权限、改 DNS、动 firewall
- 任何"超过保险阈值的操作必须留审计记录"的系统

## 2. 一条命令

```bash
go run ./examples/human-in-the-loop -agent=claude \
    -decision-timeout=6s -fake-front-end-delay=2s
```

切到 `-agent=codex` 或 `-agent=cursor` 也能跑（capability matrix 仍然出，三幕变成 SKIP，理由清楚指向 driver 真值）。

## 3. 终端产物

跑完后终端按这个顺序输出三块独立的可截图区域：

### 3.1 Capability matrix（三家 driver 真值表）

```
Capability matrix (driver-truth, not docs)
┌─────────────┬──────────────────┬──────────────────┬──────────────────┐
│ driver      │ Permission       │ PlanReview       │ Question         │
├─────────────┼──────────────────┼──────────────────┼──────────────────┤
│   codex     │ Auto             │ Auto             │ (none)           │
│ ● claude    │ Auto AutoR       │ Ask Auto AutoR   │ Ask AutoR        │
│   cursor    │ Auto             │ Auto             │ (none)           │
└─────────────┴──────────────────┴──────────────────┴──────────────────┘
Ask=adapter raises real decision request · Auto=adapter resolves silently · Retry=re-ask supported
● = current -agent (claude)
```

读法：

- **Ask** = adapter 会把决策真发给宿主（spotlight 三幕需要这一列至少一个为 true 才能演出）
- **Auto** = adapter 自己默默处理（合规无审计意义，不展示在话剧里）
- **AutoR** = adapter 支持本地合成 reject（宿主不部署 handler 时的兜底）
- **Retry** = adapter 支持 reject 后重新发起同一决策

> v1 的现实：**只有 claude 的 PlanReview 与 Question 真有 Ask 通道**。codex / cursor 当前都没有 Ask；要 HITL 治理就得挂 claude。这不是 SDK 的 bug，是各家 CLI 的能力现状。

### 3.2 三幕话剧（按 RunPolicy 物理结果分类）

```
━━━ Scene 1 · Sync Reject ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = rejected
  decision = reject
  latency  = 10.919s
  output   = Of course! Here's my clarifying question:

━━━ Scene 2 · Async Approve ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = approved
  decision = approve
  latency  = 13.167s
  output   = Of course! Here's my clarifying question:

━━━ Scene 3 · Timeout Abort ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = timed-out
  decision = timeout
  latency  = 14.702s
  output   = Of course! Here's my clarifying question:
```

每幕的 status 取自下面的真值集合：

| status | 含义 | 何时出现 |
| --- | --- | --- |
| `rejected` | 决策被宿主拒绝（`IsRejected()=true`） | sync handler 返回 `ApprovalRejected` 或 channel 用 `DecisionRejected` resolve |
| `approved` | 决策被宿主批准 | channel 用 `DecisionApproved` / `DecisionAnswered` resolve（视 kind 而定） |
| `timed-out` | `RunPolicy.HumanDecision.Timeout` 到期且 `OnTimeout=Abort`（`IsTimedOut()=true`） | handler 阻塞 / channel 没有消费者 |
| `untriggered` | agent 没发起 decision request | prompt / driver / model 因素，`OutputHead` 显示 agent 直接回答了什么 |
| `skipped` | driver `Descriptor.RunPolicyCaps` 在所选 kind 上不支持 Ask | 当前 -agent 没有任意 Ask 能力 |
| `error` | Wait 返回非取消的 error | 环境问题（CLI 未认证 / 网络等） |

### 3.3 三段 banner

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Three short plays show how your compliance / risk gates land at the SDK boundary.
对位：compliance approvals · PR auto-fix · IT change control

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/human-in-the-loop/audit/session.ndjson
- .spotlight/human-in-the-loop/last-run.md
- examples/human-in-the-loop/walkthrough.md
- examples/human-in-the-loop/audit-schema.md

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/task-recipes -agent=claude
```

## 4. 文件系统产物

```
.spotlight/human-in-the-loop/
├── audit/
│   └── session.ndjson          # 每次决策一行；schema 见 audit-schema.md
└── last-run.md                 # 本次 run 的真实快照（动态，不入 git）
```

**audit ndjson 样本**（5 字段）：

```json
{"ts":"2026-04-29T13:24:40.146453Z","run_id":"9f99b3d7d8...","kind":"question","decision":"reject","resolved_by":"sync-handler","latency_ms":10919,"note":"Scene 1 · Sync Reject"}
{"ts":"2026-04-29T13:24:53.313272Z","run_id":"8e8dc91327...","kind":"question","decision":"approve","resolved_by":"async-channel","latency_ms":13166,"note":"Scene 2 · Async Approve"}
{"ts":"2026-04-29T13:25:08.015253Z","run_id":"e3b0d83c9d...","kind":"question","decision":"timeout","resolved_by":"policy","latency_ms":14701,"note":"Scene 3 · Timeout Abort"}
```

字段含义、jq 食谱、ETL 接入策略详见 [`audit-schema.md`](./audit-schema.md)。

## 5. 落到我家产品的哪里

| 这边的物件 | 对应你家产品的什么 panel |
| --- | --- |
| **capability matrix 表** | 后台"AI 集成能力"页：哪些 driver 支持哪类审批通道，决定下单时绑哪个 driver |
| **Scene 1 · Sync Reject** | 同步审批弹窗：reviewer 在 IDE / 工单系统里点 Reject，agent 立刻终止，运行结果带 `IsRejected()=true` |
| **Scene 2 · Async Approve** | 浏览器消息推送：决策请求落到通知中心，reviewer 异步点 Approve；前端通过 `DecisionRequests()` channel + `ResolveDecision()` 回填 |
| **Scene 3 · Timeout Abort** | 兜底策略：reviewer 长时间不回应自动 abort（防止 agent 永久 hang），失败带 `IsTimedOut()=true` |
| **audit ndjson** | 合规审计 ETL：直接 `tail -F` 进 Splunk / ELK / Datadog，字段稳定，`run_id` 关联宿主 RunResult |
| **`audit-schema.md`** | 给审计接入工程师看的 schema 文档：他们不需要读 Go 源码就能集成 |

集成模板（伪代码）：

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        // 不给 binding-level handler，让决策走 channel
    )),
)

handle, _ := sdk.Start(ctx, prompt, agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
    HumanDecision: agentadaptor.HumanDecisionPolicy{
        PlanReview: agentadaptor.HumanDecisionAsk,
        Timeout:    8 * time.Second,
        OnTimeout:  agentadaptor.FailureAbort,
        OnReject:   agentadaptor.FailureAbort,
    },
}))

// 你家前端从 DecisionRequests() 拿请求，渲染审批卡片，等 reviewer 点击后回填
go func() {
    for req := range handle.DecisionRequests() {
        decision := waitForReviewer(req)             // 你家逻辑
        _ = handle.ResolveDecision(req.RequestID, decision)
    }
}()

result, _ := handle.Wait(ctx)
switch {
case result.Failure.IsRejected(): /* 给 reviewer 反馈 */
case result.Failure.IsTimedOut(): /* 给 reviewer 提醒 */
default:                          /* 正常完成 */
}
```

---

## 附录 · 不展示什么

为了把故事讲清楚，spotlight #4 故意**不**演这些（去对应 spotlight 看）：

- profile resources / skills / instructions 剧本化 → `examples/task-recipes`
- 流式 token / SSE 网关 / AG-UI 前端集成 → `examples/web-chat-stream`
- 多 driver 路由 / 多租户身份 / Admin 控制面 → `examples/multi-agent-platform`
- 30 秒最短路径 → `examples/quickstart-cli`
