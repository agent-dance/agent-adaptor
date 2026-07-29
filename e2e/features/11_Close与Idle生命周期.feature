# language: zh-CN
@real_cli @sdk @process
功能: Agent Close、idle 和 context 取消确定性回收真实进程树

  场景大纲: Close 优雅关闭单个常驻进程
    假如 "<driver>" 已完成一轮并保持真实 CLI 空闲
    当宿主用有效 context 调用 Agent.Close
    那么 Agent 应先关闭该 CLI 的输入通道
    而且应等待真实 PID 退出
    而且 Close 应返回 nil

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
  场景: Close 同时关闭默认和命名 agent
    假如默认 Claude、命名 CodeBuddy 和命名 Codex 都有 live writer
    当调用一次 Agent.Close
    那么三个真实 CLI 进程都应退出
    而且每个 driver pool 都应为空

  场景: 没有常驻进程时 Close 是成功 no-op
    假如 Agent 尚未运行任何 agent
    当调用 Agent.Close
    那么应返回 nil
    而且不应启动或终止任何外部进程

  场景: Close 重复和并发调用都幂等
    假如 Agent 拥有多个 live writer
    当多个 goroutine 同时调用 Close
    而且 Close 完成后再次调用 Close
    那么所有调用都应有界返回
    而且每个 PID 只应被回收一次
    而且不应发生 data race

  场景大纲: Close 超过 GracePeriod 后终止整个进程组
    假如 "<driver>" 的真实 CLI 拉起了一个持有端口的 E2E MCP 子进程
    而且该进程树在 stdin EOF 后未于 GracePeriod 内退出
    当宿主调用 Close
    那么 Agent 应终止对应 PGID
    而且 leader 和 MCP 子进程都应退出
    而且原监听端口应可重新绑定

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: Close context 到期仍会发出强制终止
    假如 "<driver>" 的进程树拒绝优雅退出
    当 Close context 到期
    那么 Close 应返回 context 错误
    而且强制终止信号应已发送到进程组
    而且后台 waiter 最终应完成进程回收

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景: Close 后所有执行入口明确拒绝新运行
    假如 Agent.Close 已经开始
    当分别调用 Agent.Run、Agent.Stream().Result()、Thread.Run 和 Thread.Stream().Result()
    那么每次调用都应返回 ErrAgentClosed
    而且不得静默重启任何进程池

  @slow
  场景大纲: 忘记 Close 时 idle timeout 回收进程
    假如 "<driver>" 完成了一轮且宿主不调用 Close
    当空闲时间超过 driver idle 阈值
    那么真实 CLI PID 应退出
    而且其 pool entry 应移除
    而且下轮应通过 provider ResumeID 启动新进程

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: Run context 取消回收 leader 和孙进程
    假如 "<driver>" 正在执行真实工具调用并拥有 MCP 子进程
    当 Run context 被取消
    那么 Run 应有界返回 context 错误
    而且 CLI leader、MCP 子进程和持有 pipe 的孙进程都应退出
    而且不得自动重放本轮 prompt

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
