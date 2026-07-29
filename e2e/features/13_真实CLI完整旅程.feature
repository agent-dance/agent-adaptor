# language: zh-CN
@real_cli @sdk @process
功能: 用真实 CLI 复验多轮上下文和接力后的 conversation head
  这些场景是发布常驻能力前的最终门禁

  @smoke @journey @claude
  场景: Claude Code 真实三轮复用
    假如本机真实 Claude Code 已认证
    而且 Agent 使用临时 workspace、native 或只读 clone profile 和 memory Thread Store
    而且 使用默认常驻进程模式
    当 turn1 要求记住随机 token A 并只回复确认词
    而且 turn2 要求返回 A
    而且 turn3 要求再次返回 A
    那么 turn2 和 turn3 都应包含准确的 A
    而且三轮只应产生一个 Claude PID
    而且三轮 provider SessionID 应一致

  @smoke @journey @codebuddy
  场景: CodeBuddy 真实三轮复用
    假如本机真实 CodeBuddy 已认证
    而且 Agent 使用临时 workspace、隔离 config dir 和 memory Thread Store
    而且 使用默认常驻进程模式
    当 turn1 要求记住随机 token A 并只回复确认词
    而且 turn2 要求返回 A
    而且 turn3 要求再次返回 A
    那么 turn2 和 turn3 都应包含准确的 A
    而且三轮只应产生一个 CodeBuddy PID
    而且普通对话应通过 control NDJSON 发送

  @smoke @journey @codex
  场景: Codex 真实三轮复用
    假如本机真实 Codex 已认证
    而且 Agent 使用临时 workspace、有效 CODEX_HOME 和 memory Thread Store
    而且 使用默认常驻进程模式
    当 turn1 streaming 要求记住随机 token A
    而且 turn2 streaming 要求返回 A
    而且 turn3 streaming 要求再次返回 A
    那么 turn2 和 turn3 都应包含准确的 A
    而且三轮只应产生一个 app-server PID
    而且三轮应使用相同 threadId

  @claude
  场景: Claude 常驻到 schema 接力再回常驻不丢分支
    假如真实 Claude 常驻 turn1 已记住随机 token A
    当 turn2 使用 native JSON Schema 并要求返回包含随机 token B 的对象
    而且 turn3 恢复普通常驻并询问 A 和 B
    那么 turn3 应准确返回 A 和 B
    而且 turn2 启动前旧常驻 PID 已退出
    而且 turn2 结束后应产生使用最新 checkpoint 的预热 PID
    而且任何时刻 active writer 不超过 1

  @codebuddy
  场景: CodeBuddy 常驻到 schema 接力再回常驻不丢分支
    假如真实 CodeBuddy 常驻 turn1 已记住随机 token A
    当 turn2 使用 native JSON Schema 并要求返回包含随机 token B 的对象
    而且 turn3 恢复普通常驻并询问 A 和 B
    那么 turn3 应准确返回 A 和 B
    而且旧 control PID、一次性 PID 和预热 PID 不应并存为同一 session writer

  @codex
  场景: Codex 常驻到 WithSpawn 接力再回常驻不丢分支
    假如真实 Codex app-server turn1 已记住随机 token A
    当 turn2 使用 WithSpawn() 的一次性 app-server 并引入随机 token B
    而且 turn3 恢复默认常驻并询问 A 和 B
    那么 turn3 应准确返回 A 和 B
    而且旧 app-server、一次性 app-server 和后续常驻 app-server 不应同时写同一 thread
    而且新开的 provider resume 应解析到 turn3 conversation head

  @codex
  场景: Codex schema 轮无需接力且不丢上下文
    假如真实 Codex app-server turn1 已记住随机 token A
    当 turn2 streaming 使用 native outputSchema 并返回 token A
    而且 turn3 普通 streaming 再次询问 A
    那么三轮应使用同一 PID 和 threadId
    而且总 Spawn 数应为 1

  场景大纲: 配置漂移后真实 conversation 仍连续
    假如 "<driver>" 的真实 turn1 已记住随机 token A
    当修改一个无害 env binding 并执行 turn2
    而且 turn2 询问 A
    那么 turn2 应准确返回 A
    而且 turn2 应使用新 PID
    而且旧 PID 应在新 PID 启动前退出
    而且 provider ResumeID 应保持不变

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景: Agent Close 后没有真实 agent 或 MCP 残留
    假如 Claude、CodeBuddy 和 Codex 的最终旅程都已执行
    当各自 Agent.Close 完成
    那么由本轮 E2E 记录的全部 CLI PID 都应退出
    而且全部 MCP 子进程 PID 都应退出
    而且全部测试监听端口都应可重新绑定
    而且宿主原始 profile 和认证文件内容不应改变
