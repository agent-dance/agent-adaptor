# agent CLI 常驻进程 BDD 规格

本目录描述 `agent-adaptor` 常驻进程能力的端到端验收规格。所有带 `@real_cli` 的场景必须调用本机真实安装的 Claude Code、Codex 和 CodeBuddy，不允许使用 fake CLI、shell shim、测试二进制或预制协议输出替代 agent。

## 本机基线

编写规格时检测到的版本：

- Claude Code：`2.1.159`
- Codex CLI：`0.144.6`
- CodeBuddy：`2.117.2`

执行器每次运行仍必须重新解析真实路径并记录实际版本，不能把上述版本当作固定输出。当前 Codex 本机凭据会在模型请求阶段返回 401；这种情况应归类为“环境未就绪”，不能伪造成协议测试通过，也不能改用 fake CLI 兜底。

## 真实 CLI 硬约束

1. 用 `exec.LookPath` 解析 `claude`、`codex`、`codebuddy`，再用 `filepath.EvalSymlinks` 记录最终二进制。
2. `CommonConfig.Command` 只能为空或等于解析出的真实 CLI 路径。
3. 禁止把 Command 指向临时目录、测试进程、shell 脚本或录制回放程序。
4. 故障注入通过取消 context、向真实 CLI 的 PID/进程组发信号、修改真实 profile/settings，或停止真实 MCP 连接完成。
5. 可以使用专用 E2E MCP 计数服务记录工具副作用；被测 agent 本身仍必须是真实 CLI。
6. 所有 workspace、settings 和可写 profile 必须放在测试临时目录；认证材料使用 native profile 或只读 clone，测试不得修改宿主原始凭据。
7. prompt 使用随机不可猜 token，避免模型凭先验猜中“记忆”断言。
8. 每个场景结束时调用 `Agent.Close`，并确认 CLI、MCP 子进程和监听端口均已回收。

## Godog 执行器

BDD runner 和真实 CLI 步骤实现位于 `e2e/*_test.go`，使用显式 `e2e` build tag，避免普通
`go test ./...` 意外调用外部模型或消耗额度。默认运行三家各自的最终三轮 smoke 旅程：

```bash
AGENT_ADAPTOR_E2E=1 \
  go test -tags=e2e ./e2e -run TestPersistentProcessBDD -count=1 -v
```

通过标签筛选单个 provider，或显式选择其他已经自动化的场景：

```bash
AGENT_ADAPTOR_E2E=1 AGENT_ADAPTOR_E2E_TAGS='@smoke and @claude' \
  go test -tags=e2e ./e2e -run TestPersistentProcessBDD -count=1 -v

AGENT_ADAPTOR_E2E=1 AGENT_ADAPTOR_E2E_TAGS='@smoke' \
  go test -tags=e2e ./e2e -run TestPersistentProcessBDD -count=1 -v
```

可用环境变量：

- `AGENT_ADAPTOR_E2E=1`：除 `e2e` build tag 外的第二道真实调用门禁。
- `AGENT_ADAPTOR_E2E_TAGS`：Godog 标签表达式。
- `AGENT_ADAPTOR_E2E_PATHS`：逗号或平台 path-list 分隔的 feature 路径。
- `AGENT_ADAPTOR_E2E_FORMAT`：Godog formatter，默认 `pretty`。
- `AGENT_ADAPTOR_E2E_TURN_TIMEOUT` / `AGENT_ADAPTOR_E2E_CLOSE_TIMEOUT`：Go duration。
- `AGENT_ADAPTOR_E2E_CLAUDE_MODEL`、`AGENT_ADAPTOR_E2E_CODEBUDDY_MODEL`、
  `AGENT_ADAPTOR_E2E_CODEX_MODEL`：本机账号可访问的 smoke 模型覆盖。

环境检查、真实模型请求或认证失败会明确记录为 environment unavailable 并 skip；runner
绝不会把命令替换为 fake CLI。场景执行时 drain 唯一的 `Events()`，从真实
`ProcessInfo{Kind: ProcessSpawn}` 记录 PID，并在 After hook 中使用新的 timeout context 调用 `Agent.Close`。

## 依赖选型

E2E runner 使用 Cucumber 官方维护的 `github.com/cucumber/godog`，仅由 `e2e` build tag
下的测试包引用：

- **可靠性**：直接使用官方 Gherkin parser、tag expression、scenario hook 和步骤匹配，避免手写解析器造成中文关键字、场景大纲和生命周期语义漂移。
- **可持续维护**：Godog 属于 Cucumber 官方组织，有独立 release、问题追踪和 Go 测试集。
- **可局部化**：依赖只存在于真实 CLI E2E runner，不进入 Agent core、driver 或宿主公共 API。

## 观测合同

步骤实现通过 Agent 事件和操作系统只读信息观测行为：

- `ProcessInfo{Kind: ProcessSpawn}.Data["pid"]`：进程 PID。
- `Notice{Kind: NoticeInvocation}`：启动参数以及脱敏后的 env key。
- `ps` / `Getpgid`：PID、PGID 和进程存活状态。
- Thread Store：record ID、provider ResumeID、checkpoint 前后值。
- Event：每轮 RunID、turnId、文本 delta、HITL 请求与响应。
- E2E MCP 计数服务：prompt 发送后真实工具副作用次数。

任何 env value、API key、认证 token 和完整 auth 文件都不得进入测试日志。

## 标签

- `@smoke`：最短真实双轮验证。
- `@real_cli`：必须使用真实本地 CLI。
- `@claude`、`@codex`、`@codebuddy`：provider 选择。
- `@sdk`：从公共 Agent API 验证。
- `@process`：验证 PID、PGID、进程退出或并存关系。
- `@fault_injection`：向真实进程注入取消、断线或 kill。
- `@slow`：模型调用较多或需要等待 idle 阈值。
- `@manual_upgrade`：需要本机同时准备两个真实 CLI 版本。

## 现有真实 CLI 入口

仓库已有的 Go live test 可作为步骤实现的底层入口：

```bash
go test -tags=claude_live ./claude -run 'Persistent|Streaming' -count=1 -v
go test -tags=codebuddy_live ./codebuddy -run Persistent -count=1 -v
go test -tags=codex_live ./codex/appserver -run Persistent -count=1 -v
```

Gherkin 场景是最终验收合同；已有 live test 只覆盖其中一部分，不能因为某个 Go live test 通过就跳过其余场景。
