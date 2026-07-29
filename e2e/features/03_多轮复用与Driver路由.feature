# language: zh-CN
@real_cli @sdk @process
功能: 三个 Driver 按各自正式通道复用真实 CLI

  @smoke
  场景大纲: 三轮普通对话只启动一个真实 CLI
    假如 "<driver>" 使用默认常驻进程模式并具有稳定 Thread key
    当 turn1 要求记住随机 token
    而且 turn2 要求返回该 token
    而且 turn3 要求再次返回该 token
    那么三个回答都应符合各轮 prompt
    而且总共只应产生一个 ProcessInfo(ProcessSpawn)
    而且三个 RunID 应不同
    而且 provider ResumeID 应保持一致
    而且进程 PID 应保持一致

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  @codebuddy
  场景: CodeBuddy 开启常驻后普通对话改走 control 通道
    假如 CodeBuddy 使用本机真实 CLI 且 使用默认常驻进程模式
    当执行不含 HITL 的普通对话
    那么启动参数应包含 --input-format=stream-json
    而且启动参数应包含 --output-format=stream-json
    而且 prompt 应通过 NDJSON user 帧发送而不是 argv
    而且 terminal result 后 stdin 应保持打开

  @codebuddy
  场景: CodeBuddy 使用 WithSpawn 时普通对话保持 headless
    假如 CodeBuddy 使用本机真实 CLI 且 Agent 配置了 WithSpawn()
    当执行不含 HITL 的普通对话
    那么应使用历史 headless 参数
    而且不应启用 control stdin

  @codex
  场景: Codex 第二轮只发送 turn/start
    假如 Codex 已完成真实 app-server initialize 和 thread/start
    当同一 Thread 执行第二个 turn
    那么不应再次发送 initialize 或 initialized
    而且不应发送 thread/resume
    而且应在同一 threadId 上发送一次 turn/start

  @codex
  场景: Codex WithSpawn 调用使用一次性 app-server
    假如 Codex 使用默认常驻进程模式
    当一轮调用显式传入 WithSpawn()
    那么旧 app-server 应在新进程启动前退出
    而且该次进程不得注册为后续轮次的常驻 writer

  场景大纲: 每轮结果只包含当前轮内容
    假如 "<driver>" 的 turn1 和 turn2 复用同一真实进程
    当读取 turn2 的 Result.Text、Result.Raw() 和 Result.Transcript()
    那么 Result.Text 不应包含 turn1 的 assistant 文本
    而且 Result.Raw() 不应包含 turn1 已归档的原始输出
    而且 Result.Transcript() 不应重复 turn1 的语义条目

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |
