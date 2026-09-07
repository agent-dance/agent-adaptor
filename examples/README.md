# Examples

这些示例使用最终公开 API。`offline` 使用内存 fake Driver，可直接运行；`threads/codec` 只检查纯数据映射，`inspect` 是只读探针，部分 profile 模式只物化资源。其余执行示例会调用本机真实的 Codex、Claude、Cursor 或 CodeBuddy CLI，需先安装并登录，且可能产生费用。

## 不调用 provider 的入门示例

```bash
go run ./examples/offline
go test -count=1 ./examples/...
```

`offline` 实际通过 `adaptor.New`、`Agent.Run` 和 `Agent.Stream` 执行，打印：

- 原生追加通道的构造默认、单次覆盖、空串清除，以及下一轮恢复默认；用户 Prompt 不变。
- 同一条 Event 流的 Question 应答、Capability 生命周期、Todo 全量快照与空表清除。
- 显式 `capabilityrecorder` 内存 Store 的准确 identity/RunID 查询；记录只有本次已接收的事实。
- 主动预算耗尽后的 `RunError.Reason`、部分 `Result.Text` 和 `Raw()`；整体 Policy 替换不改变墙钟上限。

[main.go](offline/main.go) 是消费者代码；[driver.go](offline/driver.go) 是独立的脚本化 SPI fixture。
所有事实与字节均为演示数据，不证明任何真实 provider 的能力、资源使用、审计完整性或授权。
[main_test.go](offline/main_test.go) 的可执行 Example 校验实际输出；另一个测试通过真实 SDK
管线核对 Run/Stream、回调应答、空快照、事件序号与终局。此测试无需 CLI 或凭据。

Todo 与原 ToolCall/Transcript 并存，不是 PlanReview。真正的 provider 支持按
[实际协议矩阵](../docs/streaming.md#provider-observation-support)判断；Codex 的
NativeInputAccepted 只证明 typed 输入接受，Cursor print 不提供 Skill/Todo 观测。

## 真实 CLI 示例

通用选择参数：

```bash
go run ./examples/quickstart -agent=claude
go run ./examples/quickstart -agent=cursor -command=/absolute/path/to/agent
```

也可通过 `AGENT_ADAPTOR_EXAMPLE_AGENT`、`CODEX_COMMAND`、`CLAUDE_COMMAND`、`CURSOR_COMMAND`、`CODEBUDDY_COMMAND` 及对应的 `*_MODEL` 环境变量选择 provider。

## Core

- `offline`：无需 provider 的完整消费者执行与错误示例。
- `quickstart`：最小 `Driver → Agent → Run → Result` 路径。
- `structured-output`：用 `adaptor.RunAs[T]` 推导 schema、校验并解码类型化结果。
- `inspect`：构造多个 Agent，读取 Environment、Models、ConfigSchema、Quota、Skills 等只读探针。
- `threads`：continue-or-start、resume-only 和 fork。
- `threads/codec`：不启动 CLI，查看 Driver 的 SessionCodec 与稳定 fingerprint 输入。
- `skills`：skill 发现、选择、单次 required skill 与 approval handler。
- `profiles`：完整 profile materialization；保留 `hook/` 与 `mcpserver/` 辅助进程。
- `profiles/resources`：profile-scoped resources 与 per-call 覆盖。
- `streaming`：一条 typed Event 流、Result 收尾与取消。
- `streaming/chat`：基于 Thread 的 typed-event 终端聊天；文本粒度取决于 Driver capability。

## Protocol bridges

- `web-chat`：最小 AG-UI SSE server 与原生浏览器页面。
- `web-chat/aguiclient`：Vite + `@ag-ui/client` 直连 Go SSE bridge。
- `web-chat/copilotkit`：CopilotKit、approval responder 与 typed Event recorder。
- `a2a-server`：本机 Agent 的 A2A server、streaming client 与 contextID→Thread 映射。

`adapter.stream.v1` 是已冻结的 wire schema 名称；A2A 示例中的 `DecodeAdapterEventV1` 因此有意保留 `V1`，它不是迁移期 Go API。

## Showcase

`showcases/team-agent-workflow` 是 live-only、多 Agent 委派示例，会产生多次付费模型调用，因此不进入自动 smoke。

## Smoke

```powershell
./examples/run_examples.ps1 -Agent codex
```

runner 总会先执行不启动 CLI 的 `threads/codec`。若所选 CLI 的 `--help` 不健康，其余 live 示例会明确跳过；健康时会按同一 provider 执行所有非 server 示例。`profiles -run=false -probe=false` 只同步并检查本地 profile，不调用模型。

上述 `go test -count=1 ./examples/...` 编译全部示例并执行实际的 fake/UI 合同测试，
不会运行各目录的 live main。live smoke 的成功仍需独立记录 CLI/平台/实际输出；
资源物化、原生输入接受和真实工具执行不能互相代替。也可只运行已有 Web UI 回归：

```bash
go test ./examples/web-chat/copilotkit
```
