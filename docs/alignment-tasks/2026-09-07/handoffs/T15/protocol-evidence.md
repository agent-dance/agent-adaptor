# T15 正式协议来源与证据层级

来源是只读已安装的官方 `@tencent-ai/codebuddy-code` 程序包，package.json version=`2.137.1`。2026-09-07 读取路径 `/opt/homebrew/lib/node_modules/@tencent-ai/codebuddy-code/dist/codebuddy.js`；文件 SHA-256=`7fa1c542cca9eebe9db1f6e70fe50c759bdd87aa2fcacda3b7007ec408b4958f`。仅静态程序文本，没有启动 CLI、访问用户 profile/凭据，或沿用 internal 的历史 live 结果。以符号与特征串定位，避免 minified 文件行号没有辨识度。

| 官方符号/特征 | 正式形状及本任务解释 |
|---|---|
| `StreamJsonMessageTransformers.transformToUserMessageFromFunctionResult` | `type=user` 的 message.content 包含 tool_result；tool_use_id 来自 callId，is_error 来自 incomplete 状态，content 为 string/text blocks。toolResult.rawResponse 存在时复制到块 `_meta.rawResponse`。顶层 parent_tool_use_id 同样来自 callId，不能当父。 |
| `transformToAssistantMessage` | assistant wrapper 的 tool_use 由正式 function call 转换；自身 parent_tool_use_id 为 null。本任务不推断未证明的嵌套图。 |
| `TaskCreateTool.execute` / `Task #` | 创建成功返回 task 与完整 todos；task.id 是 storage 真实 ID。渲染文案固定前缀 `Task #`、中段 ` created successfully: `、尾部 subject。输入有 subject/description；不会将请求顺序自行推算成 task ID。 |
| `task_update_tool_tasksToTodos`, `TaskUpdateTool.execute` | todos 项 id/content/status 分别来自 task.id/subject/status。更新输入 taskId；成功 rawResponse 含 task/todos，删除成功也含完整 todos。缺任务/无更新/异常为工具错误，不应用请求。 |
| `task_list_tool_tasksToTodos`, `TaskListTool.execute` | 完整 todos 有序列表；零任务仍返回 tasks 与 todos 空数组。空表是已确认清空。 |
| `TodoWriteTool` 的 `newTodos` schema 与 execute | 输入 oldTodos/newTodos；成功保存新列表，返回 `Todo list updated successfully`。从成功结果确认 newTodos，不把参数结束当执行完成。 |
| `McpUtils.buildFullToolName` / `createMcpTool` name getter | 按 `mcp__` + exact server + `__` + exact tool 构造；枚举已知 catalog 前缀，唯一候选才归属。没有套用 Claude Unicode alias 规则。 |
| 固定 internal `1921636:codebuddy/capability_observation_test.go` | Skill.command/skill、Task.subagent_type、tool_use/tool_result 的普通与 partial fixture，作为历史补充；不采用其 slash+init 与根 RunEventCapability 推断。 |

`alignment_observation_test.go`、`alignment_public_observation_test.go` 是按这些正式形状构造的确定性 fixture，不是声称抓到了真实付费运行。TaskCreate 的成功缺 ID fixture 明确保留合法 task 对象、真实 subject/status，发标记 synthetic 的展示 ID；未知文案或畸形对象不能确认创建。错误/空表/full metadata 等反例保留。

辅助官方文档：[CLI reference](https://www.codebuddy.ai/docs/cli/cli-reference) 确认 append 系统提示参数；[Tools reference](https://www.codebuddy.ai/docs/cli/tools-reference) 列出 TaskCreate/TaskUpdate/TaskList；[Agent SDK](https://www.codebuddy.ai/docs/cli/sdk) 说明程序化 stream/control 与环境隔离。网页是接口定位补充，正式版本字段以以上固定程序包及 fixture 为依据；具体 native/live 是否可用需 B06 在 G05 SHA 上实证。
