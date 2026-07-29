# language: zh-CN
@real_cli @sdk @process
功能: 启动时配置漂移触发真实进程安全重建
  会话身份保持不变
  但任何被 CLI 在启动时读取的配置变化都必须替换 writer

  场景大纲: 通用启动配置变化触发重建
    假如 "<driver>" 已有健康 live writer
    而且旧 PID 的退出可由操作系统观测
    当下一轮把 "<配置项>" 从值 A 改为值 B
    那么旧 writer 应先退出并完成 wait
    而且新 writer 应在旧 PID 退出之后启动
    而且 active writer 数量从未超过 1
    而且 provider ResumeID 应保持不变
    而且新一轮应使用值 B

    例子:
      | driver   | 配置项                         |
      | claude    | model                          |
      | claude    | effort                         |
      | claude    | 有效环境变量                   |
      | claude    | permission/browser 运行形态    |
      | codebuddy | model                          |
      | codebuddy | effort                         |
      | codebuddy | 有效环境变量                   |
      | codebuddy | permission/control 运行形态    |
      | codex     | model                          |
      | codex     | reasoning effort               |
      | codex     | fast mode                      |
      | codex     | 有效环境变量                   |
      | codex     | sandbox/approval policy        |

  场景大纲: profile 资源内容变化触发重建而非 session 拒绝
    假如 "<driver>" 使用临时可写 profile
    而且同一 session 已有健康 live writer
    当 "<资源>" 的实际内容发生变化
    那么调用不应因为 SessionID 不兼容而失败
    而且旧进程应安全退出
    而且新进程应继续相同 provider ResumeID
    而且真实 CLI 应能读取更新后的资源

    例子:
      | driver   | 资源             |
      | claude    | skill            |
      | claude    | MCP 配置         |
      | claude    | settings         |
      | claude    | instructions     |
      | codebuddy | skill            |
      | codebuddy | MCP 配置         |
      | codebuddy | settings         |
      | codebuddy | agent/hook       |
      | codex     | skill            |
      | codex     | config.toml      |
      | codex     | AGENTS.md        |
      | codex     | agent/hook       |

  场景大纲: settings 文件增加删除和 symlink 目标变化都被识别
    假如 "<driver>" 已从临时 profile 启动 live writer
    当相关 settings 文件被 "<操作>"
    那么下一轮应启动新的真实 CLI PID
    而且旧 PID 应已退出

    例子:
      | driver   | 操作                  |
      | claude    | 新增                  |
      | claude    | 删除                  |
      | claude    | 修改 symlink 目标内容 |
      | codebuddy | 新增                  |
      | codebuddy | 删除                  |
      | codebuddy | 修改 symlink 目标内容 |
      | codex     | 新增                  |
      | codex     | 删除                  |
      | codex     | 修改 symlink 目标内容 |

  场景大纲: 等价配置不产生虚假重建
    假如 "<driver>" 已有健康 live writer
    当 "<变化>" 不改变 CLI 实际启动输入
    那么下一轮应复用原 PID

    例子:
      | driver   | 变化                              |
      | claude    | EnvBinding 仅调整顺序             |
      | codebuddy | EnvBinding 仅调整顺序             |
      | codex     | EnvBinding 仅调整顺序             |
      | claude    | settings 重写为相同内容           |
      | codebuddy | settings 重写为相同内容           |
      | codex     | settings 重写为相同内容           |
      | codebuddy | CLI 自己刷新 plugins marketplace  |
      | codex     | 每轮 outputSchema 发生变化        |

  场景大纲: profile 目录身份变化仍拒绝 resume
    假如 "<driver>" 的 checkpoint 已记录 profile identity
    当同一 SessionID 改用另一个有效 profile 目录
    那么应返回 ErrResumeRejected 或既有 session incompatible 结果
    而且不得先写入新 profile 资源
    而且不得启动新 writer

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |
  场景大纲: secret 参与指纹但不出现在可观测输出
    假如 "<driver>" 的真实 CLI 通过 env 接收随机 secret
    当 secret 改变并触发 writer 重建
    那么新 PID 应与旧 PID 不同
    而且 Event、错误、Result.Raw() 和测试日志中都不应出现 secret 明文

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  @manual_upgrade
  场景大纲: 真实 CLI 版本切换触发重建
    假如本机同时安装了 "<命令>" 的两个真实版本
    而且第一轮使用真实版本 A
    当重新用真实版本 B 的 Driver 构造 Agent
    那么旧进程应退出
    而且下一轮应启动版本 B
    而且 provider session 应被继续而不是重新创建

    例子:
      | 命令      |
      | claude    |
      | codex     |
      | codebuddy |
