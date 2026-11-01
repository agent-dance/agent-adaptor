# v2 提案：运行中消息、任务与待命、团队协作

状态：提案定稿（2026-10-31），待实施。本文件是设计记录，不是使用文档；落地后，合同以 v2 的 `AGENTS.md`、godoc、`api-reference.md` 与 `adaptertest` 为准。本提案取代 `host-readiness-plan.md` 的 P2-1，并替换 `hosttools/a2adelegation` 的团队部分。

标注约定：✅ 已在 multica 或本仓库源码中核实；⚠️ 需要在实施时用 live 测试核实，每一项都在 §6.4 给出了核实失败时的确定行为。

第 11 节是问题追踪表：讨论过程中提出的每一个问题与场景，都能在表中找到对应的结论和章节。

---

## 0. 一页摘要

宿主只有三种用法，单 Agent 与 Team 写法完全相同（`x` 是 `*adaptor.Agent` 或 `*team.Team`）：

```go
// 1. 一次性任务：没有记忆，有终点、有结果
s := x.Send(ctx, "修复 #123 的失败测试")
res, err := s.Result()                      // 同步
for ev := range s.Events() { /* 最后一个事件是 Done{Result, Err} */ } // 或异步

// 2. 带记忆的任务：接着这段上下文做，有终点、有结果，运行中可以补充
s := x.Thread("ws/issue-123").Send(ctx, taskPrompt)
s.Send(ctx, comment, adaptor.MessageID(id), adaptor.From(author)) // 补充进这次执行

// 3. 待命：持续接收消息，没有整体结果，直到 serverCtx 结束
sb, err := x.Thread("chat-42").Standby(serverCtx)
sb.Send(r.Context(), msg)                   // 只投递
for ev := range sb.Events() { /* 每轮回答是一个 TurnEnd */ }
sb.Cancel()                                 // 停止按钮：中止当前处理，继续待命
```

关键决定：

- **执行动词只有 `Send`**。v1 的 `Run`/`Stream` 合并进 `Send`，返回的 `Stream` 同时提供同步（`Result()`）与异步（`Events()`，以 `Done` 结尾）两种获取方式。
- **运行中补充也是 `Send`**，规则只有一条：发给谁，谁负责投递。`When` 选项表达投递时机：`Soon`（默认）、`Interrupt`、`AfterTurn`，对应"下一次模型调用时补充""立即打断""排队到本轮之后"三类场景。
- **待命是 Thread 的一种使用方式**，由 `th.Standby(serverCtx)` 显式开启。它的类型上没有 `Result()`，长生命周期的 ctx 只传一次，`sb.Send` 的 ctx 只约束投递。
- **只有 `Cancel`，没有 `Close`**。`Cancel` 永远只中止"正在进行的处理"。
- **leader 是一个完整的 Agent，遵循委派优先**：统筹（拆分、派发、纠偏、验收、合并、回答用户）归 leader；适合某个成员的工作必须派发，leader 不亲自做；只有没有成员能承接的工作才由 leader 亲自实现。宿主可以收紧为纯派发器（只读 sandbox）。
- **leader 由事件驱动、永不阻塞**：leader 自己手上没事可做时才结束本轮，而不是为了等 worker 结束本轮。worker 完成、提问或用户发话时，leader 正在干活就把消息并入当前这一轮，空闲就开新的一轮。用户只和 leader 说话，由 leader 代为转达给 worker。
- **团队工具面固定为 4 个**，与成员数量无关；成员名册作为数据下发。

不变的部分：六个核心名词；一条执行管线；每次执行一条 Event 流；一个 error 判定面；core 不内建队列、调度与团队业务。`Standby` 归入 Thread 名词之下，是 Thread 的待命句柄，就像 `Checkpoint` 归属于 Thread。

---

## 1. 目标、非目标与不变量

**目标**

1. 运行中补充消息成为 core 能力：每个内置 Driver 都可用，投递时机按 Driver 能力如实声明。
2. 一次性任务、带记忆的任务、待命三种用法，单 Agent 与 Team 一致、友好、贴切。
3. 团队模式支持并行委派、纠偏、上行回报、会话连续、长时任务、worker 审批，leader 不阻塞，工具面为 O(1)。
4. multica 的主场景可以直接映射，`pkg/agent` 中按 provider 手写的 supplement 代码可以被替换。

**非目标**

- core 不提供任务队列、调度器、自动路由、团队角色表或生产持久化实现。
- 不保证把内容插入"正在生成的那次模型输出"：没有任何 provider 支持这一点。`Interrupt` 的语义是打断后续跑。
- 不定义跨进程的消息传输。服务端到 daemon 的路径属于宿主（multica 用 WS 提醒加 HTTP claim）。
- 不在 SDK 内向多个界面客户端广播事件。SDK 保证一条完整、有序的事件流，广播是宿主的 Web 层职责（hub）。
- Pi 与 OpenCode 的实现已挂起，不在本提案范围内；恢复时按 §6.1 的 SPI 接入即可。

**不变量**

- 每次执行只有一个权威结果：`Result()` 与 `Done` 事件内容完全一致。
- 被接纳的消息要么被投递，要么以失败告终并把原文交还宿主，不会静默丢失。
- 同一个 Thread 同一时刻只有一个写者，且只处于一种模式（任务或待命）。

---

## 2. v1 现状问题

### 2.1 运行中补充缺失

`Stream` 只有 `Events/Result/RunID/Cancel`。宿主要补充内容，只能等这一轮结束，或者取消后重来。`host-readiness-plan.md` 的 P2-1 方案存在以下缺口，本提案全部替换：

- 只有包级函数，被包装过的 Stream 会静默失效。
- `SteerReady` 与 `ErrSteerNotReady` 职责重叠。
- 回执 ID 由 SDK 生成，宿主无法用业务 ID（评论 ID）做幂等。
- 没有收尾续跑和打断。
- 写着 Cursor 不支持，但 multica 已经实现了。

### 2.2 团队模式问题

| # | 问题 | 位置 / 依据 |
|---|---|---|
| T1 | 委派是同步工具调用：`delegate_to_agent` 阻塞到子 agent 结束，leader 无法纠偏、无法响应用户、无法并行；任务时长受 MCP 工具超时限制 | `hosttools/a2adelegation/mcpserver.go`、`local.go` |
| T2 | 工具面随成员数线性增长：`agent` 参数只是一个自由字符串，名册只能写在 prompt 里，宿主不得不为每个角色加 `ToolSpec` | `ToolSchema()`、`MCPServerOptions.Tools` |
| T3 | 结果按 `(runID, agentKey)` 只保留最新一份，重复委派会覆盖 | `Service.Result` |
| T4 | 没有会话连续，工具 schema 不暴露上下文句柄 | `ToolSchema()` |
| T5 | 没有上行通道：worker 只能通过最终结果说话 | `DelegationPolicy.AllowInputRequired` |
| T6 | 子任务生命周期绑定在 leader 的这次 Run 上 | `option.go` 的 `DetachRun` |
| T7 | 每个 leader run 一个 HTTP sidecar | `sidecar.go` |
| T8 | `SubagentUpdate.Data` 是字符串键值，缺少类型 | `events.go:316` |

---

## 3. 三种用法

| 用法 | 记忆 | 终点与结果 | 典型宿主 | 入口 |
|---|---|---|---|---|
| 一次性任务 | 无 | 有 | CLI、脚本、CI、团队成员被委派的任务 | `x.Send` |
| 带记忆的任务 | 有 | 有 | multica 的 issue 任务、工作流的多步任务 | `x.Thread(key).Send` |
| 待命 | 有 | 无（每轮有结果） | 聊天、IM 机器人、常驻团队 | `x.Thread(key).Standby(serverCtx)` |

**宿主怎么选：**

- 每次请求彼此无关 → 一次性任务。
- 需要接着之前的上下文做，并且每次都要一个明确的结果（用量、失败原因、最终输出） → 带记忆的任务。
- 消息随时会来，回答也随时会产生，不存在"这一次的结果" → 待命。

`x.Send(ctx, text)` 等价于 `x.Thread(<新的匿名 key>).Send(ctx, text)`。匿名 Thread 每次都是新的，永远只收到这一条消息（以及通过返回的 Stream 发出的补充），所以默认无状态、可以并发，v1 的语义保持不变。

---

## 4. 公共 API

### 4.1 入口

```go
// 根包
type Runner interface {
	Send(ctx context.Context, text string, opts ...CallOption) Stream
}

// ThreadRunner 由 *adaptor.Thread 与 *team.Thread 实现，供 bridges 等通用代码使用。
type ThreadRunner interface {
	Runner
	Key() string
	Standby(ctx context.Context) (Standby, error)
	Checkpoint(ctx context.Context) (*Checkpoint, error)
}

func New(d driver.Driver, opts ...Option) *Agent

func (a *Agent) Send(ctx context.Context, text string, opts ...CallOption) Stream
func (a *Agent) Thread(key string, opts ...ThreadOption) *Thread
func (a *Agent) Inspect() Inspector
func (a *Agent) Close(ctx context.Context) error

func (t *Thread) Key() string
func (t *Thread) Send(ctx context.Context, text string, opts ...CallOption) Stream
func (t *Thread) Standby(ctx context.Context) (Standby, error)
func (t *Thread) Fork(newKey string) *Thread
func (t *Thread) Checkpoint(ctx context.Context) (*Checkpoint, error)
```

团队的入口形状相同（见 §7.1）。

### 4.2 Stream：一次执行

```go
type Stream interface {
	Events() <-chan Event     // 以 Done 结尾，随后关闭
	Result() (*Result, error) // 同步获取；与 Done 内容一致；可并发、多次调用
	Send(ctx context.Context, text string, opts ...MessageOption) error // 补充进这次执行；只接受消息选项
	Cancel()                  // 中止这次执行
	RunID() string
}
```

- `Send` 只进这一次执行。执行彻底结束后调用，返回 `ErrRunFinished`，并通过 `*MessageError` 交还原文。
- `Send` 只要接纳就返回 nil。真正送达或失败，通过 `MessageUpdate` 事件告知，最终也记录在 `Result.Messages()` 中。

### 4.3 Standby：待命

```go
type Standby interface {
	Send(ctx context.Context, text string, opts ...CallOption) error // 只投递
	Events() <-chan Event // 持续输出，Standby 的 ctx 结束时关闭
	Cancel()              // 中止当前正在进行的处理，继续待命
}
```

- 类型上没有 `Result()`。待命没有整体结果，每一轮的结果在 `TurnEnd` 中。这一点由编译器保证。
- `th.Standby(serverCtx)` 不需要消息就能创建，界面可以先订阅事件，用户再开始输入。
- 消息路由：正在处理时，按 `When` 并入当前处理；空闲时开始一次新的执行。
- 带执行级选项（`WithModel`、`WithTimeout` 等）的消息不能并入正在运行的执行，因为它的配置已经固定。这类消息排到当前执行之后，用这些选项开始一次新的执行。
- ctx 结束时：正在进行的执行被取消，对应的 `TurnEnd` 带取消错误；待处理的消息以 `MessageUpdate(Failed, cancelled)` 交还原文；最后关闭事件流。
- **有就取、没有就建**：同一进程内，对已经在待命的 Thread 再次调用 `th.Standby(ctx)`，返回已有的 Standby，不报错；生命周期仍由第一次传入的 ctx 决定。HTTP handler 并发打开同一个聊天时不会有一方失败。
- **一次执行失败不影响待命**：进程崩溃、网络中断等导致某次执行失败时，对应的 `TurnEnd` 带错误，待命继续。没投出去的消息本来就是发给这段对话的，自动带入下一次执行；可重试的失败按 `RetryAfter` 自动重试一次（R7 的失败分类），不需要宿主重发。
- **没有消费者时不堵塞**：从未调用过 `Events()` 时，Standby 不缓冲事件；调用之后，按 v1 的背压合同处理（增量事件可聚合丢弃，关键事件不丢）。

### 4.4 消息选项

```go
// MessageOption 只描述消息本身。它同时实现 CallOption，所以也能用于 x.Send、th.Send、sb.Send；
// 而 WithModel、WithTimeout 等执行级选项只实现 CallOption，传给 s.Send 会编译失败。
type MessageOption interface {
	CallOption
	messageOption()
}

func From(a Author) MessageOption       // 消息作者，决定并入时的包装模板与权威表述；默认 AuthorHuman
func MessageID(id string) MessageOption // 幂等键（例如评论 ID）；为空时由 SDK 生成；在 Thread 级别去重，见 §5.2
func When(t Timing) MessageOption       // 投递时机；默认 Soon

type Timing uint8

const (
	Soon      Timing = iota // 最早的边界并入；没有边界可用时，在本轮结束后于同一次执行内续跑
	Interrupt               // 打断当前轮，带着消息在同一次执行内立即重来
	AfterTurn               // 等当前轮结束后，在同一次执行内开新的一轮
)

type AuthorKind uint8

const (
	AuthorHuman  AuthorKind = iota + 1 // 人：只有在明确要求时，才能替换或取消原目标
	AuthorAgent                        // 其他 agent：同伴输入，不具备人的权威
	AuthorSystem                       // 宿主策略：看门狗、预算、外部事件
)

type Author struct {
	Kind AuthorKind
	ID   string
	Name string
}
```

适用范围：

| 调用 | `From` / `MessageID` | `When` | 执行级选项 |
|---|---|---|---|
| `x.Send`、`th.Send`（开始执行） | 有效（用于审计与 `TurnEnd.MessageIDs`） | 无意义，一律立即开始 | 有效，配置这次执行 |
| `s.Send`（补充） | 有效 | 有效 | **编译错误**：`s.Send` 只接受 `MessageOption`，运行中的执行无法改配置 |
| `sb.Send`（待命） | 有效 | 有效 | 排到当前执行之后，开新执行时生效 |

以上新增的选项都不是 `With*` 形式，根包的 `With*` 数量不变。

### 4.5 事件

```go
// Done 是每次执行的最后一个事件，由 core 在全部收尾（checkpoint、结构化输出校验、
// 进程清理）完成后发出，不可被背压丢弃。内容与 Result() 完全一致。
type Done struct {
	eventMetaCarrier
	Result *Result
	Err    error
}

// TurnEnd 标记一次执行内某一轮的结束（团队里是 leader 的一轮）。
type TurnEnd struct {
	eventMetaCarrier
	Turn       int
	Result     *Result  // 这一轮的结果
	Err        error
	MessageIDs []string // 这一轮处理了哪些消息
}

type MessageState uint8

const (
	MessageAccepted  MessageState = iota + 1 // 已接纳
	MessageDelivered                         // provider 已接收，模型将在 Boundary 处看到
	MessageFailed                            // 未投递，Text 中保留原文
)

type MessageUpdate struct {
	eventMetaCarrier
	ID        string
	State     MessageState
	Turn      int
	Boundary  string // "immediate" | "tool" | "stop" | "turn_end"
	Mechanism string // 例如 "codex.turn_steer"、"claude.hook.post_tool_use"、"core.resume"
	Reason    MessageFailure
	Text      string // 失败时的原文（未包装）
}

type MessageFailure string

const (
	MessageCancelled        MessageFailure = "cancelled"         // 执行被取消，或调用方 ctx 在被领取前结束
	MessageProviderRejected MessageFailure = "provider_rejected"
	MessageTimeout          MessageFailure = "timeout"
	MessageLimitExceeded    MessageFailure = "limit_exceeded"    // 达到轮次上限，见 §5.4
	MessageRunFailed        MessageFailure = "run_failed"        // 执行在投递前失败
)
```

v1 的 `RunStarted`/`RunFinished` 保留：在待命的事件流中，它们划分各次执行。`RunFinished` 继续只供参考，权威结果是 `Done`。

`SubagentUpdate` 改为强类型（解决 T8）：

```go
type SubagentUpdate struct {
	eventMetaCarrier
	Task    string
	Member  string
	Kind    SubagentEventKind // started | delta | message | state | finished
	Delta   string
	State   string // queued | running | waiting_approval | succeeded | failed | cancelled | interrupted
	Summary string
	Data    map[string]any // 只用于协议透传，不承载语义字段
}
```

`ApprovalRequest` 增加 `Origin() Origin`，其中 `Origin{Member, Task string}` 用于标识来自团队 worker 的审批请求，单 Agent 时为零值。

### 4.6 Result

```go
type Result struct {
	RunID, Model, Provider, Text, Summary string
	Usage    *Usage
	Metadata map[string]string
	Turns    int // 新增：本次执行内的轮数
	// 非导出：raw、transcript、structured、services、messages、subagents
}

func (r *Result) Raw() RawStreams
func (r *Result) Transcript() []TranscriptItem
func (r *Result) Services() []ServiceReport
func (r *Result) Decode(v any) error
func (r *Result) Messages() []MessageOutcome    // 新增：本次执行所有消息的最终结果，按接纳顺序
func (r *Result) Undelivered() []MessageOutcome // 新增：失败的消息，原文可直接并入下一次 Send
func (r *Result) Subagents() []SubagentResult   // 新增：团队执行中各任务的摘要；单 Agent 为空
```

- 多轮执行：`Text` 是最后一轮的文本；`Usage` 是各轮合计；`Transcript` 包含所有轮。
- 新增 Transcript 条目类型 `TranscriptMessage{ID, From, Mechanism, Boundary, Text}`。它不伪装成 `Role=user`，因为各 provider 的注入方式本质不同。

### 4.7 错误

| 错误 | 何时返回 |
|---|---|
| `ErrThreadBusy`（v1 已有） | 任务模式 `th.Send` 时，该 Thread 已有执行在进行或处于待命；`th.Standby` 时，该 Thread 有任务执行在进行；另一个进程持有该 Thread。同一进程内已在待命时再调用 `th.Standby` 不报错，返回已有的 Standby |
| `ErrRunFinished` | `s.Send` 时执行已彻底结束 |
| `ErrMessageUnsupported` | 当前 Driver 不支持请求的投递方式（例如 `Interrupt`），或者根本不支持运行中消息 |
| `*MessageError{ID, Text, Reason, Err}` | 以上消息相关错误的载体，可以用 `errors.Is` 匹配哨兵错误，原文在 `Text` 中 |

### 4.8 Policy.Messages

放进现有的 `WithPolicy`，不新增 `With*`：

```go
type Policy struct {
	Sandbox   SandboxLevel
	WebSearch FeatureLevel
	Browser   FeatureLevel
	Approvals ApprovalPolicy
	Messages  MessagePolicy // 新增
}

type MessagePolicy struct {
	Disabled   bool                        // 不装配运行中投递（例如不想为 Claude 注册 hook）
	Format     func(MessageContext) string // nil 表示使用内置模板
	MaxPending int                         // 等待领取的消息上限，默认 32；满了之后 Send 阻塞等待空位，受 ctx 约束
	MaxTurns   int                         // 单次执行的轮数上限，默认 20，见 §5.4
}
```

### 4.9 ctx 规则（沿用 Go 惯例）

| 调用 | ctx 约束什么 |
|---|---|
| `x.Send`、`th.Send` | 这次调用开启的执行：ctx 结束时中止执行 |
| `s.Send`、`sb.Send` | 只约束投递：被领取前 ctx 结束，消息以 `cancelled` 失败 |
| `th.Standby(ctx)` | 整个待命的生命周期，包括待命期间开启的所有执行 |
| `sb.Events()` | 无参数，随 Standby 的 ctx 一起结束 |

在待命模式下，长生命周期的 ctx 只在开启时传一次，handler 里调用 `sb.Send(r.Context(), ...)` 永远是安全的。

### 4.10 模式与并发规则

- 同一个 Thread 同一时刻只能处于一种模式：任务执行进行中，或者待命中。违反时返回 `ErrThreadBusy`。同一进程内重复调用 `th.Standby` 不算违反，返回已有的 Standby。
- 任务模式下，`th.Send` 不会并入正在进行的执行；补充只能通过 `s.Send`。这保证了 `th.Send` 返回的 Stream 永远对应这条消息开启的执行。
- 待命期间，Standby 持有该 Thread 的所有权租约，并持续续期。其他进程对该 Thread 的任何调用都返回 `ErrThreadBusy`；进程崩溃后，租约到期自动释放。
- 任务模式下，租约只在执行期间持有。

---

## 5. 语义合同

### 5.1 一次执行何时结束（单 Agent 与团队统一）

> 一次执行在"没有东西在运行，也没有待处理的消息"时结束。

- **单 Agent**：provider 的一轮结束时，如果还有待处理的消息（没赶上边界的 `Soon`、`AfterTurn`），就在同一次执行内续跑一轮。所以 `s.Send` 只要被接纳，就一定会在这次执行里得到处理，除非执行被取消、失败或触及上限。
- **团队**：leader 空闲，没有运行中或排队中的任务，也没有需要唤醒 leader 的消息。
- 续跑由 Driver 在同一进程内完成（`ContinueInRun`），或者由 core 用刚产生的 checkpoint 再调用一次 Driver（`ContinueResume`），见 §6.1。四个内置 Driver 都有 SessionCodec 并支持 resume（✅ `*/session_codec.go`），所以每个 Driver 至少都能在轮次结束时投递消息。

### 5.2 消息投递合同

```text
  Send 接纳                Driver Claim(boundary)          Driver Ack(nil)
 ──────────▶ pending ──────────────────────────▶ claimed ──────────────────▶ delivered
               │  ctx 结束、执行取消、执行失败、上限       │ Ack(err)、进程退出
               └────────────────▶ failed ◀──────────────┘
 校验失败（不支持、过大、执行已结束）：同步返回 *MessageError，不进入状态机
 边界投递失败：退回 pending，改在本轮结束后续接投递（见第 9 条）
```

1. **领取后只由 Ack 决定结果**：Driver 一旦领取，调用方取消不能抢先判失败（对应 multica 的 `writing` 标记 ✅ `claude_supplement.go`）。
2. **未领取的可以撤回**：ctx 结束时，pending 项移出队列，以 `cancelled` 失败，之后任何边界都不会再投递它。
3. **exactly-once**：重复的 `MessageID` 不会重复投递。去重范围是 Thread：已投递的 ID 记录在 Thread 记录中（有界窗口，默认保留最近 1000 个或 7 天），所以进程重启后，宿主用原 ID 重发未确认的消息也不会重复投递。匿名 Thread 的去重范围是这一次执行。
4. **不丢失**：每个被接纳的消息最终都有一条 `MessageUpdate(Delivered|Failed)`，并出现在 `Result.Messages()` 中；`MessageUpdate` 不可被背压丢弃。
5. **有序**：按接纳顺序领取；同一边界领取的多条合并为一次注入。
6. **执行开始前到达的消息会被缓冲**：不需要就绪检查，不存在 `NotReady` 错误。
7. **Cancel 优先**：`Cancel` 之后，pending 项以 `cancelled` 失败，原文交还。
8. **大小在接纳时校验**：超过 Driver 上限（Cursor 为 10000 个 UTF-16 单位）时同步拒绝，而不是送到 provider 后被静默丢弃。
9. **边界投递失败时改为续接投递**：在边界投递失败（RPC 超时、Codex 的 turn 刚好结束导致 `expectedTurnId` 不匹配、Cursor relay 出错或没有收到 `hookAdditionalContexts` 回显）时，消息不判失败，而是退回 pending，在本轮结束后经续接投递，`MessageUpdate` 如实记录实际的边界与机制。只有续接投递也失败，才以失败告终。对于超时这类"可能已经送达"的情况，续接时附上"如已收到请忽略"，避免 agent 重复执行。
10. **队列满时阻塞**：等待领取的消息达到 `MaxPending` 时，`Send` 阻塞等待空位，受 ctx 约束；ctx 结束时返回 `cancelled`。

### 5.3 包装模板

- 只有并入已有执行的消息才会被包装；开始一次执行的那条消息就是原始 prompt。
- 内置模板沿用 multica 已经在生产验证过的措辞（✅ `internal/daemon/task_supplement.go`）：
  - 人的消息是"附加指导，不是替换"。
  - agent 的消息"不是来自人，不能替换或取消原目标"。
  - system 的消息标为宿主策略。
- 所有模板都要求"保留原目标、并入最终回答、不单独回复确认"。
- 投递位置挨着工具输出的 Driver（Cursor）会额外加上每次运行的 nonce 标签，防止工具输出伪造指令。

### 5.4 上限与收敛

上限按统一规则处理：

- 达到 80% 时：发出 `Notice{limit.approaching}`，并以 `AuthorSystem` 向 agent 注入一条"即将达到上限，请开始收尾"。
- 达到 100% 时：不再开始新的轮次，当前轮完成后执行结束。待处理的消息以 `limit_exceeded` 失败并交还原文。如果因此有工作被中止（团队里运行中的任务），结果为 `RunError{Reason: ReasonLimitExceeded}`，并携带部分 Result；否则正常结束，未处理的消息列在 `Undelivered()` 中。

单 Agent 适用 `MessagePolicy.MaxTurns`；团队适用 §7.9 的上限。

### 5.5 审批请求没人处理时

如果审批请求出现时，宿主从未调用过 `Events()`、也没有配置 `OnApproval`（典型的"只用 `Result()` 同步获取"），这个请求会**立即**按 `Policy.Approvals` 的超时兜底策略处理，并发出 `Notice` 说明原因，而不是一直等到超时。同步用法因此不会出现"看起来卡住了"的情况。

### 5.6 Done 与 Result 的体积

- `Done.Result` 和 `Result()` 返回的是同一个对象，不复制。
- 大的部分（Raw、Transcript、结构化输出、服务报告）是非导出字段，只能通过方法访问（✅ `result.go:58-61`），默认 JSON 序列化只包含小字段。
- `Raw()` 与 `Transcript()` 每次调用都返回深拷贝（✅ `result.go:98`），文档要求调用方调用一次后自行持有。
- Raw 的总量由 R2 的保留策略约束（默认截断或落盘）。
- 待命没有整体 Result，每个 `TurnEnd` 只携带这一轮的结果，交付后 SDK 立即释放，不会随运行时长累积。
- 团队 Result 只包含 leader 的结果和各任务摘要（`Subagents()`）。worker 的 Raw 和 Transcript 通过任务句柄按需获取，不随成员数量放大。
- bridges 与会话录制器把 `Done` 映射为协议里的最终消息时，只带安全字段；Raw 等额外内容由 `ExposurePolicy` 显式开启（沿用 A2A bridge 现有做法）。

### 5.7 故障与兜底

原则：

1. **宿主写错了**（可确定、可复现）：尽量变成编译错误，做不到的在运行时立即报错。
2. **暂时性故障**：有界重试，保留状态，不把未投递的消息甩给宿主。
3. **只有真正需要宿主做决定时才返回错误**，而且一定带回原文或部分结果。

| 故障 | 兜底 |
|---|---|
| 在边界投递失败 | 改为在本轮结束后续接投递（§5.2 第 9 条） |
| 待领取队列满 | `Send` 阻塞等待空位（§5.2 第 10 条） |
| 待命中某次执行失败 | 待命继续；没投出去的消息带入下一次执行；可重试的失败自动重试一次（§4.3） |
| resume 被 provider 拒绝，或会话过期（长时间待命很常见） | 开新会话时发出连续性事件（P2-8 的 `NoticeThreadContinuity`），并把 Thread 记录中保存的已投递补充与最近 3 轮回答（总计不超过 8 KiB）作为前缀注入，尽量保住上下文。宿主通过 P2-8 的 `OnRestart` 提供了连续性文案时，以宿主的为准 |
| 待命中切换模型，或其他配置变化导致与现有会话不兼容 | Driver 声明支持换模型续接（P2-11 的 `ResumeTolerant`）时直接续接；否则按上一行处理 |
| checkpoint 持久化失败（store 故障） | 进程内保留最新 checkpoint 作为权威，后台有界退避重试写入，并发出 `Notice`；下一次执行开始前先补写，仍失败时用进程内的 checkpoint 续接，并继续重试 |
| 进程崩溃时还有已接纳、未投递的消息 | `MessageID` 在 Thread 级别去重（§5.2 第 3 条）：宿主重启后，用原 ID 重发没收到 `Delivered` 的消息即可，已投递的不会重复 |
| 团队中 leader 某一轮失败 | 运行中的任务不受影响，leader 通过 resume 重新唤醒（见 §7.9） |

有意保留为错误的情况：

| 情况 | 理由 |
|---|---|
| 任务模式下 Thread 正忙时再 `th.Send`（`ErrThreadBusy`） | 保持任务模式"一次只有一个执行"的明确语义；需要补充时使用 `s.Send` |
| 待命与任务模式冲突（`ErrThreadBusy`） | 宿主写错了，而且每次都会复现 |
| Thread 被另一个进程持有（`ErrThreadBusy`） | 当前进程无能为力，只能由宿主路由到持有方 |
| 执行结束后 `s.Send`（`ErrRunFinished`） | 这是路由信号，错误里带着原文；multica 靠它把评论转为新任务 |
| `When(Interrupt)` 但 Driver 不支持（`ErrMessageUnsupported`） | 打断是宿主明确要求的强语义，由宿主决定改用 `Soon` 还是取消后重来 |
| 消息超过 Driver 上限（同步拒绝） | 由宿主决定如何精简或拆分 |
| Driver 完全不支持续接（`ErrMessageUnsupported`） | 四个内置 Driver 都支持，只影响第三方 Driver，必须如实报告 |
| 上限触顶后硬停 | 80% 时已提醒收尾；这是防止无限循环和失控花费的最后防线 |
| ctx 被取消 | 这是宿主自己的意图 |

---

## 6. Driver SPI 与实现

### 6.1 SPI

core 统一持有队列、幂等、排序、取消竞态与记账；Driver 只负责在自己的边界领取消息并上报结果。

```go
package driver

type Boundary string

const (
	BoundaryImmediate Boundary = "immediate" // provider 立即接收（Codex turn/steer、ACP 扩展）
	BoundaryTool      Boundary = "tool"      // 工具调用前后（Claude/Cursor 的 hook）
	BoundaryStop      Boundary = "stop"      // agent 准备结束时（Claude Stop hook）
	BoundaryTurnEnd   Boundary = "turn_end"  // provider 的一轮结束后
)

type ContinueMode uint8

const (
	ContinueResume ContinueMode = iota // Driver.Run 在一轮后返回，由 core 用 checkpoint 再次调用（默认，所有支持 resume 的 Driver）
	ContinueInRun                      // Driver 在同一次 Run 内自行续跑（常驻进程、Stop hook）
)

type MessageCapability struct {
	Boundaries        []Boundary
	Interrupt         bool
	Continue          ContinueMode
	MaxUnits          int    // 0 表示无已知上限
	Unit              string // "bytes" | "utf16"
	ConfirmedDelivery bool   // 是否以 provider 回显为准（Cursor），而不是写入成功
	PersistsDelivered bool   // 已投递的内容在 resume 后是否仍在 provider 历史中
	MinVersion        string
}

// Descriptor 增加 Messages MessageCapability。Inspect().Environment 返回按本机版本裁剪后的实际能力。

type Request struct {
	// ...v1 字段
	Messages MessagePort // nil 表示本次执行未启用
}

type MessagePort interface {
	Window(open bool)                // provider 确认的窗口状态变化
	Signal() <-chan struct{}         // 有新消息待领取时就绪
	Claim(b Boundary) []MessageItem  // 领取在边界 b 可以投递的全部消息（已包装）
	Ack(id string, err error)        // 每个领取项恰好调用一次
}

type MessageItem struct {
	ID     string
	Text   string
	Timing Timing
}
```

领取规则：

- `Soon` 可以在任意边界领取。
- `AfterTurn` 只在 `BoundaryTurnEnd` 领取。
- `Interrupt` 只在 `BoundaryImmediate` 领取；Driver 不支持时，在接纳阶段就以 `ErrMessageUnsupported` 拒绝。
- `ContinueInRun` 的 Driver 在每轮结束时调用 `Claim(BoundaryTurnEnd)`，有内容就续跑，否则结束。`ContinueResume` 的 Driver 由 core 检查剩余消息，再决定是否续接。
- Driver 每结束一轮，都要发出 `turn.completed` 驱动事件，core 据此生成 `TurnEnd`。

### 6.2 实现矩阵

| Driver | 边界 | Interrupt | 续跑 | 窗口开启条件 | 备注 |
|---|---|---|---|---|---|
| Codex app-server | ✅ `immediate`：`turn/steer {threadId, expectedTurnId, input}`，需要 ≥0.100.0 | ✅ `turn/interrupt` 后在同一 thread 发 `turn/start` | InRun：在同一进程发 `turn/start` | 已有 threadId 且有 activeTurnId | `expectedTurnId` 保证不会误投到下一轮。✅ 流式执行与常驻 Thread 已经走 app-server（`codex/driver.go`、`codex/persistent.go`） |
| Codex exec | 无 | 无 | Resume | — | 只在轮次结束时投递 |
| Claude Code | ✅ `tool`：SDK hook 回调（在 `initialize` 中注册 `UserPromptSubmit`/`PreToolUse`/`PostToolUse`/`PostToolUseFailure`）返回 `additionalContext`，需要 ≥2.1.110；✅ `stop`：Stop hook 返回 `decision:block` 加 `reason` | ⚠️ `interrupt` control_request 后写入一个 user frame | InRun（Stop hook 与常驻进程） | 第一次收到自己注册的 hook 回调 | ✅ 必须过滤 `agent_id` 与 `parent_tool_use_id`，只投给主循环。本仓库 Claude Driver 已经在 stdio 上跑 control 协议处理 `can_use_tool` |
| CodeBuddy | ⚠️ 预期同 Claude（本仓库已有 `control.go`、`control_parser.go`） | ⚠️ | InRun 或 Resume | ⚠️ | — |
| Cursor | ✅ `tool`：项目级 hooks，`postToolUse(Failure)` 返回 `additional_context`，需要 ≥2026.10.14 | 无 | Resume（✅ `--resume`，`cursor/driver.go:388`） | 第一次收到 hook 请求 | 以 `hookAdditionalContexts` 回显为准；`--print` 模式下 stop hook 不触发，最后一段输出期间到达的消息走 Resume 续跑 |
| ACP：Kiro | ✅ `immediate`：`_session/steer`，需要 ≥2.33.0 | `session/cancel` 后再 prompt | InRun：同一 session 再发 `session/prompt` | `session/prompt` 进行中 | — |
| ACP：Grok | ✅ `immediate`：`_x.ai/interject`，需要 ≥1.0.14 | 同上 | 同上 | 同上 | 结果帧内可能带错误，需要解析 |

ACP 标准里没有 steer。通用 ACP Driver 提供按预设挂载的扩展点：`acp.Preset.Message func(sessionID, text string) (method string, params any)`。

### 6.3 Cursor 的 hook relay

Cursor 在 headless 模式下没有输入通道。它在每次工具调用后，会启动 `.cursor/hooks.json` 中声明的命令，并读取该命令的 stdout。relay 就是这条命令：它经 loopback 加 token 连回宿主，取回待投递的内容后打印出来。

- **默认方式：宿主二进制重新执行自身。** 宿主在 `main()` 第一行调用 `cursor.RunHookRelayIfRequested()`。检测到隐藏参数时，它只执行 relay 逻辑然后退出；否则立即返回。SDK 用 `os.Executable()` 生成 hook 命令，宿主无需单独发布任何东西。这和 multica 的做法相同（✅ `cmd/multica/main.go:113`）。
- **覆盖方式：** `cursor.Config.HookRelay []string` 显式指定命令。
- **启动前自检：** SDK 用探测参数执行一次 relay 命令。失败时，Cursor 的 `tool` 边界在本次执行中被如实裁掉，消息改为在轮次结束时经 Resume 投递。功能可用，只是时机更晚，并发出 `Notice` 说明原因。
- **只在受管理的工作目录里写 hooks.json：** 已存在非 SDK 管理的 `.cursor/hooks.json`，或者工作目录是用户自己的仓库目录时，同样降级为轮次结束时投递（沿用 multica 的规则 ✅ `internal/daemon/daemon.go:285`）。
- **文档要求：** relay 检测之前不能有重型 `init()`，因为每次工具调用都会启动一次宿主二进制。

### 6.4 需要核实的项目与确定的降级行为

| 项目 | 核实方式 | 核实失败时的确定行为 |
|---|---|---|
| Claude 的 Interrupt | live 测试 `interrupt` control_request 后能否在同一 session 继续 | Claude 声明 `Interrupt=false`；`When(Interrupt)` 同步返回 `ErrMessageUnsupported` |
| Claude 的 hook 内容在 resume 后是否保留 | live：投递后结束，resume 后询问内容 | 声明 `PersistsDelivered=false`；core 在 Thread 记录中保存已投递消息，下次执行开始时作为"之前的补充"前缀重放，Thread 的语义不受影响 |
| CodeBuddy 的原生边界 | live 测试 | 不声明对应边界；消息在轮次结束时经 Resume 投递 |
| Cursor relay 在 Windows 上（Cursor 用 PowerShell 执行 hook） | live 测试引号与路径 | Windows 上不启用 relay；消息在轮次结束时经 Resume 投递 |
| 匿名 Thread 的 checkpoint（Driver 的无状态模式是否产出可续接的会话） | 各 Driver 的无状态模式测试 | 该 Driver 在匿名执行中启用会话持久化，执行结束后清理；仍然做不到则声明不支持运行中消息，`s.Send` 同步返回 `ErrMessageUnsupported` |
| Remote A2A 成员是否接受对 working 状态 task 的消息 | 对接测试 | `team_send` 给 leader 返回明确错误；leader 可以取消任务后重新派发 |

---

## 7. 团队：`hosttools/team`（实现裁决）

以下以当前代码与合同测试为准；原设计和实现不同处在对应小节明确记录。
完整英文用法见 [team guide](team.md)。

### 7.1 入口与成员

`team.New` 返回 `(*team.Team, error)` 并打开一个 loopback MCP listener。
Team 实现 `adaptor.Runner`；Team Thread 实现 `adaptor.ThreadRunner`。
以下函数可接入宿主已构造的 Agents（导入根包为 adaptor，另导入 hosttools/team）：

```go
func NewTeam(leader, implementer *adaptor.Agent) (*team.Team, error) {
	return team.New(team.Config{
		Leader: leader,
		Members: []team.Member{{
			Key: "impl", Description: "实现与测试", Runner: implementer,
			Continuity: team.PerScope,
		}},
		Store: team.NewMemoryStore(),
	})
}
```

`Config` 的最终字段为 `Leader`、`Members`、`Store`、`Limits`、`Surface`。
`Member` 的最终字段为 `Key`、`Name`、`Description`、`Skills`、`Runner`、
`Remote`、`Continuity`、`Input`、`Output`、`MaxActive`、`Timeout`；Runner 与
Remote 必须且只能指定一个。Skills 是名册描述，不安装 skill。

- `t.Send(ctx, text)`：匿名私有内存 scope，不使用持久化 Config.Store。
- `t.Thread(key).Send(ctx, text)`：具名任务；ctx 约束整个团队执行。
- `t.Thread(key).Standby(lifetime)`：具名待命；首次 ctx 持有生命周期，重复调用取同一对象。
- task-mode Send 在 active execution、live Standby（即使空闲）或其他进程的有效
  scope lease 下返回失败 Stream，`Done.Err` / `Result()` 可匹配 `ErrThreadBusy`。
  不自动并入；补充用原 Stream.Send，待命输入用 Standby.Send。
- Standby 无整体 Result/RunID；Cancel 中止当前团队处理与队列但继续待命，结束 ctx 才退出。
  消息选项可并入；带 execution options 的消息等自己的下一次执行。
- `Team.Close(ctx)` 是宿主组件资源回收操作，幂等停止 listener、执行与 Standby，
  不关闭宿主 Agents。根执行对象依然使用 Cancel，没有新增执行动词。

Local Agents 与嵌套 Team 直接支持对话；自定义 Runner 可以实现
`ConversationProvider.Conversation(key) adaptor.ThreadRunner`。
内部 key 使用 `EncodeKey` 的字节长度前缀，分别编码 `(scope, "leader")`、
`(scope, "member", memberKey)` 或 `(scope, "task", taskID)`，宿主 key 原样保存。

### 7.2 一次团队执行与委派优先

一次执行在 leader 完成收尾、没有 running/queued worker、没有待处理的唤醒消息
或投递时结束。leader 每个 provider turn 产生 TurnEnd，worker 活动以 typed
SubagentUpdate 并入同一流，最后一个事件为 Done，与 Result() 一致。

leader 是完整 Agent。Preamble 要求委派优先：理解、拆分、派发、纠偏、验收、集成与
回答归 leader；成员描述/skills 能承接的工作必须派发；没有成员能承接的才亲自实现。
这是模型指令，SDK 不判断业务角色。宿主可用只读 sandbox 强制纯统筹。workspace
仍由各 Agent 配置；共享目录写冲突由宿主/leader 拆分工作解决。

派发返回后 leader 可做独立工作；需要 worker 结果时结束本轮，通知会重新唤醒，
不阻塞工具调用，不需要 wait 工具。若没有派发却声称等待，执行会正常结束并暴露回答；
宿主可以用具名 Thread 跟进。

### 7.3 leader 代传、旁注与唤醒

- 用户输入只给 leader；leader 按 task handle 转达 worker；worker 只给 leader，
  不直接寻址其他 worker。作者由 capability 推导，peer 不具备人的权威。
- worker 显式 send、任务终态和宿主消息唤醒 leader；leader 主动取消的任务不再次唤醒。
- leader 忙时通知以 Soon 并入，实际边界由 Driver 能力决定，可为 immediate、tool、
  stop 或 turn_end 续接。**实现不承诺“当前工具调用结束前”的通用延迟上界**。
- leader 空闲时 worker 通知默认两秒窗口合并；host 输入可立即开下一次 leader 执行。
- 进度旁注不单独唤醒，随下一次通知投递；任务终态通常提供该唤醒。leader 主动取消
  的任务未读旁注保留在对应 Subagents().Unread。

### 7.4 固定工具面

| 工具 | leader | Local worker | 输入 → 输出 |
|---|---|---|---|
| `team_delegate` | ✓ | 外层不提供 | `{member, objective, input?, continue?}` → `{task, state:"queued"}` |
| `team_send` | 发给运行中的任务 | 只能发给 leader | leader `{task,text,when?}`；worker `{text,expect_reply?}` → 接纳结果 |
| `team_status` | ✓ | 只读同 scope | `{tasks?}` → 名册、active/max_active、任务状态/摘要/reason |
| `team_cancel` | ✓ | ✗ | `{task,reason}` → 取消结果 |

leader 四个、Local worker 两个，数量与成员数无关；嵌套 Team 自己的工具属于内层，
`Surface.Prefix` 可替换默认 team_。成员自己的工具不投影给 leader。Remote 不接收
本进程 loopback 的 worker 工具，工具面由远端服务管理。

名册通过 RunAttachment.Preamble 注入，不进入 fingerprint。delegate.member
枚举成员 key；Member.Input 在服务端登记前校验，失败返回结构化错误与 schema；
Member.Output 只用于本地成员结构化输出。worker expect_reply 是 schema 提示，
当前实现所有显式 send 都唤醒，不同步等待回答。发送到 finished/queued 任务分别返回
`task_finished` / `task_queued`；完成后应 delegate(continue: task)。

### 7.5 任务与会话连续

任务状态为 queued → running → waiting_approval? → succeeded | failed | cancelled |
interrupted。delegate 在返回前持久登记；MaxActiveTasks、Member.MaxActive 与同一
conversation 单 writer 共同约束调度，超额排队。

**实现对 PerTask 的细化**：Agent/嵌套 Team 使用每任务隔离 Thread，保留显式 continue
所需上下文，而不是一律匿名 Send。普通 Runner 无 conversation 支持时使用 Send，
不能继续。PerScope 在同一 scope 内共享成员 Thread，即使 MaxActive 大于一也串行。
continue 必须指向同成员终态任务，本地验证健康 checkpoint；Remote 验证已观察 context。
每次继续都有新 task handle，不覆盖旧结果。

### 7.6 worker 审批与 Remote 限制

Local worker 的 bound ApprovalRequest 在同一流转发，携带 Origin{Member, Task} 并
共享原 exactly-once responder；宿主用 Approve/Deny/Answer。成员自己的 OnApproval
与 Policy.Approvals 决定超时/兜底，等待状态为 waiting_approval。同步-only 消费按
成员无消费者策略兜底。leader 不代用户审批；任务问题用 team_send 向 leader 提问。

**Remote 最终裁决**：客户端未协商 portable approval-response 扩展。input-required
只报告 waiting_approval，不能构造可应答的 ApprovalRequest。AllowInputRequired=true
时继续观察，由远端策略处理；默认失败。原“映射成同样 ApprovalRequest”设计不成立。

### 7.7 持久化、恢复与 lazy Fork

TaskStore 保存 task、leader inbox、immutable artifact 与 Fork lineage。必须提供
scope owner/token fencing、CAS revision、原子 task/wake 提交、按序独占 claim、
exact claim token 应答和过期 reclaim，并通过 teamstoretest.Run。MemoryStore 不做
崩溃持久化或自动保留期清理；具名 scope 的 durable store 由宿主实现。Agent Thread
stores 另行持久化，不可由 TaskStore 替代。

恢复取得 lease 后，首次具名执行在 leader 前把非终态任务标为 interrupted，reason
为 execution_lost，Preamble 告知 leader，由其决定重派；不会自动重新启动 worker。
Standby 需宿主重开，重开本身不启动执行。

宿主消息先保存再交 provider，保留原 MessageID、内容、From、Timing。delivered ID
去重，failed ID 可按原内容重交。Go execution options 不序列化；RequiresOptions
消息在 owner 丢失后保持 Deferred，直到宿主恢复原选项并以原 ID 重交后 dispatch，
禁止用新默认值执行。无 execution options 的 pending 消息可在 lease 恢复后重新领取。
provider 接收后、回执持久化前崩溃仍可能重放，MaybeDuplicate 标注含糊投递；不承诺
provider exactly-once。匿名团队 scope 崩溃即丢失。

Fork 的最终签名是 `(*team.Thread).Fork(newKey) (*team.Thread, error)`：

- 父/目标 scope 都必须 idle 且无 live Standby；父 leader checkpoint 健康；目标无
  task/inbox/artifact/lineage 或 leader checkpoint。busy 报 ErrThreadBusy，冲突报错。
- atomically 复制终态任务和 artifact，记录 lineage，不复制 inbox，不修改父历史。
- **lazy Fork**：leader 与支持的 Local PerScope 成员在子首次执行时通过 core Fork；
  父 checkpoint 在那时读取，不是 Fork 调用时的快照。provider/兼容错误可延后出现；
  无父 checkpoint 的未使用成员 fresh start。Agent/嵌套 Team 支持，自定义 provider
  可以在 lazy 阶段拒绝。
- PerTask conversation 链接清空，复制的历史不能 continue 回写父对话。
- A2A 无 portable conversation Fork；只要名册有 PerScope Remote 就明确拒绝。

### 7.8 用量与结果

Team 用 ComposeResults 按顺序组合所有 leader 执行，Text、Summary、Model、Provider、
Metadata、structured output 来自最后一个；Raw stdout/stderr、Transcript、Services、
Messages 串接，message turn 编号重基。Raw.Terminal 保留最后观察 payload（单 slot）。
Turns 为 leader provider turns，Usage 合计观察到的 leader/worker 用量并防重复计数。

Subagents() 返回 Member、Task、State、Summary、Usage、Unread 摘要副本；完整本地
worker Result 与 RemoteSnapshot（含非文本 artifact/protocol data）、RemoteTask、
RemoteContext 保存在 TaskStore。Remote 不伪造本地 Raw/Transcript/Usage。worker
失败可由 leader 处理，不能仅用 team Result 成功判断所有子任务成功。

### 7.9 上限、超时与失败

| 上限 | 默认值 | 作用域 |
|---|---|---|
| MaxLeaderTurns | 50 | 每次团队执行所有 leader provider turns |
| MaxTasks | 20 | 每次团队执行新登记任务 |
| MaxActiveTasks / Member.MaxActive | 4 / 1 | 并发，超额排队；同 conversation 仍单 writer |
| MaxAgentMessages / MaxSenderMessages | 10 / 3 | 每个 leader turn 的显式 send |
| Member.Timeout | 30 分钟 | 每个已启动任务；零为默认，负数拒绝，不无限 |
| Remote 无观察时限 | 2 分钟 | 断线或静默流判 failed/unknown_state |
| WakeWindow | 2 秒 | 空闲 worker 通知合并 |
| RetryBackoff | 100 毫秒 | leader 最小重试退避 |

临近上限发 limit.approaching Notice，尚有预算时注入 system 收尾通知。到硬上限停止
接纳，leader 收尾后取消未完成任务。必须中止工作时返回匹配 ErrLimitExceeded 的
RunError，ReasonLimitExceeded 携带部分 Result；正好在 cap 处干净结束可以成功。

每次启动 leader 时，在 caller options 之后用 RunSettings.CapMessageTurns 降低 core
剩余轮预算，不覆盖其余 policy，也不提高宿主更小的 cap。core 每次执行默认 20 轮；
leaf cap 到达可正常结束并报告 pending 消息 MessageLimitExceeded。团队仍受总预算
约束。MaxLeaderTurns 不限制单轮时间，宿主设置 leader timeout 与外层 ctx。

leader 错误只有实现 team.RetryableError 且 RetryAfter>=0 才最多重试两次；ctx
取消/截止不重试。重试期间 worker 继续，退避至少 RetryBackoff。不可重试或耗尽时
取消 worker，携带部分 Result。Standby 另有最多一次 RetryableError 重试且需
undelivered input；否则 carry 等下一条宿主消息。同 ID 可先 failed/run_failed 后
再 delivered。**Team 未实现原提案的自动 worker R5 提醒**；宿主可先发 system
提醒再 Cancel，Member.Timeout 为运行上限。

### 7.10 安全

工具 schema 严格拒绝伪造 From/target，peer/system 输入明确不具备人的权威。task
reason 为稳定原因码；不承诺所有本地错误都只进入日志。单条通知/send 超 64 KiB、
合并通知超 256 KiB 时保存完整 immutable artifact，以同 scope MCP resource 引用
替代，不静默截断。secret 不进入 metadata、Service reports 或普通日志。

### 7.11 传输与 Remote 最终边界

每个 Team 仅一个 IPv4 loopback listener。每 execution token 绑定 scope、actor、
task、runID、工具与可达目标，通过 SecretEnv 注入，detach 撤销。嵌套使用独立 Team
listener 与可配置 prefix，不为每个 worker 开 listener。

RemoteSpec 最终包含 AgentCardURL、可选 fingerprint pin AgentCard、Protocol（空或
"a2a"）、Tenant、Auth、HTTPClient、TrustedAuthOrigins、AcceptedOutputModes、
PreferredTransports、Policy。discovery 与凭证沿用 clients/a2a 的 origin 限制。

RemotePolicy 默认 PollInterval=1s、MaxPolls=1800、MaxArtifactBytes=1MiB；MaxTimeout
可缩短成员 timeout，RequireStreaming 拒绝不支持流的 endpoint。PerScope 使用已
观察 remote context。team_send 调用同一 task/context 的 A2A message/send，远端
拒绝返回工具错误。**补充仅 Soon**，Interrupt/AfterTurn 匹配 ErrMessageUnsupported；
不静默改变 timing。**无 output-schema 协商**，Remote Member.Output 构造即拒绝；
media modes 不能替代 schema。初始请求遇含糊失败不重放；观察丢失两分钟失败唤醒。
Remote input-required 与 Fork 限制见 §7.6–§7.7。

---

## 8. 宿主用法走查（最终 API）

以下示例函数需导入 context、fmt、根包 adaptor、hosttools/team；回调由宿主实现。

**一次性 CLI / 完成前验证**：用具名 Thread 跟进，每次尝试有独立 Result。

```go
func VerifyTask(ctx context.Context, t *team.Team, key, objective string,
	verify func(context.Context) (string, bool),
) (*adaptor.Result, error) {
	th := t.Thread(key)
	res, err := th.Send(ctx, objective).Result()
	for attempt := 0; err == nil; attempt++ {
		report, passed := verify(ctx)
		if passed {
			return res, nil
		}
		if attempt == 3 {
			return res, fmt.Errorf("verification failed after three follow-ups: %s", report)
		}
		res, err = th.Send(ctx, "测试失败，请派发修复：\n"+report).Result()
	}
	return res, err
}
```

**看过程的一次性任务**：Done 自带权威结果。approval 回调必须由宿主显示/应答，
worker 请求的 Origin 标明来源；不要把权限审批交给 leader。

```go
func ObserveTask(ctx context.Context, t *team.Team, objective string,
	publish func(adaptor.Event), ask func(*adaptor.ApprovalRequest),
) (*adaptor.Result, error) {
	s := t.Send(ctx, objective)
	defer s.Cancel()
	for ev := range s.Events() {
		switch e := ev.(type) {
		case *adaptor.ApprovalRequest:
			ask(e)
		case adaptor.Done:
			return e.Result, e.Err
		default:
			publish(ev)
		}
	}
	return s.Result()
}
```

**Web/IM**：打开对话时一次调用 t.Thread(chatID).Standby(serverCtx)，一次订阅 Events，
由宿主 hub 广播多个页面。每个请求调用 sb.Send(requestCtx, text, MessageID(id),
From(user))；IM 在 TurnEnd 用 MessageIDs 引用输入。停止按钮 sb.Cancel，离开对话
结束 server-owned ctx。带 WithModel 等 execution options 的输入排在当前执行之后。
未订阅前不缓冲事件；详细可编译函数见 [team guide](team.md)。

**看门狗、预算与外部事件**：宿主根据真实观察发 Stream.Send 或 Standby.Send，带
AuthorSystem；接近预算先提醒收尾，再按宿主策略 Cancel。Team 不自动实现 R5。

**HITL 补充**：先应答审批，再补方向；以下函数用于 Permission/PlanReview，Question
使用 Answer。

```go
func DenyAndRedirect(ctx context.Context, req *adaptor.ApprovalRequest,
	s adaptor.Stream, user adaptor.Author,
) error {
	if err := req.Deny(ctx, "不要直接改 public API"); err != nil {
		return err
	}
	return s.Send(ctx, "新增内部函数并验证现有调用方", adaptor.From(user))
}
```

**bridges 的实现边界**：

- A2A 当前每次执行请求调用 target.Send，**未实现**原设计的 working taskID →
  active Stream.Send 路由；upstream 可拒绝 working-task 请求。context 绑定的忙碌
  Thread 报错，bridge 投影为 failed task，不静默并入。ThreadByContextID 仅适配
  *adaptor.Agent；发布 Team 可传已选择的 Team Thread。wire schema 未投影
  TurnEnd/MessageUpdate/SubagentUpdate/Done，不能宣称团队事件完整透传。
- AG-UI 一次 run 对应 Runner.Send，MessageUpdate/TurnEnd 投影为 custom events，
  typed SubagentUpdate 为 Activity。当前 Team 未填旧 Activity 所需的完整 result/
  provider-name 字段，因此不能承诺 plan 下载和 provider badge 的端到端保真。
- session recorder 使用 typed Event 信封与宿主脱敏策略，不能把任意完整 Done.Result
  直接当成安全日志输出；本任务不变更 recorder 的实现。

---

## 9. multica 场景走查（实现与限制）

multica 可保留评论黑板、服务端任务存储与 CLI 工具面，只使用 core 消息能力，
不强制采用 Team MCP surface。完整宿主函数见 [team guide](team.md)。

**M1 评论纠偏**：运行开始后就能 Send，无需 SupplementReady。同步错误保留原评论；
接纳后用 MessageUpdate/Result.Messages 确认 delivered/failed，不能将 accepted 当
成功。ErrRunFinished 时由宿主调度具名 Thread 新执行；有上限、取消或 provider
失败时，已接纳消息仍可能明确失败。

```go
func ForwardComment(ctx context.Context, s adaptor.Stream, id, body string,
	from adaptor.Author,
) error {
	return s.Send(ctx, body, adaptor.MessageID(id), adaptor.From(from), adaptor.When(adaptor.Soon))
}
```

**M2 派发后结束本轮**：Team 自动登记 queued task、保活 worker、合并完成通知并重新
唤醒 leader。multica 自己编排时保留 claim/ack 与调度；不是必须迁入 SDK。

**M3 leader 纠偏 worker**：Team 使用 team_send(task, text)，core Stream.Send 携带
AuthorAgent。宿主自编排须自己校验权限与上限；Remote 只支持 Soon，其他 timing
明确拒绝。对终态任务改用 delegate(continue: task)。

**M4 worker 回复时 leader 正忙**：Team 自动以 Soon 补充，投递失败留在 durable inbox
重试；空闲时按 WakeWindow 唤醒。延迟由 Driver boundary 决定，不保证当前工具结束。

**M5 聊天中途输入**：任务模式使用已持有的 Stream.Send；常驻用 Standby.Send。另一次
Thread.Send 不并入而报 ErrThreadBusy；sb.Cancel 保留待命。

**M6 多 provider**：按各 Driver Descriptor.Messages / resolved capability 与 Inspect
Environment 核实，不硬编码旧 multica 版本表或宣称所有 provider 支持相同 timing。
Cursor in-run 需要宿主 hook relay。只有 InRun、无 resume 的 Driver 在最终 boundary
竞态可能报告 failed，而不是无声丢失。Pi/OpenCode 不属于 v2 能力承诺。

**M7 daemon 重启**：未终态团队任务变 interrupted，leader 决定重派。宿主以原 MessageID
重发未确认评论；含 execution options 的 held record 必须恢复原选项再重交。provider
接收与回执落盘之间仍有可重放窗口，不承诺无重复。重开 Standby 后需 host input
启动下一次执行。

**M8 autopilot 长任务**：MaxLeaderTurns、MaxTasks、message caps 与 Member.Timeout
提供边界；宿主 watchdog 可先 system 提醒再取消。Team 未自动实现原 R5 提醒。
临近上限通知与部分 Result 保留，不能把 limit hard stop 当成功。

只有完成 provider fixture/live 以及 multica 宿主接入验证，才可删除对应 provider 的
supplement 实现；SDK 文档不把未完成的 multica 迁移视为已验收。

---

## 10. 设计决策记录

| 决策 | 采用 | 排除的方案与理由 |
|---|---|---|
| 运行中补充的入口 | `Stream.Send` | 包级函数：被包装的 Stream 会静默失效；单独的 `Steer` 动词：和"开始执行"是同一件事，多一个动词 |
| 执行动词 | 只有 `Send`，返回 `Stream` | 保留 `Run`/`Stream`：`Stream` 本来就同时提供同步与异步，`Run` 只是语法糖 |
| 一次性任务的语义 | `Send` 每次新建匿名 Thread | 所有调用共用一个默认 Thread：会合并并发任务、串上下文、上下文无限增长、store key 冲突 |
| 常驻的表达方式 | `th.Standby(serverCtx)` | Agent 级选项：模式属于交互而不属于 Agent，常驻需要身份，组合时冲突；`Send` 上的选项：失去编译期"没有同步结果"的保证，ctx 作用不固定，没有消息就无法订阅；永不结束的 Stream（`KeepOpen`/`UntilCanceled`）：没有停止按钮，Result 无边界，checkpoint 和 lease 问题；`Session`/`Open`/`Live`：命名不贴切 |
| 命名 | `Standby` | `NoExit`/`Nonstop`/`Endless`：否定式，或与行为不符；`Resident`/`Daemon`/`KeepAlive`/`Persistent`：与现有术语撞车；`Open`/`Connect`/`Attach`/`Serve`/`Ready`：含义不符；`OnCall`：语义偏告警值班 |
| 结束操作 | 只有 `Cancel`，只中止正在进行的处理 | 同时有 `Close` 和 `Cancel`：让人困惑；`Cancel` 的结果随调用时的状态而变：隐藏的模式 |
| 结果获取 | `Result()` 与以 `Done` 结尾的 `Events()`，内容一致 | 沿用 v1 的 `RunFinished` 加事后再调 `Result()`：多一步，且 `RunFinished` 在收尾之前发出 |
| 待命的同步获取 | 不提供 | "每条消息一个回复 Stream"：引入合并与共享结果的复杂度 |
| 任务模式中忙碌时的 `th.Send` | 返回 `ErrThreadBusy` | 自动并入：返回的 Stream 可能不处理这条消息（问题 A） |
| 执行结束的定义 | 单 Agent 与团队统一为"没有运行中的东西、也没有待处理的消息" | 以 provider 的一轮结束为准：接纳的消息可能失效 |
| 收尾续跑 | 由 core 统一续跑（InRun 或 Resume），所有 Driver 都可用 | 仅 Claude 用 Stop hook，Codex 声明不支持：能力不一致 |
| leader 的工作方式 | 完整的 Agent，事件驱动，不阻塞；默认委派优先，宿主可收紧为纯派发器 | `team_wait`、StopGate：leader 占着 lease，无法响应用户，还会撞上工具超时；SDK 规定 leader 只能派发：违反 "SDK 不定义团队角色"，也剥夺了 leader 自己动手的能力 |
| 用户与 worker | 只通过 leader 转达（与 multica 一致） | 直接发给 worker：需要寻址字段，leader 不知情 |
| worker 的审批 | O3，交给宿主 | 见 §7.6 |
| ctx | Go 惯例，待命用独立入口固定 ctx 的作用 | — |
| Cursor relay | 宿主二进制重新执行自身，并允许显式覆盖 | 单独发布 relay 工具：分发成本高；curl 脚本：依赖 shell 语法，比较脆弱 |
| 名册 | `Preamble` 注入，不进入 fingerprint | 放进 Instructions：名册一变会话就失效 |
| 失败处理 | 编程错误尽量编译期暴露；暂时性故障有界重试并保留状态；只在需要宿主决定时报错，并带回原文 | 一律报错：把可以自动恢复的故障甩给宿主；一律静默降级：违反"对能力说真话" |
| 忙碌、打断不支持、消息过大 | 保持报错（`ErrThreadBusy`、`ErrMessageUnsupported`、同步拒绝） | 排队、降级为 `Soon`、写临时文件：会改变宿主明确要求的语义，交由宿主决定 |

---

## 11. 问题追踪表

| # | 问题或场景 | 结论 | 章节 |
|---|---|---|---|
| 1 | 三类投递场景（立即、下一次调用、排队合并） | `When`：`Interrupt` / `Soon` / `AfterTurn` | §4.4 |
| 2 | 为什么需要 `SteerReady` | 不需要：执行开始前的消息会被缓冲 | §5.2-6 |
| 3 | 各 Agent 原生 API 差异 | Driver 能力矩阵加统一 SPI | §6 |
| 4 | multica 的持久化 claim/ack | 留在宿主；MessageUpdate / Result.Messages 提供回执；TaskStore 使用 fencing/claim，provider 接收与回执落盘间仍可重放 | §7.7、§9 |
| 5 | 区分人与 agent 的权威 | `From` 加内置模板 | §4.4、§5.3 |
| 6 | Claude hook 被子 agent 继承 | 过滤 `agent_id` 与 `parent_tool_use_id` | §6.2 |
| 7 | 取消与投递的竞态 | 领取后只由 Ack 决定 | §5.2-1、2 |
| 8 | 版本门控 | `MessageCapability.MinVersion` 加 Inspect | §6.1、§9 M6 |
| 9 | 冻结的 Stream 接口与被包装的 Stream | v2 直接扩展接口，编译器强制包装器转发 | §4.2 |
| 10 | 幂等 ID | 宿主提供 `MessageID` | §4.4、§5.2-3 |
| 11 | Transcript 角色 | 独立的 `TranscriptMessage` 条目 | §4.6 |
| 12 | 一次执行跨多轮的影响（Result、Usage、checkpoint） | `TurnEnd` 加汇总 Result；执行结束后保存 checkpoint | §4.5、§4.6、§5.1 |
| 13 | Cursor 的支持情况与 relay 成本 | 宿主二进制重新执行自身，失败时降级为轮次结束时投递 | §6.3 |
| 14 | CodeBuddy 未核实；Pi、OpenCode | CodeBuddy 核实失败时走 Resume；Pi 与 OpenCode 的实现已挂起，不在本提案范围内 | §1、§6.4 |
| 15 | 补充内容在 resume 后是否保留 | `PersistsDelivered`，未保留时由 core 重放 | §6.4 |
| 16 | Codex 收尾续跑的影响 | core 统一续跑 | §5.1、§6.2 |
| 17 | T1 leader 阻塞 | 事件驱动的 leader | §7.2、§7.3 |
| 18 | T2 工具面随成员数增长 | leader 4 个、worker 2 个，名册作为数据 | §7.4 |
| 19 | T3 结果被覆盖 | 任务句柄 | §7.5 |
| 20 | T4 会话不连续 | PerTask 为 Agent/Team 隔离 Thread；PerScope 共享单 writer；continue 验证 checkpoint/context | §7.5 |
| 21 | T5 没有上行通道 | worker 用 `team_send` 发给 leader | §7.3、§7.4 |
| 22 | T6 子任务生命周期 | 团队执行持续到整个团队静止 | §7.2 |
| 23 | T7 每次运行一个 sidecar | 单 listener 加 capability token | §7.11 |
| 24 | T8 弱类型的 SubagentUpdate | typed Task/Member/Kind/State/Delta/Summary；当前 A2A wire 无投影，AG-UI 旧 detail 字段有保真限制 | §7.8、§8 |
| 25 | 用户直接找 worker | 不提供，由 leader 代传 | §7.3 |
| 26 | worker 之间的通信 | 不提供，由 leader 转达 | §7.3 |
| 27 | 一次性进程什么时候退出 | 执行在静止时结束，`Result()` 或 `Done` 返回 | §5.1、§8 |
| 28 | 常驻与一次性如何统一 | 三种用法，同一组动词 | §3 |
| 29 | 由宿主决定是否常驻 | 宿主选择用法；待命由宿主的 ctx 决定何时结束 | §3、§4.9 |
| 30 | leader 说"我等着"却没有派发 | 模型行为风险，不是接口缺口：执行正常结束，回答可见；briefing 要求"需要等待时必须先派发"；宿主可以用带记忆的任务跟进 | §7.2、§8 |
| 31 | 不唤醒 leader 的旁注怎么算 | 旁注的不变式 | §7.3 |
| 32 | worker 卡死 | Member.Timeout 默认 30 分钟；Team 无自动 R5 提醒，宿主可先 system 提醒再取消 | §7.9 |
| 33 | 跨轮次的循环 | `MaxLeaderTurns` 与统一的上限规则 | §5.4、§7.9 |
| 34 | 收尾瞬间的竞态 | 必须在 leader 的轮次收尾完成之后才判断静止；Thread 层面的原子路由 | §5.1、§7.2 |
| 35 | 任务登记时机 | `team_delegate` 返回前必须已登记 | §7.5 |
| 36 | Remote 断线 | 两分钟无成功观察（含静默流）failed/unknown_state；初始请求不自动重放 | §7.11 |
| 37 | 唤醒风暴 | 2 秒合并窗口 | §7.3 |
| 38 | 一次性任务用内存存储时崩溃 | 由一次性任务的定义决定；需要恢复就用具名 Thread 加持久化存储 | §7.7 |
| 39 | `Post`/`WaitIdle`/`CloseIfIdle`/`Idle` 让人困惑 | 全部删除 | §10 |
| 40 | `Open`/`Session` 命名 | 删除 | §10 |
| 41 | 团队作为 Runner | Team.Send / Thread.Send / Thread.Standby；busy 不并入，TaskStore lease 排他 | §7.1 |
| 42 | `Close` 与 `Cancel` 让人困惑 | 只保留 `Cancel` | §4.2、§4.3 |
| 43 | `KeepOpen`/`UntilCanceled` 命名 | 改为 `Standby` | §10 |
| 44 | 停止按钮 | `sb.Cancel()` 中止当前处理，继续待命 | §4.3 |
| 45 | 常驻 Stream 无边界（Result、checkpoint、lease） | 待命由多次有边界的执行组成；`TurnEnd` 交付后释放 | §4.3、§5.6 |
| 46 | 常驻被迫带初始 prompt | `Standby` 不需要消息 | §4.3 |
| 47 | `Run`/`Stream` 合并 | 统一为 `Send` | §4.1 |
| 48 | ctx 的语义与易错点 | Go 惯例，待命用独立入口 | §4.9 |
| 49 | 执行级选项碰上运行中的执行 | `s.Send` 编译期拒绝（只接受 `MessageOption`）；`sb.Send` 排到当前执行之后 | §4.4 |
| 50 | 并入的消息共享 Stream | 任务模式不并入；待命不返回每条消息的 Stream | §4.10 |
| 51 | 只用同步获取时的审批 | 立即兜底并发出 Notice | §5.5 |
| 52 | 消息的写法 | `text` 加选项 | §4.4 |
| 53 | 迁移成本 | 迁移表 | §12 |
| 54 | `Agent.Send` 的语义 | 匿名 Thread | §3 |
| 55 | 问题 A（返回的 Stream 不处理这条消息） | 任务模式不并入、待命不返回 Stream、执行结束的统一定义 | §4.10、§5.1 |
| 56 | worker 审批 | Local O3 + Origin + 原 responder；Remote 无审批应答扩展，input-required 按远端策略 | §7.6 |
| 57 | 团队持久化、恢复与 Fork | interrupted、原 ID 重交；需选项消息 held 到重交；lazy Fork lineage；PerScope Remote 拒绝 | §7.7 |
| 58 | 团队用量 | ComposeResults 保留 leader 审计层，Usage 合计，Subagents 摘要，完整 member result/snapshot 在 Store | §7.8 |
| 59 | 包装模板何时使用 | 只在并入时 | §5.3 |
| 60 | 匿名 Thread 的 checkpoint | 核实项与降级行为 | §6.4 |
| 61 | 跨进程使用同一个 Thread | 租约规则，返回 `ErrThreadBusy` | §4.10 |
| 62 | `Done` 之后还要调 `Result()` 的冗余 | `Done` 携带权威结果 | §4.5 |
| 63 | 待命不应有同步获取 | `Standby` 上没有 `Result()` | §4.3 |
| 64 | `Done` 的体积 | 共享对象、非导出字段、R2、按轮释放、团队只放摘要 | §5.6 |
| 65 | 多个页面同时查看 | 由宿主的 hub 广播（属于宿主职责边界） | §1、§8 |
| 66 | Cursor 的 hooks.json 与用户仓库 | 只在受管理的工作目录里写，否则降级 | §6.3 |
| 67 | 看门狗提醒、预算收尾、外部事件、HITL 补充、完成前验证 | 统一用 `Send` 加 `From(system)`；验证用带记忆的任务跟进 | §8 |
| 68 | 嵌套团队 | Team/ConversationProvider，Surface.Prefix 可配；外层 worker grant 不提供 delegate/cancel | §7.1、§7.4 |
| 69 | bridges 映射 | A2A active-task supplement 与 v2 event projection 未实现；AG-UI Activity detail 字段有保真限制 | §8 |
| 70 | multica M1–M8 | 按实现核实，receipt 不等于 admission；provider 重放窗口、Remote timing 与 R5 限制明确 | §9 |
| 71 | leader 是全能的还是只派发 | 完整的 Agent，遵循委派优先：统筹归 leader，适合成员的工作必须派发，没有成员能承接的才亲自做；宿主可以收紧为纯派发器（只读 sandbox） | §0、§7.2 |
| 72 | leader 忙时 worker 通知 | Soon 按实际 Driver boundary；无通用工具调用延迟上界 | §7.3 |
| 73 | leader 与 worker 同时写同一个工作目录 | 任务由 leader 拆分和派发，冲突由 leader 在拆分时避免；SDK 不提供额外机制 | §7.2 |
| 74 | 边界投递失败 | 改为在本轮结束后续接投递 | §5.2-9 |
| 75 | 待领取队列满 | `Send` 阻塞等待空位 | §5.2-10 |
| 76 | 重复调用 `th.Standby` | 有就取、没有就建 | §4.3 |
| 77 | 待命中某次执行失败 | 待命继续，消息带入下一次执行，可重试的失败自动重试一次 | §4.3、§5.7 |
| 78 | resume 被拒或会话过期 | 连续性事件加上下文前缀注入 | §5.7 |
| 79 | 待命中切换模型 | 支持换模型续接则直接续接，否则同第 78 条 | §5.7 |
| 80 | checkpoint 持久化失败 | 进程内权威加后台重试 | §5.7 |
| 81 | 进程崩溃时未投递的消息 | Thread 级别去重，宿主用原 ID 重发 | §5.2-3、§5.7 |
| 82 | 团队 leader 失败 | RetryableError/RetryAfter 最多两次，worker 期间继续；耗尽或不可重试取消并返部分 Result | §7.9 |
| 83 | 成员达到 MaxActive | 排队；PerScope conversation 即使 MaxActive>1 仍单 writer | §7.5 |
| 84 | 开了待命却没有人读事件 | 未调用 `Events()` 时不缓冲 | §4.3 |
| 85 | 哪些情况保留为错误 | 忙碌、模式冲突、跨进程持有、执行结束后补充、打断不支持、消息过大、不支持续接、上限硬停、ctx 取消，各自有理由 | §5.7 |

---

## 12. 迁移（v1 → v2）

| v1 | v2 |
|---|---|
| module `github.com/agent-dance/agent-adaptor` | `.../agent-adaptor/v2`；v1 维护分支只修 bug |
| `agent.Run(ctx, p)` | `agent.Send(ctx, p).Result()` |
| `agent.Stream(ctx, p)` | `agent.Send(ctx, p)` |
| `th.Run` / `th.Stream` | `th.Send` |
| 没有运行中补充 | `s.Send` / `sb.Send` |
| `RunFinished` 加事后 `Result()` | `Done` 携带权威结果；`RunFinished` 保留，只供参考 |
| Thread 必须注入 store | 未注入时默认使用内存存储 |
| `SubagentUpdate.Agent` 加 `Data` | 强类型字段 |
| `a2adelegation.NewService` / `Local` / `Remote` | `team.New` / `Member{Runner}` / `Member{Remote}` |
| `delegate_to_agent`（同步） | `team_delegate` 加事件驱动的唤醒 |
| `Service.Result(runID, key)` | `Result.Subagents()` / `team_status` |
| 每次运行一个 sidecar | 每个 Team 一个 listener |
| `host-readiness-plan.md` P2-1 | 本提案 |

`AGENTS.md` 需要同步修订：

- §2：执行动词只有 `Send`；`Standby` 归入 Thread。
- §5：执行管线增加"同一次执行内续跑"阶段；`Send` 等于 `Events` 加 `Done`。
- §6：Thread 的模式与租约规则；默认内存存储。
- §7：Stream 增加 `Send`；新增 `Standby`；`Done`、`TurnEnd`、`MessageUpdate` 为不可丢弃的事件；消息投递合同。
- §8：`Result` 新增字段与方法。
- §9：Driver 的 `MessageCapability` 与 `MessagePort`。
- §11：`a2adelegation` 改为 `team`。
- §15：发布门禁增加消息投递的 race 测试与各 Driver 的 Message 一致性条款。

`root_api.golden` 与 `driver_api.golden` 重新冻结。根包 `With*` 数量不变。

---

## 13. 里程碑与验收

| 里程碑 | 内容 | 退出条件 |
|---|---|---|
| V2-M0 | `Send`/`Stream`/`Done`/`TurnEnd`/`MessageUpdate`、消息队列与状态机、SPI、ContinueResume、fake driver、`adaptertest` 消息条款 | §5.2 的 10 条合同全部有 race 测试，取消与投递竞态测试重复 1000 次稳定；`Result()` 与 `Done` 逐字段一致；`s.Send` 传入执行级选项无法编译（编译失败用例） |
| V2-M1 | Codex app-server（immediate、Interrupt、InRun）、Claude（tool、stop、InRun）、Cursor（relay 与 Resume 降级）、`Standby`、§5.4 上限、§5.5 审批兜底 | 每个 Driver 都有"运行中投递并出现在 transcript""轮次结束时续跑""版本过低时如实报告"的 fixture 与 live 测试；§6.4 中与这些 Driver 相关的核实项全部有结论；§5.7 的故障注入测试通过：边界投递失败改为续接、待命中执行失败后消息带入下一次执行、resume 被拒后注入上下文前缀、store 故障时进程内 checkpoint 生效 |
| V2-M2 | `hosttools/team`：事件驱动 leader、4 个工具、单 listener、worker 审批、Preamble 名册、强类型事件、showcase 改写 | 并行委派、纠偏、worker 提问唤醒 leader、worker 审批、待命中途用户发话五条路径全部通过；任意成员数下 leader 的 `tools/list` 恒为 4 |
| V2-M3 | TaskStore、`teamstoretest`、恢复与 Fork、issue-daemon 示例 | 模拟 multica 的 M1–M8 全部通过；kill 进程后消息无丢失、无重复（宿主用原 `MessageID` 重发），任务被标记为 `interrupted`；leader 某一轮失败后任务不受影响并成功重新唤醒 |
| V2-M4 | CodeBuddy、ACP（Kiro、Grok）；bridges（A2A、AG-UI、会话录制器） | §6.4 的全部核实项有结论，并按结论落地；A2A 对 working task 的消息转为 `s.Send` |
| V2-M5 | multica 影子接入 | 投递成功率与失败原因分布不低于 multica 原实现；`claude_supplement.go`、`cursor_supplement.go` 可以删除 |

每个里程碑合入前满足 v2 `AGENTS.md` 的全部发布门禁，并同步更新 godoc、专题文档、五种语言 README 与 CHANGELOG。
