# language: zh-CN
@real_cli @sdk
功能: 常驻路径保持历史输出合同和 Streaming 语义

  场景大纲: One-shot 与 persistent 的结果分层一致
    假如 "<driver>" 可以用真实 CLI 对固定 prompt 返回简短文本
    当分别以 one-shot 和 persistent 执行等价轮次
    那么两条路径的 Result.Text 都只包含 assistant-facing 文本
    而且 Text 都不应拼接 Summary 或 provider 终局 payload
    而且 Result.Raw() 都应存在并只属于本轮
    而且 Result.Transcript() 都应来自正式 provider 协议
    而且 Summary 和 error 路径的语义应一致

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
  场景大纲: Run 与 Stream Result 返回等价结果
    假如 "<driver>" 已开启常驻
    当同一类真实请求分别通过 Run 和 Stream().Result() 执行
    那么 Text、Raw()、Transcript()、Summary 和 error 应同样可用
    而且两条调用方式都应正确持久化 checkpoint

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 每轮 Streaming 都形成独立完整序列
    假如 "<driver>" 的真实进程将被连续复用两轮
    当两轮都通过 Stream 执行
    那么每轮应分别产生 run.started 和 run.finished
    而且文本 delta 按顺序拼接后应等于该轮 Result.Text
    而且所有 Event.Meta().RunID 应携带当前 RunID
    而且 Driver 产生的 TextDelta.Role 应保持零值
    而且 turn1 的延迟事件不得进入 turn2

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: turn2 的原始流不包含 turn1
    假如 "<driver>" 在 turn1 向 stdout 和 stderr 输出了可识别随机 token
    当相同进程完成 turn2
    那么 turn2 Result.Raw() 不应包含 turn1 token
    而且 turn2 Result.Transcript() 不应包含 turn1 assistant 条目

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  @codex
  场景: Codex 未知 notification 仍保留 Raw
    假如真实 Codex app-server 在一轮中发出当前 Agent 未建模的 notification
    当 Translator 分派该 notification
    那么 Notice.Text 应保留原 method
    而且 Notice.Data 应保留脱敏后的 params
    而且不得影响后续已知事件顺序

  场景大纲: invocation 事件不泄露环境变量值
    假如 "<driver>" 通过 env 接收随机 secret
    当真实运行产生 Invocation、Spawn 和 Chunk 事件
    那么 Invocation 可以包含 env key
    但是任何事件都不应包含 secret value

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
