# language: zh-CN
@real_cli @sdk @process
功能: 并发调用、漂移、idle 和 Close 不破坏 writer 隔离

  场景大纲: 同一 Thread key 的并发运行遵守现有 lease 合同
    假如 "<driver>" 的一个真实 turn 正在执行
    当另一个 goroutine 对同一 Thread key 发起运行
    那么第二次运行应返回 ErrThreadBusy 或按既有协调规则等待
    而且同一 provider session 的 active writer 永远不超过 1

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 不同 Thread key 可以并行
    假如 "<driver>" 已开启常驻
    当两个不同 Thread key 同时执行真实模型请求
    那么应观测到两个不同 PID 和 provider session
    而且两个 Thread 的 Result.Text、Event 和 checkpoint 不应串线

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 配置漂移和普通下一轮并发时只产生一个 replacement
    假如 "<driver>" 已有健康 live writer
    当一个 goroutine修改 env 并开始下一轮
    而且另一个 goroutine同时对同一 Thread 开始普通下一轮
    那么单 writer/lease 规则应拒绝或串行其中一轮
    而且不得同时启动两个 replacement writer

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: idle timer 与新一轮竞争不误杀活跃 writer
    假如 "<driver>" 的 idle timer 即将到期
    当新一轮在阈值附近取得 writer lock
    那么该轮要么复用旧 PID 要么在旧 PID 完全退出后重建
    而且不得杀死已经接受 prompt 的新一轮

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: Close 与 prewarm 竞争不允许复活进程池
    假如 "<driver>" 的一次性轮次正在完成并准备 prewarm
    当宿主同时调用 Agent.Close
    那么 prewarm 不得在 Close 后注册新进程
    而且 Agent 结束时所有相关 PID 都应退出

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景: 全部真实 CLI 并发场景通过 race detector
    假如真实 CLI live test 已按 provider 标签启用
    当使用 go test -race 执行并发、Close、idle 和重建场景
    那么不应报告 driver pool、writer lock、timer 或 sink 的 data race
