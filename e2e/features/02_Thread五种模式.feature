# language: zh-CN
@real_cli @sdk
功能: 常驻进程不改变五种 Thread 语义
  Thread key 仍是业务稳定键
  Thread record ID 和 provider ResumeID 仍由统一执行管线协调

  场景大纲: continue_or_start 首轮创建次轮复用
    假如 "<driver>" 已开启常驻并注入空 Thread Store
    当使用同一 Thread key 执行 turn1 和 turn2
    那么 turn1 应持久化有效 provider ResumeID
    而且 turn2 应继续同一个 ResumeID
    而且两轮应使用同一个真实 CLI PID

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |
  场景大纲: ResumeOnly 命中健康 Thread
    假如 "<driver>" 已有健康 checkpoint 和 live writer
    当使用 Thread(key, ResumeOnly()) 执行下一轮
    那么应继续原 provider ResumeID
    而且不应新增真实 CLI 进程

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  场景大纲: ResumeOnly 缺失时不启动 CLI
    假如 "<driver>" 的 Thread Store 中不存在目标 Thread key
    当以 Thread(key, ResumeOnly()) 执行调用
    那么应返回 ErrThreadNotFound
    而且不应产生 ProcessInfo(ProcessSpawn)

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  场景大纲: NewThread 不复用旧 conversation
    假如 "<driver>" 已有一个健康 Thread 和 live writer
    当使用相同业务键通过 NewThread(key) 执行
    那么应创建新的 Thread record 和 provider ResumeID
    而且新 Thread 应启动独立真实 CLI writer
    而且旧 Thread 的 checkpoint 不应改变

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  场景大纲: Fork 不污染源 Thread
    假如 "<driver>" 的源 Thread 已记住随机 token A
    当通过 Thread.Fork(newKey) 让分支记住随机 token B
    那么分支应能回答 A 和 B
    而且源 Thread 后续只能回答 A
    而且源 Thread checkpoint 不应被分支覆盖

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  场景大纲: Agent 无状态调用不读写已有 Thread
    假如 "<driver>" 已有一个记住随机 token 的健康 Thread
    当直接通过 Agent 询问该 token
    那么结果不应依赖已有 Thread 上下文
    而且已有 checkpoint 和 live writer 映射不应改变

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |
