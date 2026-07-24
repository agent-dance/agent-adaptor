# Workstream: Claude 常驻进程(Persistent Process, Route 1)

状态:已落地并通过实机验证(batch / streaming / interactive HITL 三种模式均复用同一进程)。

本文件同时是「结论」与「完整方案」。上半部分是可直接采信的结论与实证数据,下半部分是实现细节、约束遵从与测试矩阵。

---

## 1. 问题

Claude 适配器历史行为是「每个 Run 起一个 `claude` CLI 进程」。在同一会话的多轮对话里,每一轮都要:

1. 冷启动 Node.js CLI 进程(解释器 + 模块加载)
2. `--resume <session_id>` 从磁盘重放会话上下文(rehydration)

这两项在每轮都重复,且与本轮实际推理无关,构成纯开销。轮数越多、上下文越长,浪费越明显。

宿主诉求:同一会话内跨轮复用一个长驻 `claude` 进程,消除逐轮冷启动 + rehydration,且**不牺牲任何现有功能(streaming、HITL)与体验**。

## 2. 结论(TL;DR)

- **可行,且无需破坏性 API 改动。** 仅新增一个 opt-in 开关 `ClaudeConfig.PersistentProcess`;零值保持历史逐轮 spawn 行为完全不变。
- **三种执行形态全部支持常驻复用**:
  - 批量(batch,非流式非交互)
  - 流式(`WithStreaming()`)
  - 交互式 HITL(Phase 3:PlanReview / Question / Permission over stdio)
- **不需要取消 `Ask` 相关配置**。曾担心「HITL 必须每轮关 stdin,导致无法常驻」——实证证明该担心不成立:`type:result` 帧本身就是每一轮的边界,stdin 全程无需关闭。因此 `Ask` 默认行为与常驻能力可以共存。
- **收敛到同一条执行语义**。常驻路径不是第二个执行入口:它仍由 `adapter.Run(ctx, req, sink)` 分派,仍复用同一个 `claudeParser`、同一套 `DriverRunResult` 产出、同一套 session/checkpoint 语义。任何常驻侧失败都会透明回退到 spawn 路径。

### 实证数据

| 场景 | 首轮(冷) | 复用轮 | 单轮节省 |
|---|---|---|---|
| batch(`claude-haiku-4`,回复单字) | 3.40s | ~2.16s | ≥1.25s |
| streaming | 首轮 spawn=1 | 后续 spawn=0,per-turn demux 正常 | — |
| interactive HITL | 首轮 spawn=1,PlanReview 触发 | spawn=0,PlanReview 仍每轮触发 | — |

> 端到端数字含模型延迟抖动;可确定性消除的部分是「进程冷启动 + `--resume` rehydration」。独立基准 `scripts/claudebench` 测得该纯开销约百毫秒级/轮,随上下文增长而放大。

Phase 3 探针(`scripts/claudebench -phase3`)另行验证:在**不关闭 stdin** 的前提下,CLI 每轮照常发出 `type:result`,并在后续轮存活;`allow` / `deny+interrupt` / `interrupt 后存活` 等决策路径均正确。

## 3. 架构约束遵从(对齐 `AGENTS.md`)

- **§2.1 只有一套执行语义**:常驻是 `adapter.Run` 内部的一条可选分支,不新增执行入口、默认值合并、session 语义。
- **§7 helper 边界**:常驻进程管理**不**放进 `internal/clihelper`(其为一次性设计)。它独立在 `claude/persistent.go`,协议解析仍由 `claudeParser` 负责,helper 的职责边界不被侵蚀。
- **§8 非目标**:不引入 daemon / 常驻 server / 调度器。进程生命周期被局部化在 adapter 的 `persistentPool` 内,随 idle 超时自动回收。
- **§3.4 输出合同**:`buildPersistentResult` 产出与 spawn 路径一致的 `Output / RawStreams / Transcript / Summary / Result / Failure`。

## 4. 完整方案

### 4.1 API 表面(唯一新增)

```go
type ClaudeConfig struct {
    // ...
    // PersistentProcess 让该 Claude 绑定为每个 session 保持一个长驻
    // `claude --input-format stream-json` 子进程,后续轮通过 stdin 投喂,
    // 而非每个 Run 重新 spawn(并支付冷启动 + --resume rehydration)。
    // 零值 = 历史逐轮 spawn 行为。
    PersistentProcess bool
}
```

无其他公共 API 变动。`RunPolicy` / `HumanDecisionPolicy` / handler 注入方式全部不变。

### 4.2 分派逻辑(`claude/driver.go`)

`adapter` 变为持有 `*persistentPool` 的有状态绑定(指针在 value-copy 的 adapter 间共享,空闲时不持有任何进程)。`Run` 中:

```
if a.persistent != nil && cfg.PersistentProcess && persistentEligible(cfg, req) {
    构造 parser(setHITLContext;streaming||interactive 时 enableStreaming)
    interactive 时:断言 sink 为 DecisionCapableSink,构造 interactiveBinder
    组装 persistentSpec
    praw, perr := pool.run(ctx, spec, sink, parser, bind)
    perr == nil            -> 返回 buildPersistentResult(...)
    errors.Is(errPersistentFallback) -> 落到 spawn 路径
    其他错误               -> 直接返回
}
// 否则:历史 spawn 路径
```

`persistentEligible` 仅排除「CLI flags / 生命周期无法跨轮复用」的模式(如 `MaxTurnsPerRun>0`、原生结构化输出);batch / streaming / interactive 全部通过。

### 4.3 进程池(`claude/persistent.go`)

- **`persistentSpec`**:从 `DriverRunRequest` 派生的纯数据(command/model/effort/cwd/env/skipPerms/streaming/interactive/resumeID/prompt)。池永不回看 SDK 请求类型。
- **`sig()`**:复用签名。`streaming` 与 `interactive` 都进签名——它们改变 spawn flags,所以同一 session 的 batch / streaming / interactive 轮各自使用独立进程,互不污染输出形状。
- **`spawnArgs()`**:
  - 基础:`--print --output-format stream-json --verbose --input-format stream-json`
  - `streaming || interactive`:加 `--include-partial-messages`
  - `interactive`:再加 `--replay-user-messages --permission-prompt-tool stdio`;且**即使** `skipPerms` 也不加 `--dangerously-skip-permissions`(交互模式由 parser 按 policy 自动放行 permission control_request)
  - `resumeID != ""`:加 `--resume <id>`
- **`persistentPool`**:`map[sessionID]*liveProcess`。`acquire` 按 `resumeID` 命中并校验 `sig` 一致 + 未关闭才复用;签名漂移或进程已死则 evict 后 `--resume` 重启。首轮会在发现 session id 后 `register` 自身,使后续 resume 轮命中同一进程。
- **`liveProcess`**:单个长驻子进程。`turnMu` 串行化每轮 NDJSON 交换;`turn()` 写一帧 user frame,读 NDJSON 直到本轮 `type:result` 帧为止,喂给 parser。

### 4.4 关键难点:交互式 HITL 的 stdin 生命周期

一次性 Phase 3 里,parser 在正常轮结束时会 `Close()` stdin(让一次性 CLI flush `type:result` 并退出)。常驻模型必须让 stdin 跨轮存活。解法是 **`nonClosingStdin`**:

```go
type nonClosingStdin struct{ w io.Writer }
func (s nonClosingStdin) Write(frame []byte) error { _, err := s.w.Write(frame); return err }
func (s nonClosingStdin) Close() error             { return nil } // 关键:不关底层 pipe
```

- 轮边界改由 read loop 的 `type:result` 帧判定(实证:stdin 开着 CLI 照样每轮发 result)。
- `interactiveBinder` 在写 user frame 之前,把 parser 通过 `nonClosingStdin` 绑到 `liveProcess.stdin`,于是本轮的 `control_request` 可用 `control_response` 在同一条 live stdin 上回应。
- 写入并发安全:本轮初始 user frame 在 read goroutine 启动前由主 goroutine 写完;之后的 `control_response` 由 read goroutine 串行写;下一轮初始写受 `turnMu` 保护。无并发写。
- 真正的 pipe 生命周期由池掌控(evict / kill),parser 的 `Close` 只是 no-op。

### 4.5 生命周期、隔离与安全

- **ctx**:进程用池自有的 `context.Background()` 派生 ctx(而非单次 Run 的 ctx,后者在 Run 返回时被 cancel),evict 时显式 kill 进程组(`processx.ConfigureCancellation`)。单轮 `ctx.Done()`(取消/超时)→ `turn` 返回 → 池 evict+kill,下一次同 session 走 `--resume` 冷路径。
- **idle 回收**:每轮结束 arm 一个 `persistentIdleTimeout`(5min)计时器;超时自动 evict,避免长闲会话永久占用子进程。
- **失败即回退**:复用进程若在轮间死亡属正常偶发,`run` 返回 `errPersistentFallback`,调用方透明改走 spawn `--resume`。任何常驻侧失败都不影响正确性,只影响是否省下冷启动。
- **平台**:Windows 暂走回退(`.cmd/.ps1` shim 仍在 clihelper);常驻路径 POSIX-only。
- **session 隔离**:池按 Claude session id(== `DriverSessionState.ResumeID`)分桶;`register` 会 kill 掉同 id 下的陈旧进程。失败轮不污染健康 session 的 checkpoint 语义与 spawn 路径一致。

### 4.6 三模式矩阵

| 维度 | batch | streaming | interactive HITL |
|---|---|---|---|
| `--include-partial-messages` | 否 | 是 | 是 |
| `--replay-user-messages` `--permission-prompt-tool stdio` | 否 | 否 | 是 |
| parser `enableStreaming` | 否 | 是 | 是(重建 tool_use 输入) |
| `interactiveBinder` | nil | nil | 非 nil(绑 nonClosingStdin) |
| 复用 sig 隔离键 | interactive=0,streaming=0 | streaming=1 | interactive=1(streaming 归零) |

## 5. 测试矩阵

单测(无 CLI 依赖)+ `//go:build claude_live` 实机测试:

| 测试 | 断言 |
|---|---|
| `TestClaudePersistentProcessReuse` | turn1 spawn=1;turn2+ spawn=0;复用轮延迟 < 冷启动轮 |
| `TestClaudePersistentStreamingReuse` | turn1 spawn=1;turn2+ spawn=0;每轮各自收到 stream events(per-turn demux) |
| `TestClaudePersistentInteractiveReuse` | turn1 spawn=1 且 PlanReview 触发;turn2 spawn=0 且 PlanReview 仍触发;无 Failure |
| 现有 `phase3_live_test.go` | 未设 `PersistentProcess`,走 spawn 路径,行为不变 |

运行:

```
go test -tags claude_live -run 'TestClaudePersistent' -v ./claude/
```

基准与探针工具:`scripts/claudebench`(`-phase3` 子探针用于验证「不关 stdin 仍每轮出 result」)。

## 6. 非目标 / 未来工作

- 不做常驻 daemon / server / 跨进程共享池(违反 §8)。
- 不做原生结构化输出的常驻(其 CLI 形态不同,继续走 spawn)。
- Windows 常驻(需要移植 shim,当前回退)。
- 池的可观测性(活跃进程数、命中率)与可配置 idle 超时,可按宿主需要后续再评估,当前保持最小面。
