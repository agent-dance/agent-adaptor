# language: zh-CN
@sdk
功能: 不支持的平台和 Cursor 保持明确边界

  场景: Cursor Descriptor 明确不支持常驻
    假如宿主用 cursor.Driver 构造了 Agent
    当宿主读取 Descriptor.Process.Persistent
    那么结果应为 false

  场景: Cursor 默认仍逐轮执行
    假如本机安装了真实 Cursor agent CLI
    当同一 Thread key 连续执行两轮
    那么每轮都应启动新的真实 Cursor PID
    而且不应尝试构造不存在的 stdin 多轮协议

  @windows
  场景大纲: Windows 上支持 driver 静默走逐轮路径
    假如测试运行于 Windows
    而且 "<driver>" 使用本机真实 CLI 并开启 常驻进程
    当同一 Thread key 连续执行两轮
    那么不应返回 unsupported 错误
    而且两轮应使用不同 PID
    而且每个进程树都应通过 taskkill 语义正确回收

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
  @windows
  场景大纲: Windows context 取消终止真实进程树
    假如 "<driver>" 的真实 CLI 已启动子进程
    当 Run context 被取消
    那么 leader 和子进程都应退出
    而且 stdout/stderr drain 不应无限阻塞

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |
