# Workstream：常驻进程与真实 CLI BDD

状态：常驻实现、provider 合同测试、15 组 Gherkin 规格和 v1 Godog smoke runner 已迁入。
本次本地验收不主动调用付费模型；真实 CLI runner 由 build tag 与环境变量双门保护。

## v1 决策

- Claude、CodeBuddy、Codex 的 `Descriptor.Process.Persistent` 为 `true`，Cursor 为 `false`。
- 对显式 `Thread`，支持常驻的 Driver 默认复用 provider 进程；无状态 `Agent` 调用仍逐轮启动。
- `WithSpawn()` 是 `SharedOption`：用于 `New` 时成为 Agent 默认值，用于 `Run`/`Stream` 时只覆盖本轮；它强制使用新进程且不把该进程留在池中。
- `Agent.Close(ctx)` 幂等关闭 Driver 管理的全部常驻进程；Close 开始后的新运行返回 `ErrAgentClosed`。
- `Run` 与 `Stream` 仍共用唯一 invocation 和 Event 管线。常驻只是 Driver 内部 transport 生命周期选择，不新增执行动词、事件通道或结果合同。
- Thread 兼容指纹重绑时，Driver 使用 `SessionContext.PreviousID` 在新 writer 启动前回收旧 record 对应的进程，保持单 writer 接力。
- prompt 一旦可能已送达 provider，通道失败不得回退重放；只有明确发生在 prompt 交付前的启动/通道失败才可安全走单次进程路径。

## BDD 边界

Gherkin 规格位于 `e2e/features`，真实 CLI runner 位于 `e2e/*_test.go`。默认选择三家
`@smoke&&@journey` 三轮旅程。步骤只允许使用公共 `Agent`、`Thread`、`Stream`、`Event`、
`Result` API 调用 `exec.LookPath` 解析到的真实 CLI；禁止 fake CLI、shell shim 和协议录制回放兜底。

每个场景建立独立的临时 workspace、`memory.Store`、随机 Thread key 和随机 token。runner
持续 drain 唯一 `Events()`，从 `ProcessInfo{Kind: ProcessSpawn}` 记录 PID，从
`Thread.Checkpoint()` 读取 provider ResumeID，并在 After hook 中使用独立 timeout context
调用 `Agent.Close`。认证、模型权限或网络未就绪可以明确 skip；协议、输出、checkpoint、
PID 或生命周期断言失败仍然是测试失败。

真实模型调用有两道门：

```bash
AGENT_ADAPTOR_E2E=1 \
  go test -tags=e2e ./e2e -run TestPersistentProcessBDD -count=1 -v
```

普通 CI 可只编译并验证门禁，不产生外部调用：

```bash
go test -tags=e2e ./e2e -run TestPersistentProcessBDD -count=1 -v
```

## 来源与适配

本 workstream 复刻 `../agent-adaptor-internal` 的八个连续提交：Claude 常驻池与 Close、
CodeBuddy control 常驻与部分事件输出处理、Codex app-server 跨轮复用与 output schema、
真实 CLI BDD 规格与 runner。旧分支的中央 SDK、binding、SessionKey、Start/RunHandle 和双事件流
均未迁入；相同行为已经映射到 v1 的构造期 Driver 配置、Thread 协调、单一 Stream/Event 流和
Result/error 合同。

## 依赖选型

新增顶层测试依赖 `github.com/cucumber/godog v0.15.1`。

1. **可靠性**：使用 Cucumber 官方 Gherkin parser、中文 dialect、场景大纲、tag 过滤、hook
   和步骤匹配，避免自建解析器造成规格与执行漂移。
2. **可持续维护**：Godog 位于 Cucumber 官方组织，有独立版本、变更记录、测试和问题追踪。
3. **可局部化**：依赖只由 `//go:build e2e` 测试包导入，不进入 core、Driver runtime、公共 API
   或宿主二进制。
