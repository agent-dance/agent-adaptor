# quickstart-cli · walkthrough

> 这一份是**静态走查**（标准应该长什么样）。每次跑 spotlight 还会生成
> `.spotlight/quickstart-cli/last-run.md` 作为**动态事实**（这次实际看到了什么）。
> PR review 看本文件；事后排错对开两份对照。

## 1. 对位场景

凡是"喂一个 prompt 拿一个文本结果就走"的脚本类产品 —— SDK 表面只用到 `New(WithDefaultAgent(...))` + `sdk.Run(ctx, prompt)`：

- **deploy-bot**：CI 把 build 失败堆栈喂给 agent，回一段 root-cause 摘要贴回 PR
- **CI step**：lint 之后跑一遍 agent，让它给出 fix-suggestions 写到 job log
- **postcommit hook**：每次 commit 后让 agent 写一段 changelog 候选词
- **`git ai-fix`**：开发者拿单条命令解决一个不要紧的 lint warning
- **release-notes-generator**：cron 拉 git log diff，喂给 agent，吐 release notes draft

这些产品共同点：**不需要流式 / 不需要会话 / 不需要审批 / 不需要多 agent 路由**。一条 prompt 进，一段 `Output` 出。

## 2. 一条命令

```bash
go run ./examples/quickstart-cli -agent=codex
```

切到 `-agent=claude` 或 `-agent=cursor` 同一份代码完整通跑。其他 flags：

| flag | 默认 | 作用 |
| --- | --- | --- |
| `-prompt` | `"Reply with a short acknowledgement for the quickstart example."` | 单条 prompt |
| `-model` | 各 driver 默认（codex=gpt-5.4 / claude=sonnet-4 / cursor=gpt-5）| 模型覆盖 |
| `-timeout` | `3m` | 整个 run 的硬超时 |

## 3. 终端产物

跑完后终端按这个顺序输出四块独立的可截图区域 + 三段统一收尾 banner。

### 3.1 四联屏（Output / Summary / RawStreams / Transcript 同一次 run 的四个层次）

```
┌─ Output ─ what your end-user sees ──────────────────────────────────
│ The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path …
└─

┌─ Summary ─ what your notification feed shows ───────────────────────
│ The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path …
└─

┌─ RawStreams.Stdout ─ raw protocol bytes (head 3 lines / 456B) ──────
│ {"type":"thread.started","thread_id":"019dd993-310d-7ed1-a352-57c2675b1bc9"}
│ {"type":"turn.started"}
│ {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"The quickstart exa…
│ ... +1 lines
└─

┌─ Transcript ─ parsed semantic items ────────────────────────────────
│ init × 1
│ system × 1
│ assistant × 1
│ result × 1
│ stderr × 1
└─
```

读法（这就是 §3.4 输出合同的可视化）：

| 面板 | 字段 | 含义 |
| --- | --- | --- |
| **Output** | `result.Output` | 最终 assistant-facing 文本，没有 stdout dump、没有摘要拼接、没有 result JSON。等同你家产品要给终端用户看的那段 |
| **Summary** | `result.Summary` | 短摘要，适合列表 / 通知 / issue comment。它**故意与 Output 分开**；本次 run 中 codex 给两者写了同一段，但合同允许它们不同 |
| **RawStreams.Stdout** | `result.RawStreams.Stdout` | adapter 收到的完整原始 stdout 字节（head 3 行 + 总字节数）。审计、replay、协议调试都靠它 |
| **Transcript** | `result.Transcript` 的 Kind 直方图 | adapter 解析出的语义条目（assistant / init / result / stderr / system 等）。timeline / message-list UI 直接消费它 |

### 3.2 三段 banner（统一收尾）

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Your deploy-bot just got a one-shot answer; pick the layer your product needs to render.
对位：deploy-bot · CI step · postcommit hook · git ai-fix

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/quickstart-cli/quickstart-cli.json
- .spotlight/quickstart-cli/last-run.md
- examples/quickstart-cli/30-second-recipe.md
- examples/quickstart-cli/walkthrough.md

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/web-chat-stream -mode=cli -agent=codex
```

## 4. 文件系统产物

```
examples/quickstart-cli/
├── main.go               # ≤ 250 行；渲染 + 写盘 + 三段 banner
├── 30-second-recipe.md   # 12 行 main.go 复制走的最小集成
└── walkthrough.md        # 静态走查（本文件）

.spotlight/quickstart-cli/
├── quickstart-cli.json   # 6 字段稳定 schema：output / summary / raw_stdout_bytes / transcript_kinds / driver_type / exit_code
└── last-run.md           # 本次 run 的真实快照（动态，不入 git）
```

`quickstart-cli.json` 样本：

```json
{
  "driver_type": "codex",
  "exit_code": 0,
  "output": "The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.",
  "raw_stdout_bytes": 456,
  "summary": "The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.",
  "transcript_kinds": {
    "assistant": 1,
    "init": 1,
    "result": 1,
    "stderr": 1,
    "system": 1
  }
}
```

字段稳定，可直接 `jq` / 入仓库 / 喂下游脚本。

## 5. 落到我家产品的哪里

| 这边的物件 | 对应你家产品的什么 panel |
| --- | --- |
| **Output 面板** | 用户面：聊天气泡 / 弹窗 / IDE 侧边栏的 assistant 消息体 |
| **Summary 面板** | 通知面：Slack 卡片标题、邮件 subject、issue comment 一行摘要 |
| **RawStreams.Stdout 面板** | 审计面：原始字节落 ELK / S3 / 合规库；replay & 协议调试都靠它 |
| **Transcript 面板** | 渲染面：timeline / message-list / 多步骤展示；按 Kind 路由到不同 UI 组件 |
| **`quickstart-cli.json`** | 下游 ETL：smoke runner / 监控管道 / 周报机器消费的稳定 schema |
| **`30-second-recipe.md`** | onboarding：复制走的 12 行 main.go，新员工 5 分钟跑通，0 SDK 内部知识 |

集成模板（伪代码）：

```go
sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})))
result, err := sdk.Run(ctx, prompt)
if err != nil { /* 你家错误处理 */ }

renderToChat(result.Output)            // 用户面
notifySlack(result.Summary)            // 通知面
audit.Append(result.RawStreams.Stdout) // 审计面
ui.RenderTimeline(result.Transcript)   // 渲染面
```

四条调用，对应自家产品的四个 panel。这就是 §3.4 输出分层在工程上的全部意义。

---

## 附录 · 不展示什么

为了把"30 秒最短路径"的故事讲清楚，spotlight #1 故意**不**演这些（去对应 spotlight 看）：

- 流式 token / SSE 网关 / AG-UI 前端集成 → `examples/web-chat-stream`
- 多 driver 路由 / 多租户身份 / Admin 控制面 → `examples/multi-agent-platform`（即将上线）
- 危险操作审批 / 同步 reject / 异步 approve / 超时 abort → `examples/human-in-the-loop`
- 任务剧本 / profile resources / skills × instructions × hooks 叠加 → `examples/task-recipes`
