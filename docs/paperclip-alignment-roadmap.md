# Agent Adaptor / Paperclip Alignment Roadmap

本文档定义 `agent-adaptor` 下一阶段最值得投入的规划。

目标不是“把 `paperclip` 的所有东西搬进来”，而是站在当前仓库已经拍板的边界上，继续沉淀那些已经被 `paperclip` 验证过、并且**确实属于纯 SDK** 的能力。

先把结论写死：

- `agent-adaptor` 仍然是纯 Go SDK
- 不引入第二套执行入口
- 不把 server / DB / queue / router / plugin store 塞回 core
- 后续工作优先服务两类对象：
  - 像 `paperclip` 这样的宿主系统
  - 直接嵌入 SDK 的 CLI / 桌面端 / 本地服务

## 0. 拆分文档索引

为避免 master roadmap 继续膨胀，后续实施按下面的 workstream 文档推进：

- [workstream-adapter-conformance-kit.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-adapter-conformance-kit.md)
- [workstream-session-codec.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-session-codec.md)
- [workstream-runtime-service-lifecycle-v2.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-runtime-service-lifecycle-v2.md)
- [workstream-builtin-probes.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-builtin-probes.md)
- [workstream-transcript-contract.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-transcript-contract.md)
- [workstream-bridges-profiles-host.md](/C:/Users/buthim/Documents/GitHub/agent-adaptor/docs/workstream-bridges-profiles-host.md)

## 1. 当前基线

当前 core 已经具备的关键能力：

- 默认 Agent 绑定优先的执行模型
- 单一 `Run/Start` 执行路径
- session / workspace / skills 的统一执行语义
- skills 动态装配闭环
- runtime services ensure / release 闭环
- richer run result / event stream / admin surface
- built-in `codex` / `claude` / `cursor` 的对齐实现

换句话说，当前仓库已经从“能跑”进入“可以做稳定宿主底座”的阶段。

下一阶段的重点不再是补零散 feature，而是回答三个更核心的问题：

1. 如何让更多 adapter 以更低成本、可验证地接入
2. 如何把 session / runtime / operator 语义做得足够稳定，不给宿主留坑
3. 如何在不破坏 core 边界的前提下，让 `paperclip` 这类上层系统接入成本继续下降

## 2. 规划原则

后续所有工作必须同时满足下面几条：

### 2.1 不破坏 core 定位

允许进入 core 的，只能是：

- adapter 合同
- 统一执行语义
- control-plane introspection
- host-neutral 的 runtime / session / skill contract

不允许进入 core 的，继续保持为非目标：

- 内置 HTTP/gRPC server
- 队列 / 调度器 / planner / router
- tenant DB / company skills service
- plugin store / UI 状态模型

### 2.2 优先沉淀“合同”，不是优先堆“能力点”

对 `paperclip` 最值钱的不是“多一个接口”，而是：

- 新 adapter 接进来能不能少踩坑
- 老 adapter 改动后会不会 silently break
- 宿主能不能依赖这些语义长期稳定演进

所以优先级应该始终偏向：

- contract
- conformance
- verification
- truthful introspection

而不是优先偏向某个单点 feature。

### 2.3 以真实宿主场景排序

评估某个能力值不值得做，不看“它像不像大平台功能”，而看它是否解决下面这些高频场景：

- 宿主需要稳定复用 session
- 宿主需要给 agent 准备 runtime services
- 宿主需要知道本地环境是否真的能跑
- 宿主需要知道为什么 resume / auth / quota 出问题
- 宿主需要快速引入新的 adapter 而不破坏旧语义

## 3. 用户与使用场景

## 3.1 直接使用 SDK 的调用方

典型形态：

- 本地 CLI
- 桌面端应用
- 最小 HTTP 服务壳
- 自动化任务 / cron worker

他们的核心诉求：

- 一套稳定的 `Run/Start/Admin` 语义
- 能绑定默认 agent，也能挂命名 agent
- 能接 session、skills、runtime，而不是自己再拼一层
- 能清楚知道环境是否健康、哪个模型生效、为什么不能 resume

## 3.2 `paperclip` 这类宿主系统

典型诉求：

- 同时承载多个 adapter
- 有自己的 DB / UI / orchestration 层
- 需要一个足够稳定的 adapter runtime kernel
- 需要在不复制 adapter 逻辑的前提下，快速扩展新的 agent 类型

对它最值钱的不是 SDK 帮它做服务层，而是 SDK 把这些难点做好：

- adapter contract
- session codec
- runtime lifecycle
- truthful admin / environment / quota / auth probes
- 新 adapter 的接入验收标准

## 3.3 adapter 实现者

典型诉求：

- 不想每次接一个新 agent 都重新猜 session / skills / runtime 语义
- 不想自己设计一套结果面和 control-plane
- 需要明确的验收标准，知道什么叫“接入完成”

## 3.4 operator / 支持同学

典型诉求：

- 为什么这个 adapter 跑不起来
- 是命令缺失、cwd 错了、auth 没配、quota 用光、还是 resume 上下文失效
- 当前生效模型到底是什么

## 4. 价值排序总览

从当前项目目标和 `paperclip` 使用场景出发，后续工作分成两类：

### 4.1 必须继续推进进 core 的

1. adapter conformance test kit
2. session codec 正式化
3. runtime service lifecycle v2
4. built-in quota / model / auth probes
5. normalized transcript / event contract

### 4.2 高价值，但应该做在 core 之上的

1. process adapter bridge package
2. http adapter bridge package
3. profile / resolver package
4. 最小 service host example

## 5. 核心工作流规划

## 5.1 Workstream A: Adapter Conformance Test Kit

### 为什么值得优先做

这是下一阶段最值钱的工作。

原因很简单：`paperclip` 真正依赖的是“一批 adapter 都遵守同一套合同”，而不是某一个 adapter 正好能工作。

如果没有 conformance kit，后面每新增一个 adapter，或者每次改 `RunResult` / session / runtime / skills 语义，都会回到手工检查和经验判断，成本高且不可靠。

### 服务的用户场景

- `paperclip` 新接一个 adapter，例如 `gemini` / `opencode` / `pi`
- 宿主系统升级 SDK 后，需要快速确认现有 adapter 没被破坏
- 第三方实现者需要知道“要做到什么程度才算兼容”

### 计划内容

- 提供可复用的 adapter contract test suite
- 覆盖以下维度：
  - `Run` / `Start` 一致性
  - event stream 最低行为要求
  - session checkpoint / resume / reject 行为
  - skills list / sync / run consumption
  - runtime services ensure / release / result reporting
  - admin environment / model / config schema / quota surface
  - error / timeout / non-zero exit truthful reporting

### 交付物

- `adaptertest` 或类似 package
- 面向 adapter 作者的接入说明文档
- 内置 adapter 全量接入 conformance suite

### 验收标准

- 新 adapter 可以通过引入 test kit 获得一组标准化 contract tests
- built-in adapters 全部跑过这套测试
- contract 变更时，测试可以第一时间暴露 breakage

### 不做什么

- 不做 UI
- 不做 mock server 框架
- 不把每个 provider 的特殊逻辑塞进 test kit

## 5.2 Workstream B: Session Codec 正式化

### 为什么值得做

当前 SDK 已经把 `cwd` / `prompt_bundle_key` 这类 resume 关键信息写进 `DriverSessionState.Data`，但它还属于半结构化状态。

这一步的价值在于把“session 可恢复性”从经验约定升级成正式合同。

### 服务的用户场景

- `paperclip` 需要稳定判断某个 session 是否仍可继续
- 宿主需要把 session 状态存到自己的数据库里
- adapter 作者需要明确哪些字段决定 resume compatibility

### 计划内容

- 引入正式 `SessionCodec` SPI
- 统一能力：
  - deserialize
  - serialize
  - display id
  - compatibility key / resume guard fields
- 保持现有 session 执行模型不变，只把结构表达做清楚

### 交付物

- core `SessionCodec` 合同
- built-in adapters 的 codec 实现
- session persistence / compatibility 文档
- 面向宿主的持久化示例

### 验收标准

- 宿主不需要猜 `DriverSessionState.Data` 中的 key
- resume guard 语义在 adapter 侧有明确、可测试的 contract
- built-in adapters 对 cwd / prompt bundle / workspace 等关键字段有稳定策略

### 不做什么

- 不改 session store 的整体职责边界
- 不引入新的 session 执行入口

## 5.3 Workstream C: Runtime Service Lifecycle V2

### 为什么值得做

当前 runtime services 已经有 ensure / release 闭环，但离 `paperclip` 这类复杂宿主的真实诉求还有差距。

真正值钱的是把 runtime service 从“一个 URL 列表”升级成“一个可复用、可观测、可回收的运行时资源合同”。

### 服务的用户场景

- agent 需要依赖 dev server / db / cache / browser backend
- 宿主想复用共享 service，而不是每次重新拉起
- operator 需要知道 service 是否健康、是谁持有、什么时候该停

### 计划内容

- 扩展 runtime service contract：
  - lifecycle: `shared` / `ephemeral`
  - `reuseKey`
  - `health`
  - `stop policy`
  - richer runtime reports
- 明确 SDK 与 `RuntimeServiceManager` 的职责边界

### 交付物

- runtime lifecycle contract 文档
- richer `RuntimeServiceReport`
- 面向共享 service 与临时 service 的 example

### 验收标准

- 宿主可以明确声明某个 runtime service 是 shared 还是 ephemeral
- adapter 和 host 都能读到统一的 lifecycle / health 语义
- release / cleanup 规则对宿主是可解释的

### 不做什么

- 不做内置 service orchestrator
- 不在 core 中引入具体的 docker / tmux / process supervisor

## 5.4 Workstream D: Built-in Quota / Model / Auth Probes

### 为什么值得做

这件事对 `paperclip` 的 operator 价值极高。

现在 surface 已经有了，但 built-in adapter 还只是“诚实地说 unavailable”。下一步非常值得把 probe 做实。

### 服务的用户场景

- 为什么这台机器上的 Claude / Codex 不能跑
- 当前实际生效的是哪个模型
- 账号是不是登录了
- quota 是不是已经触顶

### 计划内容

- `codex` 本地 auth / model / quota probe
- `claude` 本地 auth / model / quota probe
- `cursor` 至少补强 auth / config / model truth surface

### 交付物

- built-in adapter probes
- `Admin().GetQuota()` 的真实返回
- richer environment report examples

### 验收标准

- built-in adapter 的 `CheckEnvironment()` 能区分 command / cwd / auth 问题
- `DetectModel()` 尽量反映真实生效配置，而不是只回显 binding config
- 支持 quota 的 adapter 返回真实 quota windows

### 不做什么

- 不在 core 做 provider SDK 大而全封装
- 不承诺所有 adapter 都必须支持 quota probe

## 5.5 Workstream E: Normalized Transcript / Event Contract

### 为什么值得做

`paperclip` 类宿主系统真正需要的不只是 raw stdout/stderr，而是足够稳定的结构化执行信号。

现在的输出合同有两个明显问题：

- `Output` 在不同宿主眼里语义冲突，有时被当最终文本，有时被当 raw stdout
- shared helper 会对 stdout/stderr 做保守 JSON 猜测，这和 adapter 自己识别正式协议的边界不够一致

所以下一步不是“继续在旧事件上补字段”，而是把输出合同重整为一套清晰的单轨结构。

### 服务的用户场景

- 宿主想统一渲染 agent 输出
- 宿主想区分 assistant、thinking、tool、result summary
- 宿主想做运行分析和 UI 展示，但不想自己重复实现各家 adapter parser
- 宿主即使只走 `Run()`，也仍然需要拿到原始 stdout/stderr 做审计与调试

### 计划内容

- 重整 `RunResult` / `DriverRunResult` / `RunEvent` 的输出合同
- 明确分离：
  - `Output`：最终 assistant 文本
  - `RawStreams`：原始 stdout / stderr
  - `Transcript`：标准化语义条目
  - `Summary`：简短摘要
  - `Result`：terminal result 原始 JSON
- 让 shared helper 只负责进程 IO 与原始 chunk 分发，不再猜协议
- 让 built-in adapters 自己做正式协议解析，并从同一次解析同时产出 transcript、summary、checkpoint、result
- 允许 public API break，不保留旧 event / transcript enum 的兼容层
- 不在代码命名里引入 `V2` / `v2` 后缀

### 交付物

- transcript / event contract 文档
- built-in `codex` / `claude` / `cursor` 的 streaming parser
- examples 与 tests 的输出合同迁移
- 宿主消费建议

### 验收标准

- 宿主可以在不依赖 provider-specific JSON 的情况下消费核心 transcript 语义
- `Run()` 与 `Start().Wait()` 都能拿到完整原始 stdout/stderr
- `Output` 在 built-in adapters 上都等于最终 assistant 文本
- `RunResult.Transcript` 与按顺序收集到的 transcript item 事件完全一致
- shared helper 不再感知 provider 协议

### 不做什么

- 不做 UI framework
- 不把所有 provider-specific token 流抽象得过度复杂
- 不为了 transcript conformance 引入测试专用 public SPI

## 6. 上层包 / 示例规划

这些工作很有价值，但不该进入 core。

## 6.1 Process Adapter Bridge Package

### 价值

让宿主把“一个本地进程协议”快速桥接成 `DriverAdapter`，降低接入新 agent 的门槛。

### 典型场景

- `paperclip` 接一个本地 CLI agent，但不想把完整 adapter 逻辑写进 server 层

### 边界

- 做成独立 package
- 不塞进 core 主包

## 6.2 HTTP Adapter Bridge Package

### 价值

让远程 agent / sidecar agent 也能复用同样的 SDK 心智。

### 典型场景

- 宿主已经有 HTTP service 形态的 adapter
- 需要在不改 core 的情况下复用 `Run/Admin` 合同

## 6.3 Profile / Resolver Package

### 价值

把业务角色映射到 `AgentBinding`，直接降低 `paperclip` 这种多角色宿主的接入复杂度。

### 典型场景

- `default-coding`
- `review`
- `ops`
- `safe-migration`

### 边界

- 是 core 之上的 mapping layer
- 不能变成 core 的第二套执行入口

## 6.4 Minimal Service Host Example

### 价值

给宿主一套“怎么把 SDK 嵌到服务里”的最小可运行参考，而不是让每个宿主自己拼。

### 典型场景

- 需要 HTTP 层，但不需要完整 `paperclip`
- 想复用 session store / runtime manager / named agents 语义

## 7. 分阶段路线图

## Phase 1: Core Hardening

目标：

- 把 adapter contract 固化
- 把 session resume 语义正式化

内容：

- Workstream A: Adapter Conformance Test Kit
- Workstream B: Session Codec 正式化

为什么先做：

- 这是后续所有 adapter 扩展的地基
- 先做这两项，能显著降低后面每个能力的回归风险

## Phase 2: Runtime / Operator Readiness

目标：

- 让 SDK 更适合作为 `paperclip` 级宿主底座
- 让 operator 看到真实状态，而不是猜状态

内容：

- Workstream C: Runtime Service Lifecycle V2
- Workstream D: Built-in Quota / Model / Auth Probes
- Workstream E: Normalized Transcript / Event Contract

## Phase 3: Leverage Above Core

目标：

- 让更多宿主更轻松地用上 core
- 继续保持 core 边界干净

内容：

- Process Adapter Bridge Package
- HTTP Adapter Bridge Package
- Profile / Resolver Package
- Minimal Service Host Example

## 8. 成功标准

如果这个路线做对了，应该看到这些结果：

### 8.1 对 core 本身

- 新增 adapter 的接入成本下降
- 变更 contract 时能被 conformance tests 及时拦住
- session / runtime / admin 行为更可解释、更少隐式约定

### 8.2 对 `paperclip` 这类宿主

- 宿主层代码显著减少重复 adapter glue code
- 宿主不需要自己猜 auth / quota / model / resume 状态
- 接新 adapter 的速度更快，且风险更低

### 8.3 对直接 SDK 用户

- 不需要自己再造一层 runtime/session/admin 抽象
- 能通过 examples 直接看懂如何嵌入 CLI / 桌面端 / 最小服务

## 9. 验证策略

后续每个 workstream 都必须同时交付：

- contract 设计
- regression tests / conformance tests
- runnable example
- README 或 `docs/` 文档

不接受“先把接口放进去，后面再补示例 / 文档 / 测试”的做法。

## 10. 明确延后或不做的事项

下列能力即使 `paperclip` 里有，也不应作为 core 规划目标：

- 内置 HTTP/gRPC server
- queue / scheduler / dispatch / router
- tenant DB / company skills service
- plugin store
- UI 状态模型

这些能力如果以后要做，也应作为：

- example
- side package
- 宿主层工程

而不是继续扩大 core。

## 11. 推荐执行顺序

如果只按投入产出比排序，建议的顺序是：

1. Adapter Conformance Test Kit
2. Session Codec 正式化
3. Runtime Service Lifecycle V2
4. Built-in Quota / Model / Auth Probes
5. Normalized Transcript / Event Contract
6. Process / HTTP Adapter Bridge Packages
7. Profile / Resolver Package
8. Minimal Service Host Example

一句话总结：

下一阶段最有价值的，不是把 `paperclip` 的服务层继续搬进来，而是把 `paperclip` 已经验证过的 adapter contract 沉淀成更稳定、更可扩展、更容易被宿主复用的 SDK 能力。
