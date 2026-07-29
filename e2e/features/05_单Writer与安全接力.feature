# language: zh-CN
@real_cli @sdk @process
功能: 同一 provider session 在任何时刻只有一个真实 writer

  场景大纲: SuspendAndWait 等待旧进程真正退出
    假如 "<driver>" 的真实常驻进程正在写同一 session
    而且执行器已记录其 PID 和 PGID
    当下一轮需要临时一次性通道
    那么 Agent 应先关闭旧输入通道
    而且应终止旧进程组
    而且应等待旧 PID 从进程表消失
    而且临时进程的启动时间应晚于旧进程退出时间
    而且 active writer 数量从未超过 1

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 一次性形态成功后按最新 checkpoint 预热
    假如 "<driver>" 已有健康常驻 writer
    当通过 "<一次性形态>" 完成一轮并产生有效 checkpoint
    那么旧 writer 应先退出
    而且一次性进程应正常退出
    而且 Agent 应启动一个不发送 prompt 的预热进程
    而且预热进程应加载该轮最新 provider ResumeID
    而且下一轮应复用预热 PID

    例子:
      | driver   | 一次性形态                  |
      | claude    | native JSON Schema          |
      | claude    | MaxTurnsPerRun               |
      | codebuddy | native JSON Schema          |
      | codebuddy | MaxTurnsPerRun               |
      | codex     | WithSpawn() app-server      |

  场景大纲: 非 shared runtime 使用安全接力
    假如 "<driver>" 已有健康常驻 writer
    当下一轮请求 per-run runtime service
    那么旧 writer 应先完成 SuspendAndWait
    而且本轮应走历史一次性通道
    而且 runtime service 报告应正常返回
    而且有效 checkpoint 应触发预热

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 失败或无效 checkpoint 不允许预热
    假如 "<driver>" 已暂停旧 writer 并进入一次性路径
    当一次性运行出现 "<结果>"
    那么不得注册新的 live writer
    而且 Thread Store 中的健康 checkpoint 应保持不变

    例子:
      | driver   | 结果                |
      | claude    | 非零退出            |
      | claude    | provider failure    |
      | codebuddy | 非零退出            |
      | codebuddy | 在正式 session_id 前取消  |
      | codex     | 在 turn checkpoint 前取消 |
      | codex     | provider failure    |

  场景大纲: 预热本身不产生用户副作用
    假如 "<driver>" 的一次性轮次刚刚成功
    而且 E2E MCP 计数服务记录了该轮工具调用次数
    当 Agent 完成预热但尚未开始下一轮
    那么 MCP 计数不应增加
    而且 provider transcript 不应新增 user message

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
