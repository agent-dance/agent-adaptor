# language: zh-CN
@real_cli @sdk @process @fault_injection
功能: 只有 prompt 发送前的常驻故障可以透明回退
  prompt 可能到达 provider 后结果未知
  Agent 不得用自动重放制造重复工具副作用

  场景大纲: 发送前杀死真实进程允许回退
    假如 "<driver>" 的真实常驻进程已启动但尚未接受本轮 prompt
    当执行器终止该真实 PID
    那么 Agent 可以通过历史一次性路径重新执行本轮
    而且 provider 最终只应收到一次 prompt
    而且调用方应得到正常结果或真实一次性路径错误

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: prompt 送出后断线不得重放
    假如 "<driver>" 已收到包含随机 RunID 的真实 user message
    而且 E2E MCP 计数服务可记录该轮工具调用
    当在 terminal result 前终止真实 CLI 进程组
    那么本轮应返回失败或 context 错误
    而且不应为同一 RunID 启动 fallback prompt
    而且 MCP 副作用次数不得超过 1
    而且失败进程不得留在 pool 中

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  @codex
  场景: Codex turn/start 已发出但响应丢失时不得重放
    假如真实 Codex app-server 已加载 thread
    当执行器在 turn/start 写入后且收到响应前终止进程
    那么该轮应按结果未知返回错误
    而且不得自动启动第二个 turn/start
    而且同一 RunID 的真实 prompt 投递计数应为 1

  场景大纲: context 在发送前取消不触发 fallback
    假如 "<driver>" 的 Run context 已取消
    而且本轮 prompt 尚未投递
    当 driver 开始执行
    那么应返回 context.Canceled
    而且不应启动新的真实 CLI

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: context 在发送后取消会处置整个进程组
    假如 "<driver>" 已把 prompt 送入真实常驻通道
    而且真实 CLI 已启动一个持有 stdout 或端口的 MCP 子进程
    当 Run context 被取消
    那么 leader 和 MCP 子进程都应退出
    而且本轮不得自动重放
    而且下一轮只能从 Thread Store 的健康 checkpoint 重建

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: terminal result 完整到达后进程退出不改变本轮成功
    假如 "<driver>" 已返回完整 terminal result 和有效 checkpoint
    当真实 CLI 在进入 idle 前自行退出
    那么本轮结果仍应成功
    而且 checkpoint 应正常持久化
    而且下一轮应检测 closed writer 并启动新进程

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
