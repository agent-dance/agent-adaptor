# language: zh-CN
@real_cli @sdk
功能: 常驻能力声明和默认路由符合 v1
  常驻复用只发生在统一 Driver.Run 管线内部
  Claude、CodeBuddy 和 Codex 默认允许常驻，WithSpawn() 明确切换为单次进程

  场景大纲: Descriptor 如实声明常驻能力
    假如宿主用 "<driver>" 构造了 Agent
    当测试读取该 Driver 的 Descriptor
    那么 Process.Persistent 应为 <支持>

    例子:
      | driver    | 支持  |
      | claude    | true  |
      | codex     | true  |
      | codebuddy | true  |
      | cursor    | false |

  场景大纲: WithSpawn 强制每轮使用新进程
    假如 "<driver>" 使用本机真实 CLI 和 memory Thread Store
    而且 Agent 构造时显式使用 WithSpawn()
    当同一 Thread 连续执行两轮最小对话
    那么两轮都应成功产生有效 checkpoint
    而且应观测到两个不同的真实 CLI PID
    而且每轮结束后对应 PID 都应退出

    例子:
      | driver    |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 没有 Thread Store 时按无状态路径逐轮启动
    假如 "<driver>" 使用本机真实 CLI
    而且 Agent 未注入 Thread Store
    当直接通过 Agent 连续执行两轮调用
    那么调用不应因为缺少 Thread Store 报错
    而且应观测到两个不同 PID
    而且不得在进程池中留下 live writer

    例子:
      | driver    |
      | claude    |
      | codex     |
      | codebuddy |

  场景大纲: 默认常驻只作用于显式 Thread
    假如 "<driver>" 已按默认配置构造 Agent
    当直接通过 Agent 执行两轮无状态调用
    那么两轮都应走单次进程路径
    而且不应持久化 Thread 映射
    而且不应留下空闲进程

    例子:
      | driver    |
      | claude    |
      | codex     |
      | codebuddy |
