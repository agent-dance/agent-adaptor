# language: zh-CN
@real_cli @sdk
功能: HITL 在真实常驻双向通道上保持完整语义

  场景大纲: Permission Ask 在同一真实进程内完成
    假如 "<driver>" 已有健康常驻 writer
    而且策略要求 Permission Ask
    当 prompt 要求真实 agent 在临时 workspace 执行一个需要许可的无害操作
    而且宿主批准 ApprovalRequest
    那么请求和响应应具有相同 request ID
    而且工具执行应继续使用原 PID
    而且本轮不应产生额外 Spawn

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  场景大纲: Permission 拒绝不污染 session
    假如 "<driver>" 已有健康 checkpoint 和 live writer
    当真实 agent 请求工具许可且宿主拒绝
    那么 *RunError 应标记 HumanDecision rejection
    而且拒绝后的 provider head 不应持久化为健康 checkpoint
    而且该 writer 不应继续复用

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  场景大纲: HITL timeout 有界结束并回收进程
    假如 "<driver>" 正在等待 Permission 或 Question 决策
    当 HumanDecision timeout 到期且 OnTimeout 为 Abort
    那么 *RunError 应标记 timed out
    而且真实 CLI 进程组应退出
    而且不得自动重放 prompt

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  场景大纲: OnApproval 和 Event 消费行为等价
    假如 "<driver>" 可以触发真实 Permission、PlanReview 或 Question
    当分别通过 OnApproval 和 Event 中的 ApprovalRequest 响应
    那么两种宿主接法都应让同一进程继续运行
    而且都应产生 ApprovalRequest 和 NoticeApprovalResolved 事件

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  场景大纲: 上一轮 Approval 状态不泄漏到下一轮
    假如 "<driver>" 的 turn1 已完成一次 HITL 决策
    当同一进程执行不需要 HITL 的 turn2
    那么 turn2 不应重复发出 turn1 request ID
    而且 turn2 不应复用旧 Approval 响应

    例子:
      | driver   |
      | claude    |
      | codebuddy |

  @codebuddy
  场景: CodeBuddy 常驻 terminal result 不关闭 control stdin
    假如真实 CodeBuddy control 进程完成 turn1 terminal result
    当 turn2 user frame写入相同 stdin
    那么写入应成功
    而且 turn2 应在相同 PID 上执行

  @codex
  场景: Codex 不支持的 Ask 在启动前失败
    假如 Codex Driver 请求当前 Descriptor 不支持的 HITL Ask
    当宿主调用 Stream 并读取 Result()
    那么应返回 ErrHumanDecisionModeUnsupported
    而且不应启动真实 codex 进程
