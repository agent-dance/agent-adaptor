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

## 6. 决策记录:为什么是 opt-in 开关,而不是默认行为

这一节记录「`PersistentProcess` 为何设计成 opt-in 开关而非默认打开」的完整推演,便于后来者理解取舍,并作为未来「是否默认化」的评审基线。

### 6.1 问题

既然常驻对多轮场景普遍有收益、且失败会透明回退,为什么不直接默认打开、让所有 Claude 绑定开箱即得?

### 6.2 反对「默认打开」的四条理由

1. **adapter 从无状态变有状态**:历史上 Claude adapter 一个 Run 一个进程、随 Run 结束而死;常驻让 adapter 持有 `persistentPool`、进程活过单次 Run ctx。默认打开等于让所有宿主在不知情下多出一批常驻子进程,违背 `AGENTS.md §2.4`(可靠性优先、不静默改变语义)。
2. **资源足迹静默增长**:每个活跃 session 占一个 Node 进程,并在末轮后保留 `persistentIdleTimeout`(5min)才回收。对「每请求一个短 session」的宿主,默认打开会静默堆积 idle 进程。
3. **`env` parity 缺口(最硬的一条,属正确性问题)**:复用签名 `sig()` 目前包含 command/model/effort/skipPerms/streaming/interactive/cwd/extraArgs,**不含 `env`**。同一 session 后续轮若改了 env 绑定,复用进程仍用 spawn 时的旧 env,而 spawn 路径每轮重启不会有此问题。
4. **平台/成熟度**:Windows 当前回退;常驻路径刚过实机验证,宜先在显式 opt-in 宿主里跑一段真实流量。

其中「怕破坏兼容」不算强理由——任何常驻侧失败都透明回退到 spawn,正确性在失败路径被保住;「用户体验」反而是支持默认化的正向论据。

### 6.3 追问:如果给宿主一个「可主动 close 的句柄」,上述理由是否全部不成立?

结论:**不是全都成立不了,最硬的一条不受影响。**

- **理由 1(有状态/生命周期归属)——被化解,甚至加分**:显式 close 把生命周期所有权交还宿主,statefulness 从隐式变显式可控。这是支持默认化的正向论据。
- **理由 3(`env` parity)——完全不受影响**:这是正确性问题,与生命周期正交。只要进程还活着被复用,后续轮改 env 仍吃旧 env;close 救不了它。**这才是默认化前必须先解决的硬阻挡。**
- **理由 2(资源静默增长)——只被部分且不对症地缓解**:默认化的目标人群恰是「不知道该特性存在」的宿主,他们不会去调 close;真正给他们兜底的是 idle 超时,而非 close。close 只帮到「已知情、愿主动管理」的宿主,而那批人 opt-in 也能拿到。
- **作用域陷阱**:`RunHandle` 是 per-Run(每轮一个),而常驻进程是 per-session。close 若挂在 `RunHandle` 上要么语义错配、要么「每轮就关」等于没常驻;要做对必须挂在 **session 维度**(如 SDK 级 `Release(sessionRef)` 或 session 句柄),而当前公共 API 无此层,新增它本身是一次扩面,且离 `AGENTS.md §8`(不把进程/daemon 管理塞进 core)较近,需单独拍板。

### 6.4 决定

- **当前**:保持 opt-in 的 `ClaudeConfig.PersistentProcess`(零值 = 历史逐轮 spawn)。这是本 workstream 已落地的实现。
- **`env` parity 缺口**:作为已知限制记录(见 §6.5),opt-in 语义下仅影响「同时 opt-in 且逐轮改 env」的宿主;是默认化的前置必修项。
- **close 句柄**:是独立值得做的能力(把生命周期还给宿主),但不是默认化的充分条件,与 parity 正交;若做须挂在 session 维度。

### 6.5 通往默认化的分步路线(未来工作,非本次范围)

1. 补 `env`(及任何逐轮可变、影响进程状态的字段)进 `sig()`,或证明其不可逐轮变 —— 关闭正确性缺口。
2. 池的可观测性(活跃进程数、命中率)+ 可配置 idle 超时。
3. (可选)session 维度的显式 `Release`,把生命周期还给宿主。
4. 跑过真实流量后,再评估把默认翻为「有 `SessionStore` 时自动常驻」——与「无 `SessionStore` = 无状态」的现有心智天然契合,并保留逃生阀(如 `DisablePersistentProcess`)。

### 6.6 已知限制(opt-in 语义下)

- `sig()` 不含 `env`:同一 session 逐轮变更 env 绑定时,复用进程保留 spawn 时的 env。需要逐轮 env 隔离的宿主暂勿开启 `PersistentProcess`,或每轮用不同 session。

## 7. 非目标 / 未来工作

- 不做常驻 daemon / server / 跨进程共享池(违反 §8)。
- 不做原生结构化输出的常驻(其 CLI 形态不同,继续走 spawn)。
- Windows 常驻(需要移植 shim,当前回退)。
- 池的可观测性(活跃进程数、命中率)与可配置 idle 超时,可按宿主需要后续再评估,当前保持最小面。
