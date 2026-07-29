# language: zh-CN
@real_cli @sdk
功能: 使用本机真实 agent CLI 执行端到端验收
  为了避免协议替身掩盖真实 CLI 行为
  作为 Agent 维护者
  我要求所有常驻进程场景都解析并启动本机真实 agent 命令

  场景大纲: 发现并记录真实 CLI
    假如本机 PATH 中存在命令 "<命令>"
    当测试执行器解析命令的最终路径和版本
    那么最终路径不应位于测试临时目录
    而且最终路径不应是测试二进制或 shell shim
    而且版本输出不应为空
    而且后续场景应将该真实命令绑定到 "<driver>"

    例子:
      | driver   | 命令      |
      | claude    | claude    |
      | codex     | codex     |
      | codebuddy | codebuddy |

  场景大纲: 认证状态在执行模型场景前被验证
    假如 "<driver>" 已绑定本机真实 CLI
    当执行一个只读且无工具调用的最小模型请求
    那么认证成功时场景继续执行
    而且认证失败时结果应标记为环境未就绪
    而且不得切换到 fake CLI 或录制回放
    而且日志不得包含认证 secret

    例子:
      | driver   |
      | claude    |
      | codex     |
      | codebuddy |

  场景: 每个场景使用隔离的可写目录
    假如三个真实 CLI 均已完成环境检查
    当测试创建 workspace、profile 和 settings
    那么所有可写路径都应位于该场景的临时根目录
    而且宿主原始认证文件只能被只读使用或克隆
    而且场景结束后临时目录可以完整清理

  场景: 真实进程可由 Agent 事件和操作系统交叉确认
    假如一次真实 agent 运行已经发出 ProcessInfo{Kind: ProcessSpawn}
    当执行器读取事件中的 PID 并查询操作系统进程表
    那么 PID 对应的可执行文件应是已解析的真实 CLI
    而且其进程组 ID 应可用于后续生命周期断言
