# language: zh-CN
@real_cli @sdk @fault_injection
功能: 失败运行不污染健康 Thread

  场景大纲: provider 失败保留旧 checkpoint
    假如 "<driver>" 已有健康 checkpoint H1
    而且 live writer 正从 H1 执行下一轮
    当真实 CLI 在 prompt 送出后以 provider failure 或断线结束
    那么 Thread Store 中仍应保存 H1
    而且失败进程应被丢弃
    而且下一轮应从 H1 新建真实 writer

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 首轮失败不创建 Thread 映射
    假如 "<driver>" 的 Thread key 尚不存在
    当首轮真实运行未产生有效 checkpoint
    那么 Thread Store 不应出现该 Thread key 映射
    而且 pool 中不应注册 provider ResumeID

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 工具结果中的伪 session 字段不能成为 checkpoint
    假如真实 "<driver>" 调用了 E2E MCP 工具
    而且该工具结果嵌套包含一个伪造 session_id
    当执行器在 provider 正式 checkpoint 到达前取消运行
    那么 checkpoint 应为空或 Valid=false
    而且工具 JSON 中的伪 session_id 不应被递归采用

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 非零退出不更新健康 Thread
    假如 "<driver>" 已有健康 checkpoint
    当真实 CLI 以非零状态退出
    那么本轮不得持久化新 checkpoint
    而且下轮应继续旧 ResumeID

    例子:
      | driver   |
      | claude    |
      | codebuddy |
      | codex     |

  场景大纲: 本地策略失败也不得推进健康 Thread
    假如 "<driver>" 已有健康 checkpoint
    当真实模型输出触发 "<本地失败>"
    那么 error 应为携带部分 Result 的 *RunError
    而且 Thread Store 中健康 checkpoint 不应前移
    而且已前移的 live writer 不应继续复用

    例子:
      | driver   | 本地失败                    |
      | claude    | prompt-validate JSON 无效   |
      | codebuddy | prompt-validate JSON 无效   |
      | codex     | JSON Schema 本地校验失败    |
