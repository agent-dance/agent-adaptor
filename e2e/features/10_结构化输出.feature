# language: zh-CN
@real_cli @sdk
功能: 结构化输出在常驻和接力路径中保持正确

  场景大纲: Claude 和 CodeBuddy native schema 使用安全接力
    假如 "<driver>" 已有健康常驻 writer
    当下一轮请求 native JSON Schema 并要求返回随机整数 token
    那么旧 writer 应先退出
    而且真实一次性 CLI 应返回 provider-native 结构化 payload
    而且 Result.Decode() 应通过 Agent schema 校验
    而且有效 checkpoint 应触发无 prompt 预热
    而且下一轮普通对话应复用预热进程

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  @codex
  场景: Codex native schema 直接进入 turn start
    假如真实 Codex app-server 已有健康 thread 和 live PID
    当下一轮 streaming 请求 native JSON Schema
    那么 turn/start.params.outputSchema 应等于规范化 schema
    而且不应执行 SuspendAndWait
    而且 PID 和 threadId 应保持不变
    而且 Result.Decode() 应得到 native schema 对应对象

  @codex
  场景: Codex 普通轮和 schema 轮共享进程
    假如 Codex turn1 是普通 streaming 对话
    当 turn2 使用 schema A
    而且 turn3 使用 schema B
    而且 turn4 恢复普通对话
    那么四轮应只产生一次 Spawn
    而且只有 turn2 和 turn3 的 turn/start 包含各自 outputSchema

  场景大纲: Prompt validate 不改变底层常驻形态
    假如 "<driver>" 已有健康 live writer
    当下一轮使用 PromptValidateOutput
    那么 schema 约束应被注入 prompt
    而且本轮应使用已有 PID
    而且最终 JSON 应由 Agent 本地验证

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 非法 schema 在启动 driver 前被拒绝
    假如 "<driver>" 已有健康 live writer
    当宿主提交语法错误或无法编译的 JSON Schema
    那么应返回 ErrInvalidOutputSchema
    而且不应产生新的 Spawn
    而且既有 writer 应保持健康并可供下一轮复用

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 无效结构化结果遵守 OnInvalid 策略
    假如 "<driver>" 的真实模型被要求产生一个无法满足 schema 的结果
    当 OnInvalid 为 "<策略>"
    那么 Result.Decode() 或 *RunError 应携带校验失败
    而且运行结果应为 "<结果>"
    而且健康 session 不应被失败轮推进

    例子:
      | driver   | 策略           | 结果                       |
      | claude    | fail_run       | *RunError                  |
      | claude    | return_invalid | 无运行失败但返回校验错误   |
      | codebuddy | fail_run       | *RunError                  |
      | codebuddy | return_invalid | 无运行失败但返回校验错误   |
      | codex     | fail_run       | *RunError                  |
      | codex     | return_invalid | 无运行失败但返回校验错误   |
