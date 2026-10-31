# 宿主就绪计划：以 multica 为参照宿主

状态：草案（2026-10-31）。本文件是规划记录，不是使用文档；条目落地后，其合同以 `AGENTS.md`、godoc、`api-reference.md` 与对应专题文档为准，本文件同步标注完成状态。

## 0. 阅读指引

每个条目使用固定结构：

- **现状**：代码中的事实与位置。
- **问题与后果**：不处理会在什么情况下出什么错。
- **方案**：怎么改，边界在哪。
- **API 变更**：标注为「无」「新增（additive）」「行为变化」或「breaking」，并给出改前/改后代码。所有 API 均为提案，名称在评审时可调整。
- **用户怎么用**：宿主侧代码。
- **关键场景**：引用第 3 节的场景编号。
- **验收**：完成判据。

优先级：P0 必须先做（宣传与事实不符或收尾）；P1 可靠性；P2 宿主能力补全；P3 Provider 覆盖；P4 用真实宿主验收。

## 1. 背景与基线

`agent-adaptor` 与 multica 的 `server/pkg/agent` 都是「把多种本地 coding agent CLI 统一成一个执行接口」的适配层。multica 是一个把 issue 分配给 agent、由 daemon 在用户机器上长时间无人值守执行的平台，它对适配层的要求代表了嵌入式宿主的上限需求。

| 维度 | multica `pkg/agent` | agent-adaptor（当前） |
|---|---|---|
| 执行接口 | `Backend.Execute` → `Session{Messages, Result, Supplement, ...}` | `Runner.Run/Stream` → `Stream{Events, Result, Cancel}` |
| Provider 数 | 27（约 12 个 ACP） | 6 |
| 中途追加指令 | `Session.Supplement` + `SupplementReady` | 无 |
| 活性控制 | 墙钟、空闲、首轮无进展、握手、中断、工具预算，睡眠感知时钟 | 仅 `WithTimeout`（墙钟） |
| 会话续接 | `ResumeRejected` / `ResumeRejectedTransient`，可检测性名单，连续性提示文案由宿主提供 | `ErrResumeRejected`，continue-or-start 静默回退一次 |
| 失败分类 | `taskfailure` 约 30 种 | 5 种 `FailureReason` |
| 用量 | 按模型、int64、缓存读写分列、1e-10 USD 成本 | 单份、int、无缓存写入、千分之一美元 |
| 模型目录 | 默认值、每模型 effort 档位、service tier | `ID`/`Label` |
| 进程清理 | Windows Job Object；`CleanupRequired`、`TerminalObserved`；清理未确认是独立失败原因 | Windows `taskkill /T`，Unix 进程组；setsid 逃逸为已知残留风险 |
| 会话指针 | 运行中拿到 SessionID 即落库；暂时性失败保留指针 | 只在成功 Finalize 时写入 |

agent-adaptor 的优势（multica 没有或较弱）：唯一执行管线、Thread lease 与 fingerprint、单一 typed Event 流、Approval exactly-once、结构化输出协商、宿主 Tools、profile 隔离、API golden 冻结、adaptertest 一致性套件。本计划不改变这些设计，只补齐长时运行可靠性与宿主运营能力。

**基线验证（2026-10-31）**：Windows、Go 1.26.5 下 `go vet ./...` 与 `go test -count=1 ./...` 全部通过（含当前未提交改动）。未运行：`-race`、Linux/macOS、live 测试。

## 2. 边界：哪些归 SDK，哪些归宿主

坚持 `AGENTS.md` 第 1、16 节：SDK 提供**机制与真实信号**，宿主决定**策略与业务**。

| 归 SDK | 归宿主 |
|---|---|
| 进程树回收、输出上限、协议解析、握手/中断超时 | 任务队列、调度、重试次数与退避节奏 |
| 活性看门狗机制（检测并以明确原因终止） | 看门狗时长取值、按 provider 的不同配置 |
| provider 中立的失败分类与 `Retryable`/`RetryAfter` 信号 | 是否重试、通知谁、写入什么 issue 评论 |
| 会话连续性信号（续上了 / 新开了 / 为什么） | 连续性提示文案、是否告知最终用户 |
| 中途追加指令的投递与确认 | 追加指令的来源（评论、聊天）与持久化 |
| 用量的准确、口径一致的数字 | 定价、计费、配额策略 |
| Thread store 合同、一致性套件、参考实现 | 生产数据库 schema 与迁移 |

## 3. 关键用户场景

后续条目以编号引用这些场景。

- **H1 Issue 工单 Agent（multica 主场景）**：宿主把 issue 分配给 agent，Thread key 取 `workspace/issue/agent`，一次运行可能持续数小时，没有人盯着；每次新评论触发一轮续接；结果写回 issue。
- **H2 Web 聊天与中途补充**：用户在 Agent 运行中继续打字补充要求，不希望取消重来。
- **H3 运行时管理面板**：宿主列出本机已安装的 agent CLI、版本、健康状态、可选模型与 effort 档位，让用户为每个 agent 选择。
- **H4 用量计费与配额**：宿主按运行统计 token 与成本，按团队计费，配额耗尽时给出明确提示。
- **H5 宿主崩溃、重启与升级恢复**：daemon 被杀、机器睡眠、宿主升级后，进行中的会话要能续上，不能留下孤儿进程。
- **H6 Windows 桌面宿主**：CLI 以 npm `.cmd`/`.ps1` 形式安装，路径和参数含空格与特殊字符，命令行长度受限。
- **H7 多 Agent 编排**：Leader 委派子 Agent，需要纠偏子 Agent、统计各自用量。
- **H8 小众/新 CLI 接入**：用户要用 Kimi、Kiro、Qoder、Grok 等 CLI，或公司内部包装过的 CLI（wrapper 命令）。

---

## 4. P0：收尾与宣传一致性

### P0-1 落地未提交改动

- **现状**：工作树中 66 个文件有实质改动（Pi/OpenCode profile agents、durable MCP、OpenCode 权限恢复、Pi/OpenCode fuzz 门禁、`driver_api.golden` 新增 75 行等），另有约 700 个文件仅为 CRLF 差异，`.gitattributes` 已修改。
- **问题与后果**：真实改动被行尾噪音淹没，无法评审，也无法基于它继续开发；`AGENTS.md` 第 15 节要求脏工作树逐项验收并形成可追溯提交。
- **方案**：
  1. 单独一个提交完成 `.gitattributes` 与行尾归一化（`git add --renormalize .`），确认 `git diff --ignore-cr-at-eol` 为空后提交。
  2. 剩余实质改动按主题拆成原子提交：Pi profile agents、OpenCode profile agents、durable MCP、OpenCode 权限、CI fuzz、文档。
  3. 每个提交核对 `CHANGELOG.md`、`driver_api.golden`、`root_api.golden`。
- **API 变更**：以 `driver_api.golden` 现有改动为准（driver SPI 新增声明），本条不引入新变更。
- **验收**：`git status` 干净；每个提交可独立通过 `go test ./...`。

### P0-2 Codex app-server 单次运行用量被重复累计

- **现状**：`codex/appserver/run.go` 处理 `thread/tokenUsage/updated` 时，直接取 `body.TokenUsage.Total` 作为本次运行用量，并且不按 `turnId` 过滤。`Total` 是**整个 thread 的累计值**。multica 的同类实现（`pkg/agent/codex.go` 的 `updateThreadTokenUsage`）只接受本轮 `turnId` 的通知，首个快照取 `last`，之后取 `total` 的单调增量。
- **问题与后果**：Codex 常驻进程默认在显式 Thread 下跨轮复用，第 N 轮的 `Result.Usage` 会包含前 N-1 轮的全部 token。宿主按运行计费时被成倍多计，`README` 宣称的「Usage 用量与审计信息」不成立。resume 场景下 `Total` 是否从 rollout 恢复历史需 live 测试确认，但常驻跨轮必然累计。
- **方案**：按 multica 的口径改为本轮增量：记录本轮 `turnId`，忽略其他 turn 的通知；首个快照用 `last`，后续用 `total` 与上次快照的差值累加。批处理 `codex exec --json` 路径同样核对 `turn.completed.usage` 的语义。
- **API 变更**：行为变化（数值修正），无签名变化。CHANGELOG 记为 Fixed。
- **用户怎么用**：无需改代码；`result.Usage` 变为本次运行的真实消耗。
- **关键场景**：H1、H4、H7。
- **验收**：合同测试覆盖「同一 Thread 连续两轮，第二轮 Usage 只含第二轮」「混入其他 turnId 的通知被忽略」「重复快照不重复计」；live 测试对照 Codex 自身日志。

### P0-3 跨 Driver 用量口径不一致

- **现状**：
  - Claude：`InputTokens = input_tokens`（**不含**缓存读取），`CachedInputTokens = cache_read_input_tokens`，`cache_creation_input_tokens` 被丢弃。
  - Codex：`InputTokens` 是 OpenAI 口径，**包含** `CachedInputTokens`。
  - OpenCode：`InputTokens = input + cache.read + cache.write`，`OutputTokens = output + reasoning`。
  - `driver.Usage` 的 godoc 没有定义这些字段是互斥还是包含关系。
- **问题与后果**：同样的任务换一个 Driver，`InputTokens` 的含义就不同，宿主的跨 provider 成本对比、配额计算都是错的；Claude 的缓存写入（计费是普通输入的 1.25 倍）完全丢失。这与「换 Driver 其余代码不动」的宣传相矛盾。
- **方案**：在 godoc 中固定 v1 口径并逐个 Driver 对齐：
  - `InputTokens`：本次运行**全部**输入 token，含缓存读取与缓存写入。
  - `CachedInputTokens`：`InputTokens` 中命中缓存读取的部分（子集）。
  - `OutputTokens`：全部输出 token，含 reasoning/thinking。
  - 新增字段见 P2-4（缓存写入子集、reasoning 子集、按模型拆分）。
  - Claude 改为 `input + cache_read + cache_creation`；OpenCode 已符合；Codex 已符合。
  - 新增 adaptertest 条款：Usage 各子集不得大于总量。
- **API 变更**：行为变化（Claude/CodeBuddy 的 `InputTokens` 数值变大），无签名变化。
- **用户怎么用**：
  ```go
  u := result.Usage
  uncachedInput := u.InputTokens - u.CachedInputTokens // 在所有 Driver 上含义相同
  ```
- **关键场景**：H4、H7。
- **验收**：每个 Driver 的 parser 测试断言口径；`docs/api-reference.md` 写明口径。

### P0-4 文档与实现漂移

- **现状**：`README*.md` 第 216 行写「Claude、CodeBuddy、Codex 跨轮复用同一个进程」，`AGENTS.md` 第 5 节写 Claude、CodeBuddy、Codex、Pi、OpenCode 都默认复用。
- **问题与后果**：宿主按 README 判断 Pi/OpenCode 每轮新进程，可能做出错误的资源规划或误以为不需要 `Agent.Close`。
- **方案**：以 Descriptor 的 `Process.Persistent` 为唯一事实来源，修正五种语言 README；新增守卫测试：遍历内置 Driver 的 Descriptor 生成能力表，与 README 中标记区块比对，不一致即失败。同一机制扩展到 MCP、Skills、Instructions、StructuredOutput 能力列。
- **API 变更**：无。
- **验收**：守卫测试进入 CI。

### P0-5 Pi/OpenCode 配置面与其他 Driver 不一致

- **现状**：`codex/claude/cursor/codebuddy` 的 `Config` 嵌入 `CommonConfig`（`ExtraArgs`、`GracePeriod`、`Timeout`、`Instructions`、`PromptTemplate` 等），`pi.Config` 与 `opencode.Config` 只有 `Command/CWD/Env` 加各自字段。
- **问题与后果**：宿主写一套「给所有 Driver 加额外 CLI 参数 / 调整取消宽限期」的通用代码时，在 Pi/OpenCode 上无处可设，只能分支处理；若有人以为 `WithTimeout` 之外还能用 Config 级 Timeout，会被静默忽略。
- **方案**：在两个 Config 中**直接追加字段**（不改为嵌入，避免破坏现有复合字面量）：`ExtraArgs []string`、`GracePeriod time.Duration`。对不适用的 `CommonConfig` 字段，不追加，并在 godoc 与能力矩阵中写明。`ExtraArgs` 统一经过 P2-6 的保留参数校验。
- **API 变更**：新增（additive）。
  ```go
  // 改后
  pi.Driver(pi.Config{
      Model:       "anthropic/claude-sonnet-4-5",
      ExtraArgs:   []string{"--verbose"},
      GracePeriod: 10 * time.Second,
  })
  ```
- **关键场景**：H3、H8。
- **验收**：`driver_api.golden` 更新；两个 Driver 的 ExtraArgs 冲突校验测试。

### P0-6 CI 矩阵补齐发布门禁

- **现状**：门禁要求 Linux `-race`，本次基线只在 Windows 跑了非 race 测试；Pi/OpenCode fuzz 的 CI 改动尚未提交。
- **方案**：GitHub Actions 矩阵 `ubuntu/macos/windows × go1.26.x`；Linux 跑 `-race`；fuzz 各 30s；live 测试保持双门（环境变量 + build tag）。
- **API 变更**：无。
- **验收**：三平台绿灯是后续所有条目合入的前提。

---

## 5. P1：长时运行可靠性

### R1 进程树回收与清理确认

- **现状**：`internal/processx`：Unix 用 `Setpgid` 后 `kill(-pgid)`；Windows 用 `taskkill /T /F`，失败回退为只杀 leader。注释中承认：子进程 `setsid` 脱离进程组后仍持有 stdout/stderr 管道时，`clihelper` 的 drain 永远等不到 EOF，`cmd.Wait` 不会被调用，`WaitDelay` 也不会生效，运行挂住，Thread lease 被持续续期，下一轮返回 `ErrThreadBusy`。
- **问题与后果**：
  - H1/H5：一个挂住的运行会让该 issue 永久「忙」，直到宿主进程重启；宿主重启后孤儿进程仍在写会话文件。
  - Windows 上 leader 先退出后，`taskkill /T` 无法沿父子关系找到已被重新挂靠的孙进程。
- **方案**：
  1. **Windows**：启动时把进程放进 Job Object，设置 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`；取消时 `TerminateJobObject`；用 `JobObjectBasicAccountingInformation` 查询剩余活动进程数，作为「清理已确认」的依据。宿主进程意外退出时，句柄关闭即杀全树。`golang.org/x/sys/windows` 已是间接依赖，只在 `processx` 的 windows 文件中引用。
  2. **Unix**：leader 退出后启动 drain 截止计时（默认等于 `GracePeriod`），到期强制关闭管道读端，使 drain 返回；Linux 可选 `PR_SET_CHILD_SUBREAPER` 回收逃逸进程（作为后续增强）。
  3. 清理结果作为一等信号对外暴露（见 API）。
  4. Thread：若上一轮清理未确认，同 key 的下一轮在启动前先再执行一次回收并校验，仍不能确认则返回明确错误，而不是与旧进程并发写同一个会话。
- **API 变更**：新增（additive）。
  ```go
  // 新增 Notice kind
  const NoticeProcessCleanup = "process.cleanup"
  // Data: {"confirmed": bool, "remaining": int, "elapsed_ms": int64}

  // 新增 FailureReason
  const ReasonCleanupUnconfirmed FailureReason = "cleanup_unconfirmed"

  // 新增错误
  var ErrPreviousRunCleanupUnconfirmed error
  ```
  规则：运行已有终局结果、但清理未确认时，`Result` 照常返回，同时发出 `confirmed=false` 的 Notice（见 R8）；运行没有终局结果且清理未确认时，返回 `RunError{Reason: ReasonCleanupUnconfirmed}`。
- **用户怎么用**：
  ```go
  for ev := range stream.Events() {
      if n, ok := ev.(adaptor.Notice); ok && n.Kind == adaptor.NoticeProcessCleanup {
          if confirmed, _ := n.Data["confirmed"].(bool); !confirmed {
              alerting.Warn("agent 子进程可能残留", "run", stream.RunID())
          }
      }
  }
  ```
- **关键场景**：H1、H5、H6。
- **验收**：adaptertest 新增「孙进程 setsid 后持有管道」「leader 先退出」「宿主 kill -9」三个用例（Unix/Windows 各一套 fake CLI）；运行必须在 `GracePeriod + 1s` 内返回，lease 必须释放。

### R2 原始输出内存无上限

- **现状**：`internal/clihelper` 用 `bytes.Buffer` 全量保存 stdout/stderr 以实现 `Result.Raw()`；常驻进程路径同样累积。
- **问题与后果**：H1 中数小时的运行、工具大量输出（编译日志、测试输出、`cat` 大文件）时，单次运行可占用数百 MB 到 GB；daemon 同时跑多个任务时会被 OOM。
- **方案**：引入保留策略，默认有上限：
  - 默认：每条流内存上限 16 MiB，超出后保留头部 1 MiB 与尾部 15 MiB，`Truncated=true`，记录总字节数与丢弃字节数。
  - 需要完整原文时，宿主显式指定落盘目录：完整写入文件，内存中只保留尾部；文件生命周期归宿主（SDK 不自动删除宿主指定目录中的文件）。
  - 终局 payload（`Raw().Terminal`）永不截断。
  - 修改 `AGENTS.md` 第 8 节「`Raw()` 必须提供完整 stdout、stderr」为「完整，或明确标记截断并给出完整文件路径」。
- **API 变更**：新增 + 行为变化（默认截断）。
  ```go
  // 新增构造期选项
  func WithRawRetention(r RawRetention) Option

  type RawRetention struct {
      MaxMemoryBytes int64  // 每条流；0 表示默认 16 MiB；-1 表示不限制（旧行为）
      SpillDir       string // 非空时完整写入 <SpillDir>/<RunID>.{stdout,stderr}
  }

  // driver.RawStreams 追加字段
  type RawStreams struct {
      Stdout, Stderr   string
      Terminal         *TerminalPayload
      StdoutTruncated  bool
      StderrTruncated  bool
      StdoutTotalBytes int64
      StderrTotalBytes int64
      StdoutFile       string // 仅在 SpillDir 生效时非空
      StderrFile       string
  }
  ```
- **用户怎么用**：
  ```go
  agent := adaptor.New(codex.Driver(codex.Config{}),
      adaptor.WithRawRetention(adaptor.RawRetention{SpillDir: "/var/lib/myhost/raw"}),
  )
  res, _ := agent.Run(ctx, prompt)
  raw := res.Raw()
  if raw.StdoutTruncated {
      archive.Upload(raw.StdoutFile) // 审计需要完整原文
  }
  ```
- **关键场景**：H1、H4（审计）、H5。
- **验收**：压力测试输出 1 GiB 时常驻内存不超过上限的 2 倍；`Run` 与 `Stream.Result` 的 RawStreams 逐字段相等。

### R3 统一的有上限行读取

- **现状**：Claude/CodeBuddy 常驻路径 `bufio.Reader.ReadString('\n')` 不设上限；OpenCode server 用 32 MiB scanner；Codex app-server 用 `json.Decoder`。上限与超限行为各不相同。
- **问题与后果**：provider 输出一行超大 JSON（例如工具返回了整个文件内容的 base64）时，有的路径占满内存，有的路径 scanner 报 `token too long` 后协议中断，表现为不明原因的失败。
- **方案**：在 `internal/` 提供唯一的行读取器（初始 1 MiB 缓冲，上限 32 MiB，与 multica 对齐），所有 Driver 的 stdout 协议读取必须经过它；超限时丢弃该行剩余部分，发出 `ProcessInfo`/`Notice` 记录，并让 Driver 按协议错误处理：该行是终局帧则运行失败（`ReasonProtocolError`，见 R7），否则继续。新增架构守卫测试，禁止 Driver 直接 `ReadString`/`NewScanner` 读 provider stdout。
- **API 变更**：新增 `ReasonProtocolError`（并入 R7）。
- **关键场景**：H1。
- **验收**：每个 Driver fuzz/合同测试含「超长非终局行」「超长终局行」用例。

### R4 Codex 握手、线程恢复与中断超时

- **现状**：`codex/appserver` 的 `initialize`、`thread/start`、`thread/resume`、`turn/start` 只受调用方 ctx 约束；中断写死 500ms；常驻进程打开写死 30s、关闭 10s。
- **问题与后果**：
  - 慢主机或 Windows Defender 扫描时 `initialize` 偶发卡住，没有墙钟超时的宿主会一直等。
  - 长历史 thread 的 `thread/resume` 需要回放 rollout，常常超过 30s，被写死的常驻打开超时误杀。
  - 500ms 中断在慢机器上经常来不及收到 `turn/completed`，导致取消后状态不确定。
- **方案**：在 `codex.Config` 暴露三个超时，零值使用内置默认（握手 30s、线程握手 120s、中断 10s）；握手超时若发生在 prompt 交付前且进程树已确认回收（R1），按 `AGENTS.md` 第 5 节允许的「交付前安全回退一次」自动重试一次。
- **API 变更**：新增（additive）。
  ```go
  type Config struct {
      CommonConfig
      Model           string
      ReasoningEffort ReasoningEffort
      FastMode        bool
      // 新增
      HandshakeTimeout       time.Duration // initialize、turn/start
      ThreadHandshakeTimeout time.Duration // thread/start、thread/resume、thread/fork
      InterruptTimeout       time.Duration // turn/interrupt 到 turn/completed
  }
  ```
- **用户怎么用**：
  ```go
  codex.Driver(codex.Config{
      ThreadHandshakeTimeout: 5 * time.Minute, // 超长工单续接
      InterruptTimeout:       20 * time.Second,
  })
  ```
- **关键场景**：H1、H5、H6。
- **验收**：fake app-server 测试覆盖「initialize 不响应」「resume 慢但在预算内」「中断后迟到的 turn/completed」；重试只发生在 prompt 交付前。

### R5 活性看门狗

- **现状**：唯一的时间约束是 `WithTimeout`（墙钟，ctx deadline）。
- **问题与后果**：H1 的宿主通常不愿设墙钟（任务可能合法地跑 3 小时），但 provider 卡死（网络半开、模型服务挂起、`turn/started` 后再无输出）时，没有任何机制终止，issue 永远显示「进行中」。反过来，设了墙钟又会误杀仍在正常输出的长任务。multica 为此有空闲看门狗、首轮无进展、工具预算三套机制。
- **方案**：新增双作用域选项，与 `WithTimeout` 并列：
  - `Idle`：连续多长时间没有语义事件（文本、thinking、工具开始/结束、审批、子 Agent 更新）即判定空闲。`ProcessInfo`（原始字节）不算语义进度，避免心跳噪音掩盖卡死。
  - `FirstProgress`：从运行开始到第一个语义事件的上限（覆盖「启动后一直沉默」）。
  - `ToolIdle`：有工具在执行时，改用这个更长的预算（`npm install`、长测试期间没有输出是正常的）；0 表示工具执行期间不计空闲。
  - 等待人工审批期间暂停计时（审批超时只归 `Policy.Approvals`，不形成第二套策略）。
  - 时钟使用不计主机睡眠的单调时钟：笔记本合盖再打开不能被判为空闲。
  - 触发后以明确原因结束运行，与墙钟超时区分。
- **API 变更**：新增（additive）。
  ```go
  func WithLiveness(l Liveness) SharedOption

  type Liveness struct {
      Idle          time.Duration // 0 = 不启用
      FirstProgress time.Duration // 0 = 不启用
      ToolIdle      time.Duration // 0 = 工具执行期间不计空闲
  }

  const (
      ReasonTimeout    FailureReason = "timeout"     // WithTimeout 墙钟
      ReasonIdle       FailureReason = "idle"        // Liveness.Idle / ToolIdle
      ReasonNoProgress FailureReason = "no_progress" // Liveness.FirstProgress
  )
  ```
  合并规则：调用处整体覆盖构造处（与 `WithTimeout` 一致）。
- **用户怎么用**：
  ```go
  agent := adaptor.New(claude.Driver(claude.Config{}),
      adaptor.WithLiveness(adaptor.Liveness{
          Idle:          10 * time.Minute,
          FirstProgress: 3 * time.Minute,
          ToolIdle:      45 * time.Minute,
      }),
  )

  _, err := thread.Run(ctx, prompt)
  var runErr *adaptor.RunError
  if errors.As(err, &runErr) && runErr.Reason == adaptor.ReasonIdle {
      issue.Comment("agent 长时间无响应，已终止；可重新指派")
  }
  ```
- **关键场景**：H1、H2、H5。
- **验收**：fake driver 测试覆盖「沉默启动」「中途卡死」「长工具无输出不误杀」「审批等待不计时」「睡眠不计时」（注入时钟）。

### R6 Windows 命令行转义与长度限制

- **现状**：`processx.PrepareCommand` 遇到 `.cmd/.bat` 时改为 `cmd.exe /d /s /c call <shim> args...`。prompt 走 stdin，但 Claude 的 `--json-schema <JSON>`、MCP 配置、Codex 的 `-c key=value` 等仍在 argv 中。
- **问题与后果**：
  - `cmd.exe` 会对 `%`、`^`、`&`、`|`、`<`、`>`、`"` 做二次解析，npm shim 的 `%*` 再次分词，JSON/TOML 参数可能被改写或截断，表现为 provider 报参数错误，或参数被注入为额外命令。multica 在 issue #6082 中遇到同样问题，做法是把 `.cmd` 改写为同目录的 `.ps1` 用 PowerShell `-File` 启动。
  - Windows 命令行上限 32767 字符、Unix `E2BIG`：大 schema 或大量 MCP 声明时启动失败，只得到裸 errno。
- **方案**：
  1. `.cmd` 存在同名 `.ps1` 时优先走 PowerShell `-File`；两者都不可用时才走 `cmd.exe`，并对参数做 cmd 元字符转义（`^` 前缀加整体引号）。
  2. 各 Driver 把大体积参数改为文件传递（Claude `--mcp-config` 支持文件路径；schema 放 profile/临时文件，provider 不支持文件时才走 argv）。
  3. 启动错误为 `E2BIG` / `ERROR_FILENAME_EXCED_RANGE` 时包装为可识别错误。
- **API 变更**：新增（additive）。
  ```go
  var ErrCommandLineTooLong error // errors.Is 可识别；同时保留原 errno 链
  ```
- **用户怎么用**：
  ```go
  if errors.Is(err, adaptor.ErrCommandLineTooLong) {
      return fmt.Errorf("工具/MCP 声明过多，请精简：%w", err)
  }
  ```
- **关键场景**：H6。
- **验收**：Windows CI 上用含 `%PATH% & | ^ "` 的 schema 与路径做端到端测试；超长 argv 返回 `ErrCommandLineTooLong`。

### R7 provider 中立的失败分类

- **现状**：`FailureReason` 只有 `agent_error`、`approval_denied`、`approval_timeout`、`cancelled`、`policy_violation`。`WithTimeout` 超时返回 `context.DeadlineExceeded` 基础设施错误，**不携带部分 Result**。
- **问题与后果**：
  - 宿主无法区分「额度用完」（重试无用，应通知用户）、「限流」（等一会儿重试）、「鉴权失效」（需要重新登录）、「上下文溢出」（应开新会话或压缩）、「CLI 未安装/版本太旧」。multica 为此维护了约 30 种原因并据此决定重试与提示。
  - 超时运行的已有输出、用量、transcript 丢失，宿主无法在 issue 里展示「做到了哪一步」。
- **方案**：
  1. 扩展 `FailureReason`（所有 Driver 共用，只根据 provider 正式错误字段与 stderr 尾部判定，不猜测）。
  2. `RunError` 增加重试提示：是否可重试、建议等待时长（provider 给出 `retry-after`/重置时间时）。
  3. 墙钟超时与看门狗（R5）改为返回 `RunError`，携带部分 Result，同时 `errors.Is(err, context.DeadlineExceeded)` 仍成立。
  4. 新增 `ErrorTail`：失败时附带 stderr 尾部 2 KiB（已脱敏），宿主可直接展示。
  5. adaptertest 为每种原因提供 fixture，Driver 声明自己能识别哪些原因（不能识别就报 `agent_error`，不伪造）。
- **API 变更**：新增 + 行为变化（超时从基础设施错误变为 `*RunError`，`errors.Is` 兼容）。
  ```go
  const (
      ReasonAuth               FailureReason = "auth"                // 未登录、token 失效、无权限
      ReasonQuota              FailureReason = "quota"               // 额度/订阅用尽，重试无用
      ReasonRateLimit          FailureReason = "rate_limit"          // 限流/容量不足，可稍后重试
      ReasonProviderServer     FailureReason = "provider_server"     // provider 5xx
      ReasonNetwork            FailureReason = "network"
      ReasonContextOverflow    FailureReason = "context_overflow"
      ReasonModelUnavailable   FailureReason = "model_unavailable"
      ReasonMissingExecutable  FailureReason = "missing_executable"
      ReasonVersionUnsupported FailureReason = "version_unsupported"
      ReasonEmptyOutput        FailureReason = "empty_output"
      ReasonProtocolError      FailureReason = "protocol_error"
      ReasonProcessFailure     FailureReason = "process_failure"
      ReasonIterationLimit     FailureReason = "iteration_limit"     // MaxTurns 等
      // 以及 R1 的 cleanup_unconfirmed、R5 的 timeout/idle/no_progress
  )

  type RunError struct {
      Reason    FailureReason
      Message   string
      Details   map[string]any
      Result    *Result
      // 新增
      Retryable  bool          // SDK 依据原因与 provider 信号给出的判断
      RetryAfter time.Duration // provider 明确给出时非零
      ErrorTail  string        // 脱敏后的 stderr 尾部
  }

  // driver SPI：driver.FailureCode 同步扩展，RunFailure 增加 RetryAfter
  ```
- **用户怎么用**：
  ```go
  _, err := thread.Run(ctx, prompt)
  var runErr *adaptor.RunError
  if errors.As(err, &runErr) {
      switch runErr.Reason {
      case adaptor.ReasonQuota:
          notifyOwner("模型额度已用完")
      case adaptor.ReasonAuth:
          markRuntimeNeedsLogin()
      case adaptor.ReasonContextOverflow:
          reassignWithFreshThread()
      default:
          if runErr.Retryable {
              queue.RetryAfter(task, max(runErr.RetryAfter, 30*time.Second))
          }
      }
      issue.Comment(runErr.Result.Summary + "\n\n" + runErr.ErrorTail)
  }
  ```
- **关键场景**：H1、H3、H4。
- **验收**：每个 Driver × 每个可识别原因至少一个 fixture；`docs/public-errors.md` 列出原因、`Retryable` 默认值与示例。

### R8 终局已观察到时，清理卡住不得改判结果

- **现状**：`driver.Run` 返回后才形成 Result；终局 payload 已读到但进程迟迟不退出时，运行要么被墙钟超时改判为失败，要么一直挂住。
- **问题与后果**：H1 中 agent 已完成并给出最终回答，却因为某个后台子进程不退出被标为超时失败，宿主自动重试，造成重复提交、重复评论。
- **方案**：
  - Driver SPI 增加「终局已观察到」信号：Driver 读到正式终局帧时通过 sink 上报一个暂定 Response。
  - 引擎从该时刻起只给清理 `GracePeriod` 的预算；到期后以暂定 Response 形成 Result 返回，清理在后台继续，由 `Agent.Close` 统一收尾，并发出 `NoticeProcessCleanup{confirmed:false}`（R1）。
  - 活性看门狗与墙钟超时在终局观察之后不再改判结果。
- **API 变更**：
  - 根包：无新增（复用 R1 的 Notice）。
  - Driver SPI 新增（additive）：
    ```go
    // driver.EventSink 的可选扩展
    type TerminalObserver interface {
        // ObserveTerminal 在 Driver 读到 provider 正式终局帧后、开始任何可能阻塞的清理前调用一次。
        ObserveTerminal(provisional Response)
    }
    ```
- **关键场景**：H1、H5。
- **验收**：fake driver「终局后 stdout 不关闭」用例：Result 为成功，在 `GracePeriod` 内返回，Notice 标记未确认。

### R9 混沌与长稳测试

- **现状**：已有 Event 顺序、背压、Approval exactly-once 的 race 测试，缺少进程级故障注入。
- **方案**：在 `internal/testutil` 提供可编程 fake CLI（按脚本输出、沉默、产生孙进程、setsid、忽略 SIGTERM、输出超长行、在握手中途退出），adaptertest 新增故障场景组，每个内置 Driver 必须通过；夜间 CI 进行 2 小时 soak（循环 Thread 续接 + 随机取消），监测 goroutine、文件句柄、内存。
- **API 变更**：`adaptertest` 新增故障场景（Driver 作者可选择性运行，内置 Driver 强制）。
- **关键场景**：全部。
- **验收**：soak 结束时 goroutine 数与句柄数回到基线。

### R10 同一 provider home 的并发写保护

- **现状**：Thread lease 保证同一 key 单 writer。但多个 Agent 共享同一 profile 目录（如同一 `CODEX_HOME`），并通过不同 key 或 Fork 指向同一个 provider 会话时，可能有两个进程同时写同一个 rollout/会话文件。multica 为此在 `execenv/codex_session_writer_lock.go` 加了文件锁。
- **方案**：Driver 在打开 provider 会话前，对 `<profile>/<resumeID>` 加跨进程文件锁（Unix `flock`，Windows `LockFileEx`）；拿不到锁时返回 `ErrThreadBusy`。Fork 的父会话只读访问不加写锁。
- **API 变更**：无（复用 `ErrThreadBusy`）。
- **关键场景**：H1、H7。
- **验收**：两个进程同时续接同一 ResumeID，必有一个得到 `ErrThreadBusy`。

---

## 6. P2：宿主能力补全

### P2-1 中途追加指令（Steer）

- **现状**：`Stream` 只有 `Events/Result/RunID/Cancel`。运行中无法给 agent 补充信息，只能取消重来。
- **问题与后果**：H1 中用户在 issue 里追加评论「别动 public API」，宿主只能等这轮结束或取消（丢掉已完成的工作）；H2 聊天中用户补一句话要重跑整轮。multica 已在 Codex（`turn/steer`）、Claude（SDK hooks 在 `PreToolUse`/`PostToolUse`/`Stop` 注入 additionalContext）、Cursor（hook relay）上实现了这一能力。
- **方案**：
  - 投递机制由各 Driver 实现：
    - Codex app-server：`turn/steer`，立即进入当前 turn。
    - Claude/CodeBuddy：在 stream-json 输入通道与 hook 控制协议上，于下一个工具边界或 Stop 时注入；Stop 时有待投递内容则继续同一循环。
    - Pi：RPC 的 steer/follow-up 命令。
    - OpenCode：server 模式核实可行性后实现。
    - Cursor：暂不支持，如实报告。
  - 交付语义：`Steer` 返回表示 provider 已接受；实际注入时发出 Notice；运行在注入前结束则返回明确错误，宿主可把内容转入下一轮 prompt，不丢失。
  - 追加内容作为 `Role=user` 的 transcript 条目记录，不影响 Thread fingerprint。
  - 为避免修改已冻结的 `Stream` 接口，使用包级函数。
- **API 变更**：新增（additive）。
  ```go
  // 根包
  func Steer(ctx context.Context, s Stream, text string) (SteerReceipt, error)
  func SteerReady(s Stream) bool // provider 已确认有活动 turn，可以投递

  type SteerReceipt struct {
      ID       string
      Delivery SteerDelivery // immediate | next_boundary
  }

  const NoticeSteerDelivered = "steer.delivered" // Data: {"id": string}

  var (
      ErrSteerUnsupported error // Driver 或当前 transport 不支持
      ErrSteerNotReady    error // 尚未进入可投递状态，可稍后重试
      ErrRunFinished      error // 运行已结束，内容未投递
  )

  // driver SPI
  type SteerCapability struct {
      Supported bool
      Delivery  SteerDelivery
  }
  // Descriptor 增加 Steer SteerCapability
  // Request 增加 SteerInputs <-chan SteerInput（不支持时为 nil）
  type SteerInput struct {
      ID   string
      Text string
      Ack  func(err error) // provider 接受或拒绝时调用一次
  }
  ```
- **用户怎么用**：
  ```go
  stream := thread.Stream(ctx, "修复 #123 的失败测试")
  go consume(stream)

  // issue 上来了新评论
  func onComment(c Comment) {
      if !adaptor.SteerReady(stream) {
          pending.Add(c) // 留到下一轮
          return
      }
      _, err := adaptor.Steer(ctx, stream, c.Body)
      switch {
      case err == nil:
      case errors.Is(err, adaptor.ErrSteerNotReady):
          retryLater(c)
      case errors.Is(err, adaptor.ErrRunFinished), errors.Is(err, adaptor.ErrSteerUnsupported):
          pending.Add(c) // 作为下一轮 prompt 的一部分
      }
  }
  ```
- **关键场景**：H1、H2、H7（Leader 纠偏子 Agent）。
- **验收**：每个支持的 Driver 有「运行中投递并在 transcript 出现」「运行结束竞态返回 ErrRunFinished」「未就绪返回 ErrSteerNotReady」测试；Descriptor 能力与 adaptertest 一致。

### P2-2 按次设置 effort 与 service tier

- **现状**：effort 只在各 Driver Config 中：`codex.ReasoningEffort`、`claude.Effort`、`codebuddy.Effort`、`pi.Thinking`、`opencode.Variant`；Codex 的速度档是 `FastMode bool`。`WithModel` 可以按次覆盖，effort 不行。
- **问题与后果**：H3 中用户为每个 agent、甚至每个任务选择 effort；宿主要为每个组合重新 `New` 一个 Agent，常驻进程、profile 物化都要重做。
- **方案**：
  - 新增双作用域 `WithEffort`，值使用可移植常量；Driver 负责映射到原生值，并按模型目录（P2-3）校验，不支持时在启动前报错。
  - `WithEffortNative` 透传 provider 原生值（例如 OpenCode 的 variant 名称）。
  - `WithServiceTier` 目前仅 Codex 支持，其他 Driver 启动前报不支持。
  - 优先级：调用处 > 构造处选项 > Driver Config 字段。
  - effort 与 service tier 不进入 Thread 兼容 fingerprint（同一会话换 effort 是 provider 支持的正常操作）。
- **API 变更**：新增（additive）。根包 `With*` 从 26 个增至 28–29 个，需要更新 `AGENTS.md` 第 15 节的心智负担目标。
  ```go
  type Effort string
  const (
      EffortMinimal Effort = "minimal"
      EffortLow     Effort = "low"
      EffortMedium  Effort = "medium"
      EffortHigh    Effort = "high"
      EffortXHigh   Effort = "xhigh"
      EffortMax     Effort = "max"
  )
  func WithEffort(e Effort) SharedOption
  func WithEffortNative(value string) SharedOption
  func WithServiceTier(tier string) SharedOption

  var ErrEffortUnsupported error
  ```
- **用户怎么用**：
  ```go
  agent := adaptor.New(codex.Driver(codex.Config{}), adaptor.WithEffort(adaptor.EffortMedium))

  // 这个任务难，单独提高
  res, err := thread.Run(ctx, prompt,
      adaptor.WithModel(task.Model),
      adaptor.WithEffort(adaptor.Effort(task.Effort)),
      adaptor.WithServiceTier("priority"),
  )
  if errors.Is(err, adaptor.ErrEffortUnsupported) {
      ui.Error("该模型不支持此 effort 档位")
  }
  ```
- **关键场景**：H3、H4。
- **验收**：每个 Driver 的映射表测试；同一 Thread 切换 effort 后仍续接同一会话。

### P2-3 模型目录

- **现状**：`ModelInfo{ID, Label}`。
- **问题与后果**：H3 的模型选择器无法知道默认模型、每个模型支持哪些 effort 档位（例如 Claude 的 `xhigh` 只有 Opus 支持）、Codex 哪些模型有 priority tier，只能在宿主里硬编码，provider 升级就过期。
- **方案**：扩展 `ModelInfo`，由 Driver 从 provider 官方来源发现（例如 `codex debug models`、ACP 的 `session/new` 返回的 models），发现失败时使用静态表并标注来源。
- **API 变更**：新增（additive，字段追加）。
  ```go
  type ModelInfo struct {
      ID           string
      Label        string
      // 新增
      Provider     string
      Default      bool
      Efforts      []EffortOption // nil 表示该模型没有 effort 控制
      ServiceTiers []string
      Source       string // "live" | "static" | "config"
  }
  type EffortOption struct {
      Value   Effort
      Native  string
      Label   string
      Default bool
  }
  ```
- **用户怎么用**：
  ```go
  models, err := agent.Inspect().Models(ctx)
  for _, m := range models {
      ui.AddModel(m.ID, m.Label, m.Default)
      for _, e := range m.Efforts {
          ui.AddEffort(m.ID, e.Value, e.Default)
      }
  }
  ```
- **关键场景**：H3。
- **验收**：每个 Driver 至少一个 live 发现 fixture；`Source` 在静态回退时必须为 `static`。

### P2-4 用量扩展：缓存写入、reasoning、按模型、成本精度

- **现状**：`Usage{InputTokens, OutputTokens, CachedInputTokens, EstimatedCostMilli}`，单份汇总；成本单位千分之一美元。
- **问题与后果**：
  - 缓存写入单独计价，无法表达（P0-3 已定义口径，这里补字段）。
  - 一次运行可能用到多个模型（Claude 的 haiku 子任务、Team 模式的多个子 Agent），汇总后无法按模型计价。
  - 很多单轮成本不足 0.1 美分，`EstimatedCostMilli` 会被舍入为 0。
  - 无法区分「provider 上报的成本」和「SDK 估算的成本」。
- **方案**：保持现有字段与口径，追加子集字段、按模型拆分与高精度成本。
- **API 变更**：新增（additive）。
  ```go
  type Usage struct {
      InputTokens        int
      OutputTokens       int
      CachedInputTokens  int   // InputTokens 的子集：缓存读取
      EstimatedCostMilli int64 // 保留兼容；新代码使用 CostMicros
      // 新增
      CacheWriteInputTokens int    // InputTokens 的子集：缓存写入
      ReasoningOutputTokens int    // OutputTokens 的子集
      CostMicros            int64  // 百万分之一美元；0 表示未知
      CostSource            string // "provider" | "estimated" | ""
  }

  // Result 追加
  type Result struct {
      // ...现有字段
      ModelUsage []ModelUsage // 按模型拆分；provider 不提供时为 nil，此时只有 Usage
  }
  type ModelUsage struct {
      Model string
      Usage Usage
  }
  ```
  `Usage == nil` 继续表示「未观察到」，与真实零值区分。
- **用户怎么用**：
  ```go
  for _, mu := range res.ModelUsage {
      billing.Charge(team, mu.Model,
          mu.Usage.InputTokens-mu.Usage.CachedInputTokens-mu.Usage.CacheWriteInputTokens,
          mu.Usage.CachedInputTokens, mu.Usage.CacheWriteInputTokens, mu.Usage.OutputTokens)
  }
  if res.Usage != nil && res.Usage.CostSource == "provider" {
      ledger.Record(res.RunID, res.Usage.CostMicros)
  }
  ```
- **关键场景**：H4、H7。
- **验收**：`ModelUsage` 各项之和等于 `Usage`；Claude `modelUsage`、Codex、OpenCode 的 fixture。

### P2-5 版本探测与按版本门控

- **现状**：`EnvironmentReport{DriverType, Status, Healthy, Summary, Checks}`，没有可执行文件路径与版本字段；Driver 不检查最低版本。
- **问题与后果**：H3 无法显示「Claude Code 2.1.3，位于 …」；用户装了过旧的 CLI，运行时才以莫名其妙的协议错误失败。multica 有 `CheckMinVersion` 与按版本开启 Supplement 的门控。
- **方案**：
  - `EnvironmentReport` 追加 `ExecutablePath`、`Version`、`MinVersion`。
  - Driver 声明最低支持版本；版本过低时 Environment 报 `fail`，运行时启动前返回 `ReasonVersionUnsupported`。
  - 能力（如 P2-1 Steer）可按版本声明，Descriptor 为静态上限，Inspect 返回本机实际能力。
  - 版本探测结果按可执行文件路径与 mtime 缓存，避免每次运行都执行 `--version`。
- **API 变更**：新增（additive）。
  ```go
  type EnvironmentReport struct {
      DriverType     string
      Status         EnvironmentStatus
      Healthy        bool
      Summary        string
      Checks         []EnvironmentCheck
      // 新增
      ExecutablePath string
      Version        string
      MinVersion     string
      Capabilities   *Descriptor // 按本机版本裁剪后的实际能力；nil 表示与 Descriptor 相同
  }
  ```
- **用户怎么用**：
  ```go
  rep, _ := agent.Inspect().Environment(ctx)
  ui.Runtime(rep.DriverType, rep.ExecutablePath, rep.Version, rep.Healthy, rep.Summary)
  if rep.Capabilities != nil && !rep.Capabilities.Steer.Supported {
      ui.Hint("升级 CLI 后可在运行中追加指令")
  }
  ```
- **关键场景**：H3、H6、H8。
- **验收**：每个 Driver 的版本解析 fixture；版本过低时 `Run` 在启动 provider 进程前失败。

### P2-6 wrapper 命令与参数冲突校验

- **现状**：`CommonConfig.Command` 是单个可执行文件，`ExtraArgs` 追加在 SDK 参数之后，不做冲突检查。
- **问题与后果**：
  - H8：公司把 CLI 包装成 `ccms start q36 <args>` 这样的 wrapper，wrapper 的子命令必须紧跟可执行文件、位于所有 SDK 参数之前；现在无法表达。
  - 用户在 `ExtraArgs` 里传 `--output-format text`，会破坏 SDK 依赖的 stream-json 协议，导致解析失败而不是明确报错。
- **方案**：
  - 新增 `CommandArgs`：紧跟 `Command`、位于所有 SDK 参数之前，同样用于版本探测与 Inspect（与实际启动方式一致）。
  - 每个 Driver 维护保留参数表（协议、输出格式、会话、权限相关），`ExtraArgs`/`CommandArgs` 命中时 `ValidateConfig` 返回 `InvalidDriverConfigError`，并指出替代的 SDK 选项。
- **API 变更**：新增 + 行为变化（以前被静默接受的冲突参数现在报错）。
  ```go
  type CommonConfig struct {
      Command     string
      CommandArgs []string // 新增：wrapper 前缀参数
      // ...
      ExtraArgs   []string
  }
  ```
- **用户怎么用**：
  ```go
  claude.Driver(claude.Config{CommonConfig: claude.CommonConfig{
      Command:     "ccms",
      CommandArgs: []string{"start", "q36"},
  }})
  // ExtraArgs: []string{"--output-format", "text"} →
  // InvalidDriverConfigError: "--output-format" is managed by the SDK; use WithSchema for structured output
  ```
- **关键场景**：H3、H8。
- **验收**：每个 Driver 的保留参数表测试；wrapper 用例同时覆盖 Run 与 Environment。

### P2-7 provider 会话身份尽早发出

- **现状**：provider session id 只出现在最终 checkpoint 与 transcript 条目中。
- **问题与后果**：H2/H3 的 UI 想在运行一开始就显示「会话 abc…，可在终端用 `claude --resume abc` 打开」；P2-9 的断点续接也依赖尽早拿到它。
- **方案**：Driver 从正式协议首次得到会话 id 时（Claude `system/init`、Codex `thread/started`、ACP `session/new` 返回）发出一个 Notice；数据只包含 `SessionState.DisplayID`，不把 resume id 升级为消费者身份体系（遵守 `AGENTS.md` 第 6 节）。
- **API 变更**：新增（additive）。
  ```go
  const NoticeSessionIdentified = "session.identified" // Data: {"display_id": string, "resumed": bool}
  ```
- **用户怎么用**：
  ```go
  if n, ok := ev.(adaptor.Notice); ok && n.Kind == adaptor.NoticeSessionIdentified {
      ui.ShowSession(n.Data["display_id"].(string))
  }
  ```
- **关键场景**：H2、H3、H5。
- **验收**：每个支持 resume 的 Driver 在第一个语义事件前或同时发出该 Notice。

### P2-8 会话连续性信号

- **现状**：`invocation.go` 中 continue-or-start 遇到 `ResumeRejectedError` 时调用 `PrepareFresh` 直接重试，宿主看不到；配置漂移（例如换了模型）导致 fingerprint 不兼容时，同样静默开新会话。两者都不区分永久与暂时拒绝。
- **问题与后果**：H1 中 agent 在新会话里重新开始，却以为自己记得之前的讨论，回复中引用不存在的上下文；用户以为 agent「失忆」是 bug。multica 的做法是：resume 失败落到新会话时，在首轮 prompt 前注入宿主提供的连续性提示（例如「之前的会话已无法恢复，请重新阅读 issue 评论」），并区分永久/暂时拒绝，暂时拒绝不废弃旧会话。
- **方案**：
  1. 每次 Thread 运行都发出一个连续性 Notice，并写入 Result，说明续上了还是新开了、原因是什么。
  2. 新增 ThreadOption，让宿主提供「落到新会话时」前置到 prompt 的文案；SDK 只负责在确实新开时注入，文案归宿主（遵守第 2 节边界）。
  3. `ResumeRejectedError` 增加 `Transient`：暂时拒绝时本轮用新会话，但不归档旧记录，下次继续尝试续接旧会话。
  4. Descriptor 如实声明 Driver 能否检测 resume 拒绝；不能检测的 Driver，连续性原因报 `unknown`，不报 `resumed`。
- **API 变更**：新增（additive）。
  ```go
  const NoticeThreadContinuity = "thread.continuity"

  type Continuity struct {
      Outcome   ContinuityOutcome // resumed | started | restarted
      Cause     string            // restarted 时："resume_rejected" | "resume_rejected_transient" | "incompatible" | "unknown"
      Dimension string            // Cause=incompatible 时：发生漂移的维度，如 "model"、"mcp"
  }

  // Result 追加
  type Result struct {
      // ...
      Continuity *Continuity // 非 Thread 运行为 nil
  }

  func OnRestart(notice string) ThreadOption

  // driver SPI
  type SessionCapability struct {
      SupportsResume         bool
      DetectsResumeRejection bool // 新增
  }
  ```
- **用户怎么用**：
  ```go
  th := agent.Thread(issueKey, adaptor.OnRestart(
      "注意：之前的对话已无法恢复。请先阅读 issue 的全部评论再继续。"))
  res, err := th.Run(ctx, prompt)
  if res != nil && res.Continuity != nil && res.Continuity.Outcome == adaptor.ContinuityRestarted {
      issue.SystemNote("agent 已在新会话中继续（原因：" + res.Continuity.Cause + "）")
  }
  ```
- **关键场景**：H1、H2、H5。
- **验收**：四种 Outcome/Cause 各有测试；Transient 拒绝后旧记录仍为 active；`OnRestart` 文案只在 restarted 时出现在 Driver 收到的 prompt 中。

### P2-9 崩溃安全的会话固定（Pin）

- **现状**：Thread 状态只在运行成功结束时通过 `Store.Finalize` 写入。
- **问题与后果**：H5 中 daemon 在一个 2 小时运行的第 90 分钟被杀或升级，provider 会话文件里有完整进展，但 Thread store 中仍是上一轮的 checkpoint；重启后只能从上一轮续接，丢失 90 分钟的工作。multica 在拿到 SessionID 时立即落库来避免这个问题。
- **方案**：
  - `threadstore` 增加可选扩展接口，Store 实现了它才启用。
  - 引擎在 P2-7 拿到可续接的会话状态时，以 lease 持有者身份写入一条「暂定」状态；运行正常结束时 `Finalize` 覆盖它。
  - 下次 Resolve 读到暂定状态（说明上一轮未正常结束）时，优先尝试续接暂定会话；被拒绝时回退到上一个已确认的 checkpoint，再不行才新开，并通过 P2-8 的连续性信号说明。
  - 暂定状态必须经过 Driver SessionCodec 校验，且不违反「失败不污染健康状态」：已确认的 checkpoint 保留在 record 中，直到新 checkpoint 被确认。
- **API 变更**：新增（additive）。
  ```go
  // threadstore
  type Pinner interface {
      // Pin 在不改变已确认 State 的前提下记录暂定 State；必须校验 lease。
      Pin(ctx context.Context, lease Lease, recordID string, provisional *driver.SessionState) error
  }

  type Record struct {
      // ...现有字段
      Provisional *driver.SessionState // 新增；nil 表示上一轮正常结束
  }
  ```
- **用户怎么用**：宿主的 Store 实现 `Pin`（写一列 JSON 即可）；`memory.Store` 内置实现。无需修改调用代码。
- **关键场景**：H1、H5。
- **验收**：测试「运行中途杀掉宿主进程 → 新进程续接到暂定会话」；暂定会话被拒绝时回退到已确认 checkpoint。

### P2-10 暂时性失败后保留可续接 checkpoint

- **现状**：`AGENTS.md` 第 9 节：非零退出、取消、业务失败默认不产生有效 checkpoint；「只有 Driver 能证明 provider 明确交付了健康可续状态」才可例外，但目前没有任何 Driver 实现这个例外。
- **问题与后果**：H1 中一个运行了 1 小时的任务在第 59 分钟遇到限流或网络中断，Thread 回到上一轮状态，重试时 agent 从头再做一遍，浪费时间与费用。multica 在网络、限流、配额、5xx 失败时保留会话指针，让平台重试续接被截断的对话。
- **方案**：
  - 仅对 R7 中 `Retryable=true` 且属于 provider 侧暂时性故障的原因（`rate_limit`、`network`、`provider_server`）开放。
  - Driver 必须能证明会话持久化完整（例如 Codex rollout 已 flush 且最后一条记录完整、Claude 会话 jsonl 最后一行可解析），才把 checkpoint 标为 `Valid` 并设置 `AfterFailure`。
  - Descriptor 声明哪些 Driver 支持；不支持的保持现有行为。
  - 宿主可以显式关闭（例如对结果正确性要求极高的场景）。
- **API 变更**：新增 + 合同修订（`AGENTS.md` 第 9 节）。
  ```go
  // driver SPI
  type Checkpoint struct {
      State        *SessionState
      Valid        bool
      AfterFailure bool // 新增：失败路径上经 Driver 证明健康的 checkpoint
  }
  type SessionCapability struct {
      // ...
      CheckpointOnTransientFailure bool // 新增
  }

  // 根包
  type RunError struct {
      // ...R7 字段
      ThreadPreserved bool // 本次失败的进展已保存，重试会从失败处续接
  }
  func NoFailureCheckpoint() ThreadOption // 显式关闭
  ```
- **用户怎么用**：
  ```go
  _, err := thread.Run(ctx, prompt)
  var runErr *adaptor.RunError
  if errors.As(err, &runErr) && runErr.Retryable {
      next := prompt
      if runErr.ThreadPreserved {
          next = "上一轮因网络中断停止，请从中断处继续。"
      }
      queue.RetryAfter(task.WithPrompt(next), runErr.RetryAfter)
  }
  ```
- **关键场景**：H1、H4。
- **验收**：每个支持的 Driver 有「中途限流 → 保存 → 续接后 agent 看到之前的工具结果」live 测试；不完整的会话文件不得被标为 Valid。

### P2-11 Driver 声明可容忍的配置漂移

- **现状**：兼容 fingerprint 覆盖模型、instructions、MCP、skills、tools 等全部维度；任何漂移都会导致新开会话（continue-or-start）或 `ErrThreadIncompatible`（ResumeOnly）。
- **问题与后果**：H1/H3 中用户把 issue 的 agent 从 sonnet 换成 opus 后继续讨论，provider 本身完全支持同一会话换模型，SDK 却丢掉上下文。反过来，MCP 或 tools 变了确实可能导致历史中的工具调用无法解释，应该保持严格。
- **方案**：
  - Descriptor 声明「切换后仍可续接」的维度（初始只开放 `model`；effort 已在 P2-2 排除）。
  - 引擎计算兼容 fingerprint 时排除这些维度，但把它们写入 record，变化时通过 P2-8 Notice 报告（Outcome=resumed，附带变化维度），方便宿主审计。
  - 宿主可以对单个 Thread 要求严格比较。
- **API 变更**：新增 + 行为变化（换模型从「新开」变为「续接」，仅对声明了容忍的 Driver）。
  ```go
  // driver SPI
  type SessionCapability struct {
      // ...
      ResumeTolerant []CompatDimension // 例如 {CompatModel}
  }

  // 根包
  func StrictCompatibility() ThreadOption
  ```
- **用户怎么用**：
  ```go
  th := agent.Thread(issueKey)                                   // 换模型继续同一会话
  audit := agent.Thread(auditKey, adaptor.StrictCompatibility()) // 审计场景：任何漂移都新开
  ```
- **关键场景**：H1、H3。
- **验收**：Codex/Claude 的 live 测试证明换模型后上下文仍在；StrictCompatibility 下行为与当前一致。

### P2-12 Thread store：一致性套件、SQL 参考实现、存量会话导入

- **现状**：`threadstore.Store` 合同完整（Resolve、Finalize 原子语义、lease 的 owner/token），但只有 `memory.Store` 实现，没有可复用的一致性测试；宿主已有的 provider 会话 id 无法转为 Thread。
- **问题与后果**：multica 这类宿主必须把 Thread 存进自己的 Postgres，Finalize 的原子性、lease 防覆盖等细节容易写错，且没有测试可对照；从现有实现迁移时，存量 issue 的会话全部丢失。
- **方案**：
  1. 新增 `threadstore/threadstoretest`：`Run(t, factory)` 覆盖 lease 竞争、过期 lease 不能 Finalize、ArchiveOld/RebindActive/RequireKeyAbsent、Pin（P2-9）、并发 Finalize。`memory.Store` 必须通过。
  2. 新增 `threadstore/sqlstore`（独立 module 或子包，只依赖 `database/sql`），提供 Postgres/SQLite 的 schema 与实现，作为参考。
  3. 存量导入：`ThreadOption` 在 key 不存在时用宿主提供的 provider 会话状态作为初始 checkpoint；由 Driver 包提供构造函数并经 SessionCodec 校验，避免宿主手写 codec 内部格式。key 已存在时该选项无效并报错，遵守「不提供同 key 重绑入口」。
- **API 变更**：新增（additive）。
  ```go
  // threadstore/threadstoretest
  func Run(t *testing.T, newStore func(t *testing.T) threadstore.Store)

  // 根包
  func SeedCheckpoint(cp *Checkpoint) ThreadOption

  // 各 Driver 包
  func codex.ImportSession(threadID string) (*driver.Checkpoint, error)
  func claude.ImportSession(sessionID string) (*driver.Checkpoint, error)
  ```
- **用户怎么用**：
  ```go
  // 宿主自己的 Store
  func TestPostgresStore(t *testing.T) {
      threadstoretest.Run(t, func(t *testing.T) threadstore.Store { return newPGStore(t) })
  }

  // 迁移：老任务表里有 codex thread id
  cp, err := codex.ImportSession(task.LegacySessionID)
  th := agent.Thread(issueKey, adaptor.SeedCheckpoint(cp))
  res, err := th.Run(ctx, prompt) // 首次运行续接旧会话；之后由 Store 接管
  ```
- **关键场景**：H1、H5。
- **验收**：`memory.Store` 与 `sqlstore` 都通过套件；导入后首轮续接 live 测试。

### P2-13 核实项：instructions 交付位置

- **现状**：`internal/profileinstructions` 把 `WithInstructions` 物化为 profile 作用域内的原生文件，并跟踪所有权。multica 则在每个任务的 workdir 中写 `CLAUDE.md`/`AGENTS.md`。
- **待核实**：
  1. 在 `profile.Native()`（直接用本机配置）下，SDK 是否会写入用户真实的 `~/.claude/CLAUDE.md`、`~/.codex/AGENTS.md`；若会，是否有明确的所有权保护与冲突报错。
  2. 与仓库自带 `AGENTS.md` 的叠加顺序是否符合各 provider 的官方语义。
- **方案**：核实后补充 `docs/profile-resource-provider-matrix.md` 的说明与测试；发现覆盖用户文件的情况则按 P0 处理。
- **关键场景**：H1、H3。

---

## 7. P3：Provider 覆盖

### P3-1 通用 ACP Driver

- **现状**：没有 ACP 支持。multica 27 个 provider 中约 12 个走 ACP：hermes、kimi、kiro、qoder（含 qoderclicn）、traecli、grok、qwenpaw、dim、devin、mcode、zeroclaw、reasonix，此外 OpenCode 也有 ACP 模式。
- **问题与后果**：H8 用户只要用到这些 CLI 就无法使用 agent-adaptor；每个都写一个专用 Driver 成本太高。
- **方案**：新增 `acp` 包，一个 Driver 实现 ACP 规范，按协议能力协商映射到 SDK：

  | ACP | SDK |
  |---|---|
  | `initialize`（capabilities） | Descriptor 的运行时能力（Inspect） |
  | `session/new`（含 `mcpServers`） | 新会话；`WithMCP`/`WithTools` 原生交付（stdio/http/sse） |
  | `session/load` | Thread 续接；不支持时如实报不支持 resume |
  | `session/prompt` | 一次运行 |
  | `session/update`：`agent_message_chunk` / `agent_thought_chunk` / `tool_call` / `tool_call_update` / `plan` | `TextDelta` / `Thinking` / `ToolCall` / `ToolResult` / `Notice(step)` |
  | `session/request_permission` | `ApprovalRequest`（Permission），exactly-once 应答 |
  | `session/cancel` | `Cancel()` |
  | `session/set_model` / `set_mode` | `WithModel` / `Policy` 中可映射的部分 |
  | `_meta.usage` 等扩展 | 有则填 Usage，没有则 `Usage=nil`，不伪造 |

  预设只包含命令与已知差异（例如需要的子命令、是否支持 `session/load`），放在 `acp` 包中以构造函数提供；预设不在根包新增任何名词。
- **API 变更**：新增 `acp` 包。
  ```go
  package acp

  type Config struct {
      Command string   // 例如 "kimi"
      Args    []string // 例如 []string{"acp"}
      Env     []driver.EnvBinding
      Model   string
      Name    string   // Descriptor.Type，例如 "kimi"
  }
  func Driver(cfg Config) driver.Driver

  // 预设
  func Kimi() Config
  func Kiro() Config
  func Qoder() Config
  func Grok() Config
  ```
- **用户怎么用**：
  ```go
  kimi := adaptor.New(acp.Driver(acp.Kimi()), adaptor.WithWorkspace(repo))

  internal := adaptor.New(acp.Driver(acp.Config{
      Name: "acme-agent", Command: "acme", Args: []string{"agent", "--acp"},
  }))
  ```
- **关键场景**：H8、H3。
- **验收**：通用 Driver 通过 adaptertest；至少 3 个预设通过 live 测试；不支持的能力在 Descriptor 中为 false 且启动前报错。

### P3-2 第二批专用 Driver

- **Copilot CLI**：JSON 模式；需要处理 multica 记录的「`session.error` 早于 `session.start` 时拿不到 SessionID」问题，并如实声明 `DetectsResumeRejection=false`。
- **Qwen Code**：`-p --output-format stream-json`，与 Claude 协议同源，复用 Claude 的 stream-json 解析器（通过 `internal` 共享，不复用 Claude 的 Descriptor）。
- **API 变更**：新增 `copilot`、`qwen` 包，结构与现有 Driver 一致。
- **关键场景**：H8。

### P3-3 长尾交给第三方

- openclaw、codearts、deveco、antigravity、muse 等不内置。
- 加强第三方作者体验：`docs/` 增加「编写 Driver」教程；`adaptertest` 提供 fake transport 与 fixture 录制工具；`internal/clihelper` 中稳定的部分（进程启动、有上限行读取、Job Object）以 `driver/driverkit` 形式公开，避免第三方重复踩 R1/R3/R6 的坑。
- **API 变更**：新增 `driver/driverkit`（SPI 层，不进入根包）。

---

## 8. P4：用真实宿主验收

### P4-1 issue-daemon 示例

在 `examples/showcases/issue-daemon` 用 fake driver 与真实 Driver 两种模式模拟 multica 的完整流程，作为 P1/P2 的端到端验收：

1. 认领任务 → 准备 worktree 与隔离 profile（`WithWorkspaceSpec`、`profile.CloneNative`）。
2. `agent.Thread("ws/issue/agent", OnRestart(...))` 加 `WithLiveness`。
3. `Stream` 消费事件，把工具调用与文本写回「issue」。
4. 运行中到来新评论 → `Steer`。
5. 模拟限流 → `RunError.Retryable` + `ThreadPreserved` → 重试续接。
6. 杀掉宿主进程 → 重启 → Pin 续接，无孤儿进程。
7. 用户换模型 → 连续性 Notice 显示 resumed。
8. 统计 `ModelUsage` 计费。

### P4-2 multica 影子接入

在 multica 的 fork 中，用 feature flag 增加一个基于 agent-adaptor 的 `Backend` 实现，先覆盖两边都有的 6 个 provider，对同一批任务做影子对比：最终状态、输出、用量、续接成功率、失败原因分布、取消延迟、残留进程数。

multica 字段与 agent-adaptor 的对应关系（用于实现影子 Backend，也用于检查覆盖度）：

| multica | agent-adaptor | 状态 |
|---|---|---|
| `ExecOptions.Cwd` | `WithWorkspace` / `WithWorkspaceSpec` | 已有 |
| `ExecOptions.Model` | `WithModel` | 已有 |
| `ExecOptions.ThinkingLevel` | `WithEffort` / `WithEffortNative` | P2-2 |
| `ExecOptions.ServiceTier` | `WithServiceTier` | P2-2 |
| `ExecOptions.SystemPrompt` | `WithInstructions` | 已有（P2-13 核实） |
| `ExecOptions.MaxTurns` | `claude.Config.MaxTurnsPerRun` 等 | 部分已有 |
| `ExecOptions.Timeout` | `WithTimeout` | 已有（R7 改为携带部分 Result） |
| `SemanticInactivityTimeout` / `IdleWatchdogTimeout` | `Liveness.Idle` | R5 |
| `FirstTurnNoProgressTimeout` | `Liveness.FirstProgress` | R5 |
| `HandshakeTimeout` / `ThreadHandshakeTimeout` / `TurnInterruptTimeout` | `codex.Config.*Timeout` | R4 |
| `ResumeSessionID` | Thread key + store；存量用 `SeedCheckpoint` | 已有 + P2-12 |
| `ResumeExpected` / `ResumeContinuityNotice` | `OnRestart` + `Result.Continuity` | P2-8 |
| `ExtraArgs` / `CustomArgs` | `CommonConfig.ExtraArgs`（加冲突校验） | 已有 + P2-6 |
| `Config.LaunchPrefix` | `CommonConfig.CommandArgs` | P2-6 |
| `McpConfig` | `WithMCP` | 已有 |
| `Session.Messages` | `Stream.Events()` | 已有 |
| `Message.SessionID`（Status） | `NoticeSessionIdentified` | P2-7 |
| `Session.Supplement` / `SupplementReady` | `Steer` / `SteerReady` | P2-1 |
| `Session.ToolActivity` | 由 `ToolCall` 开始/结束事件推导；看门狗内置 | R5 |
| `Session.InterruptBackgroundTools` | 无 | 未计划（见第 10 节） |
| `Session.TerminalObserved` | Driver SPI `TerminalObserver` | R8 |
| `Session.CleanupRequired` | `NoticeProcessCleanup` / `ReasonCleanupUnconfirmed` | R1 |
| `Result.Status` | `error == nil` / `RunError.Reason` | 已有 + R7 |
| `Result.Output` | `Result.Text` | 已有 |
| `Result.Usage`（按模型） | `Result.Usage` + `Result.ModelUsage` | P0-2、P0-3、P2-4 |
| `Result.ResumeRejected` / `ResumeRejectedTransient` | `Continuity.Cause` | P2-8 |
| `ResumeRejectionUndetectable` | `SessionCapability.DetectsResumeRejection` | P2-8 |
| `taskfailure.Reason*` | `FailureReason` + `Retryable` | R7 |
| `ListModels` / `Catalog` | `Inspect().Models` | P2-3 |
| `DetectVersion` / `CheckMinVersion` | `EnvironmentReport.Version` / `MinVersion` | P2-5 |
| `ErrArgvLimitExceeded` | `ErrCommandLineTooLong` | R6 |
| 27 个 provider | 6 个 + ACP 通用 + 第二批 | P3 |

---

## 9. API 变更总表

| 条目 | 变更 | 类型 |
|---|---|---|
| P0-2、P0-3 | Usage 数值口径修正 | 行为变化 |
| P0-5 | `pi.Config`、`opencode.Config` 追加 `ExtraArgs`、`GracePeriod` | 新增 |
| R1 | `NoticeProcessCleanup`、`ReasonCleanupUnconfirmed`、`ErrPreviousRunCleanupUnconfirmed` | 新增 |
| R2 | `WithRawRetention`、`RawRetention`、`RawStreams` 追加截断/文件字段；默认截断 | 新增 + 行为变化 + 合同修订 |
| R4 | `codex.Config` 三个超时 | 新增 |
| R5 | `WithLiveness`、`Liveness`、`ReasonTimeout/Idle/NoProgress` | 新增 |
| R6 | `ErrCommandLineTooLong` | 新增 |
| R7 | `FailureReason` 扩展；`RunError.Retryable/RetryAfter/ErrorTail`；超时改为 `*RunError` | 新增 + 行为变化 |
| R8 | driver SPI `TerminalObserver` | 新增（SPI） |
| P2-1 | `Steer`、`SteerReady`、`SteerReceipt`、`NoticeSteerDelivered`、三个错误；SPI `SteerCapability`、`Request.SteerInputs` | 新增 |
| P2-2 | `Effort`、`WithEffort`、`WithEffortNative`、`WithServiceTier`、`ErrEffortUnsupported` | 新增 |
| P2-3 | `ModelInfo` 追加字段、`EffortOption` | 新增 |
| P2-4 | `Usage` 追加字段、`Result.ModelUsage` | 新增 |
| P2-5 | `EnvironmentReport` 追加字段 | 新增 |
| P2-6 | `CommonConfig.CommandArgs`；保留参数校验 | 新增 + 行为变化 |
| P2-7 | `NoticeSessionIdentified` | 新增 |
| P2-8 | `Continuity`、`Result.Continuity`、`OnRestart`、`NoticeThreadContinuity`；SPI `DetectsResumeRejection` | 新增 |
| P2-9 | `threadstore.Pinner`、`Record.Provisional` | 新增 |
| P2-10 | `Checkpoint.AfterFailure`、`CheckpointOnTransientFailure`、`RunError.ThreadPreserved`、`NoFailureCheckpoint` | 新增 + 合同修订 |
| P2-11 | SPI `ResumeTolerant`、`StrictCompatibility` | 新增 + 行为变化 |
| P2-12 | `threadstoretest`、`sqlstore`、`SeedCheckpoint`、各 Driver `ImportSession` | 新增 |
| P3 | `acp`、`copilot`、`qwen`、`driver/driverkit` 包 | 新增 |

全部变更均可在 v1 内以 additive 方式完成，不需要删除或改签名；「行为变化」项需在 CHANGELOG 中单列，并更新 `root_api.golden`、`driver_api.golden`、`AGENTS.md` 第 8、9、15 节与相关专题文档。根包 `With*` 预计从 26 个增至 30 个（`WithRawRetention`、`WithLiveness`、`WithEffort`、`WithEffortNative`、`WithServiceTier` 中按评审保留），需同步修订心智负担目标。

## 10. 待决策

1. **R2 Raw 合同**：默认截断（推荐）还是默认全量、截断为可选？
2. **P2-10 失败 checkpoint**：是否开放这个例外？推荐开放，但只针对 `rate_limit`/`network`/`provider_server`，并要求 Driver 证明会话完整。
3. **P2-1 Steer 形态**：包级函数（推荐，不动冻结接口），还是扩展 `Stream` 接口（breaking，需 v2）？
4. **P2-2 名称预算**：接受 `With*` 增至约 30 个，还是把 effort 与 service tier 合并为一个 `WithModelSettings`？
5. **P2-11 默认行为**：声明容忍的 Driver 默认换模型续接（推荐），还是默认严格、由宿主显式放宽？
6. **P2-12 存量导入**：`SeedCheckpoint` 是否与「resume id 不升级为消费者身份」的原则冲突？推荐的形式只在 key 不存在时生效，并由 Driver 校验。
7. **未计划项**：multica 的 `InterruptBackgroundTools`（在工具预算到期时只停后台工具、不取消 agent）暂不纳入；需要时再评估 Codex 后台终端的协议支持。

## 11. 里程碑与验收

| 里程碑 | 内容 | 退出条件 |
|---|---|---|
| M0 | P0 全部 | 工作树干净；三平台 CI 绿；用量口径测试通过 |
| M1 | R1、R2、R3、R4、R7、R8 | adaptertest 故障场景通过；1 GiB 输出内存受控；失败原因 fixture 全覆盖 |
| M2 | R5、R6、R9、R10，P2-1、P2-2、P2-7、P2-8 | issue-daemon 示例步骤 1–4、7 通过 |
| M3 | P3-1 ACP + 3 个预设 | ACP 通过 adaptertest 与 live 测试 |
| M4 | P2-3 至 P2-6、P2-9 至 P2-13 | issue-daemon 全部步骤通过；`threadstoretest` 套件发布 |
| M5 | P3-2、P3-3、P4-2 | multica 影子对比：续接成功率、失败分类准确率不低于 multica 原实现，残留进程为 0 |

每个里程碑合入前都要满足 `AGENTS.md` 第 15 节的全部发布门禁，并同步更新 godoc、专题文档、五种语言 README 与 CHANGELOG。
