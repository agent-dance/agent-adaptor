# Workstream: `pkg/hosttools/sessionrecorder` 参考实现

本文件是 [`docs/workstream-hitl-v2.md`](./workstream-hitl-v2.md) §4.3.1 "UI 会话历史恢复" 的**配套实现文档**。§4.3.1 规定了协议（`history → pending → subscribe`）并明确"历史恢复是宿主职责，SDK 不内置 replay"；本文件交付一份 opt-in 的**参考实现**，收敛实现层的反复踩坑，让宿主少写 200 行 JSONL + cursor 样板。

- 代码位置：[`pkg/hosttools/sessionrecorder/`](../pkg/hosttools/sessionrecorder/)
- 非目标（不改 core SDK）：不增动 `RunHandle`、不动 `StreamPayload` 形状、不新增 SDK 顶层依赖
- 触发条件：单进程宿主希望用"开箱即用"的持久化，而不愿自己搭一整套 Redis/Postgres 基础设施

## 0. TL;DR

- 新包 `pkg/hosttools/sessionrecorder`，暴露三样东西：`Recorder`（门面）、`Backend`（存取层抽象）、`Record`（持久化单位）
- 引入 `HostSeq`（宿主会话内强单调 cursor），区别于 `StreamPayload.Seq`（run 内强单调，跨 run 重置）
- 默认给两个 `Backend` 实现：`MemoryBackend`（测试/单 CLI）、`JSONLBackend`（单进程生产）；其他后端由宿主按 `Backend` 接口接入
- 示例 `examples/showcases/web-copilotkit-hitl` 顺带迁移过来，作为"用法正确的脚手架"

## 1. 要解决什么问题

§4.3.1 把"历史恢复"下放给宿主，给出的关键游标字段是 `StreamPayload.Seq`（§3.4.2）。但 §3.4.2 开宗明义：`Seq` 是 **run 内部单调**（"strictly monotonic within one run"）。一旦宿主把"会话边界"定义成比一次 run 更大的概念（最常见：浏览器稳定的 `thread_id`，跨多次 run），`Seq` 作为游标就失效了：

**失败模式**（真实 bug，已在生产 agent-registry 部署上复现）：

1. 用户发第一条消息 → run A 产出 `Seq=0..K`
2. 浏览器刷新 → 同一 `thread_id`，拉 `/session/events?after=K` 恢复历史
3. 用户发第二条消息 → run B 产出 `Seq=0..M`
4. 再刷新一次 → 调 `/session/events?after=lastSeenSeq`
   - 如果 `lastSeenSeq >= M`：run B 的事件漏了（它们 `Seq < lastSeenSeq`）
   - 如果 `lastSeenSeq < K`：run A 的尾部会被重放，顺序错乱

根因是**同一 `thread_id` 上的 `Seq` 不是全序的**——它只在单次 run 内部全序。§4.3.1 的示例协议默认"一次浏览器会话 = 一次 run"（`run_id` 作 session 键），生产场景里大多数宿主把 "一次浏览器会话 = N 次 run" 当常识，于是两者错位。

此外，朴素 host 实现里常见的两个孪生坑：

- **历史截断**：`threads[threadID].history = history[-500:]` 这种截断在 UI 上表现为"刷新后前面几轮对话都消失了"，且磁盘已经写到 JSONL 却没体现到内存；
- **`StreamPayload.ThreadID` 与 host `thread_id` 语义冲突**：前者是 adapter-native session id（比如 claude 的 session UUID），后者是宿主/浏览器侧的稳定键，盲目校验会把合法事件丢掉。

## 2. 设计目标

| 编号 | 目标 | 怎么保证 |
|---|---|---|
| G1 | 跨 run 恢复正确 | 引入 `HostSeq`，由 Recorder 在 session 维度集中分配；`Record` 就是 `(HostSeq, RecordedAt, Payload)` |
| G2 | core SDK 零入侵 | 只加 `pkg/hosttools/`；SDK 根包、bridges、adapters 都不 import 它；`StreamPayload` 结构不变 |
| G3 | 可替换持久化后端 | 抽出窄接口 `Backend { Load, Append, Sessions, Close }`；MemoryBackend / JSONLBackend 是两份参考实现 |
| G4 | 默认安全 | 默认 `KeyValidator` 卡住 path traversal；Append 失败自动回滚 HostSeq，避免 Load 出来观察到 gap |
| G5 | 增量恢复契约清晰 | `Since(ctx, key, afterHostSeq)` 语义是"strictly greater than"，0 表示全量；返回按 HostSeq 升序 |
| G6 | 可观测 | `Sessions()` 返回 `{Key, LastSeq, RecordedAt}`，宿主能直接拿来渲染"最近会话列表" |
| G7 | 并发安全 | 公共实现类型对 goroutine 安全；`recorder_test.go` 有并发 Record + HostSeq 连续性断言 |

## 3. 非目标

- **不跨进程分配 HostSeq**：多 pod 场景需要宿主做 sticky routing（sticky-by-sessionKey），或者把 Backend 换成支持 `INCR`/`SELECT … FOR UPDATE` 的后端；本期不内置。
- **不内置 SSE/HTTP handler**：bridge 层与 HTTP 路由是宿主代码 / `pkg/bridges/*` 的责任。
- **不内置 replay `StreamEvents()`**：SDK 的 `handle.StreamEvents()` 消费即走即散，重放从持久化层 `Recorder.Since` 读，这一点沿用 §4.3.1 原意。
- **不内置 pending decision 持久化**：pending 是 per-run runtime state，RunHandle 消失即失效；宿主按自己的 SLA 在 runtime map 里维持即可（示例 `threadStore` 演示了这部分）。

## 4. API 设计

### 4.1 值类型

```go
type HostSeq = uint64

type Record struct {
    HostSeq    HostSeq                    `json:"host_seq"`
    RecordedAt time.Time                  `json:"recorded_at"`
    Payload    agentadaptor.StreamPayload `json:"payload"`
}

type SessionInfo struct {
    Key        string    `json:"key"`
    LastSeq    HostSeq   `json:"last_seq"`
    RecordedAt time.Time `json:"recorded_at"`
}
```

`HostSeq` 故意用 `type HostSeq = uint64`（type alias 而非新类型），方便宿主在 JSON 响应、SQL 列类型、NextJS 客户端 TypeScript 层直接按 `number` 对接，避免"到处写转换"。

### 4.2 Recorder 接口

```go
type Recorder interface {
    Record(ctx context.Context, sessionKey string, p agentadaptor.StreamPayload) (Record, error)
    Since(ctx context.Context, sessionKey string, afterHostSeq HostSeq) ([]Record, error)
    Sessions(ctx context.Context) ([]SessionInfo, error)
    io.Closer
}

func New(backend Backend, opts ...Option) Recorder
```

契约：

- `Record`
  - 当且仅当 `backend.Append` 返回成功时才把 HostSeq 落到内存；失败则回滚，下一次 Record 仍拿同一个号，**避免 HostSeq 空洞**
  - 该方法对同一 `sessionKey` 的并发调用是序列化的，分配出的 HostSeq 严格递增、连续（1, 2, 3 …）
- `Since`
  - `afterHostSeq == 0` 表示全量；`>= lastHostSeq` 返回空切片
  - 保证按 HostSeq 升序；跨 run 的事件混合在一起，但顺序仍以 HostSeq 为准
  - 实现层从内存缓存读，启动后首次访问会 lazy 从 Backend 回灌
- `Sessions`
  - 按 `RecordedAt` 降序（最近活跃的在前）
  - 会把内存里尚未被 backend 枚举到的"新写入"overlay 进去，保证 `(LastSeq, RecordedAt)` 反映实时状态
- `Close`
  - 幂等；之后任何 `Record` 返回"closed"错误

### 4.3 Option

| Option | 作用 | 默认 |
|---|---|---|
| `WithClock(fn)` | 替换时间源 | `time.Now().UTC()` |
| `WithKeyValidator(v)` | 校验 sessionKey | `DefaultKeyValidator`（正则：`^[A-Za-z0-9][A-Za-z0-9_\-]{0,127}$`） |

KeyValidator 在 `Recorder` 层和 `JSONLBackend` 层**各挂一份**：Recorder 层保护业务 API，JSONLBackend 层保护文件系统（路径穿越）。两层用默认值一致；宿主若放宽策略（比如允许冒号做多租户前缀）需要在两个地方都 override。

### 4.4 Backend 接口

```go
type Backend interface {
    Load(ctx context.Context, sessionKey string) ([]Record, error)
    Append(ctx context.Context, sessionKey string, r Record) error
    Sessions(ctx context.Context) ([]SessionInfo, error)
    io.Closer
}
```

接口刻意做窄：

- 不要求 Backend 自己分配 HostSeq（Recorder 在上层统一分配，避免后端之间的语义漂移）
- 不要求 Backend 做查询过滤（`Since` 过滤在 Recorder 的内存层完成）
- 只要求"持久化正确 + 并发安全"两件事，门槛低到 Redis/Postgres 都能十几行接上

### 4.5 内置 Backend

**`NewMemoryBackend()`**

- 用途：单元测试、CLI demo、一次性脚本
- 不做持久化，进程退出即丢

**`NewJSONLBackend(dir, opts ...JSONLOption)`**

- 用途：单 pod / 单容器的生产部署（本文档的 canonical 假设场景）
- 存储布局：`<dir>/<sessionKey>.jsonl`，每行一条 `Record` 的 JSON
- 写入策略：`O_APPEND|O_CREATE|O_WRONLY`，单进程并发写同一 session 互斥（`sync.Mutex`）
- 启动加载：lazy，首次 `Load(key)` 时才扫 `<dir>/<key>.jsonl`
- 容错：`WithJSONLBadLineHandler`——默认吞掉坏行（便于 SIGKILL 造成的半写尾），宿主可换成 fail-hard
- 选项：`WithJSONLKeyValidator` / `WithJSONLFileMode` / `WithJSONLDirMode` / `WithJSONLBadLineHandler`

**`JSONLBackend` 不做 `fsync`**：`write(2)` 之后数据在 page cache，容器 crash 可能丢最近几百毫秒。宿主若要求强持久化，该换后端（Redis AOF / Postgres commit / S3 multipart），不是在这里加 fsync——因为 fsync 代价大且不同工作负载优化方向相反，参考实现不替宿主拍板。

### 4.6 线程模型 / 死锁分析

- Recorder 持两把锁：
  - `r.mu`：只保护 `sessions map[string]*sessionState` 的增长；锁期极短，不覆盖任何 I/O
  - `st.mu`（per-session）：覆盖 `Record` / `Since` / lazy Load 的临界区
- 任何路径上 **不存在同时持有两把锁**，不会发生 AB/BA 锁序逆转
- `Record` 在持 `st.mu` 时调 `backend.Append`，允许 I/O 阻塞同一 session 的后续 `Record` / `Since`。对 JSONL backend 来说这个 I/O 本来就是串行的；对 Redis/Postgres 后端宿主可以在 backend 内部引入连接池优化，与本层无关
- `Sessions` 先短暂持 `r.mu` 快照 sessions map，再按需 `st.mu.Lock()` 读 lastSeq/updatedAt——**按固定顺序获取每把锁，不会死锁**

### 4.7 失败与回滚语义

`Record` 核心流程：

```
st.lastSeq + 1 = next
err := backend.Append(next-marked record)
if err == nil { st.lastSeq = next; append to history }
else { return err; lastSeq NOT advanced }
```

这条路径保证了两条核心不变量：

- **不变式 A（无空洞）**：`{Records by HostSeq}` 永远是 `1, 2, …, lastSeq` 的连续序列
- **不变式 B（Append 幂等前提下的可重试）**：调用方看到错误后重试，拿到的 HostSeq 与失败那次相同；backend 如果在失败时已经写了一半（比如 write 返回错但实际写入成功），重试会导致同一 HostSeq 被写两次；Load 阶段 `sort.SliceStable(... HostSeq ...)` 会把它们放一起，但 Recorder 不做去重——**backend 层要保证 append 的 all-or-nothing**（JSONL 单行 `Write([]byte)` 在常规 ext4/xfs 上是原子的；再严格的场景需要宿主自己做双写/checksum 策略）

## 5. 与 §4.3.1 的关系

§4.3.1 规定的是**协议**（`history → pending → subscribe` 三步）和**持久化主键**（`(RunID, Seq)`），这两点对"一次浏览器 session 只有一次 run"的场景（比如 §4.3.1 示例里的 `history.Append(handle.RunID(), ev.Seq, ev)`）是完备的。

本设计补的是**跨 run 的 session 维度**：

| 维度 | §4.3.1 canonical | 本设计 |
|---|---|---|
| 会话边界 | `runID` | 宿主自定义的 `sessionKey`（通常 = 浏览器 `thread_id`） |
| cursor 字段 | `StreamPayload.Seq`（per-run） | `HostSeq`（per-sessionKey） |
| 主键 | `(RunID, Seq)` | `(SessionKey, HostSeq)` |
| 实现归属 | 宿主自实现 | opt-in 的 `pkg/hosttools/sessionrecorder` |

两者**互不矛盾**：

- 只要宿主的"会话"就是一次 run，用 §4.3.1 的原方案即可，不需要本包
- 如果宿主的"会话"跨越多次 run（copilotkit / chatgpt 式的多轮对话 UI），上 `SessionRecorder`；Payload 里的 `Seq` 字段仍然有用（per-run 审计、debug 按 run 切片），只是不作为跨 run cursor

建议 §4.3.1 在下次 revision 里加一段"如果你的会话边界跨 run，请看 `docs/workstream-session-recorder.md`"的交叉引用——本 PR 不触动该文档，改动空间留给作者在合适时机收口。

## 6. 典型宿主用法

### 6.1 单 pod，JSONL 持久化

```go
import "github.com/agent-dance/agent-adaptor/pkg/hosttools/sessionrecorder"

be, err := sessionrecorder.NewJSONLBackend("/data/app/sessions")
if err != nil { log.Fatal(err) }
rec := sessionrecorder.New(be)
defer rec.Close()

// 在 StreamEvents() 消费循环里写入
go func() {
    for ev := range handle.StreamEvents() {
        if _, err := rec.Record(ctx, sessionKey, ev); err != nil {
            log.Warn("record: %v", err)
        }
    }
}()

// HTTP handler：/session/events?thread_id=T&after=N
http.HandleFunc("/session/events", func(w http.ResponseWriter, r *http.Request) {
    key := r.URL.Query().Get("thread_id")
    after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
    records, err := rec.Since(r.Context(), key, after)
    if err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }
    json.NewEncoder(w).Encode(map[string]any{
        "thread_id": key,
        "after":     after,
        "events":    records,
        "last_seq":  lastHostSeq(records),
    })
})
```

`sessionKey` 的选择由宿主决定：

- 浏览器前端管理稳定 `thread_id`（CopilotKit / AG-UI 的默认做法）→ `sessionKey = thread_id`
- 宿主按用户 + 话题组合生成 stable hash → `sessionKey = hash(user_id, topic_id)`
- 一次 run 即一次会话（§4.3.1 原场景）→ `sessionKey = runID`

### 6.2 多 pod：sticky routing + 共享 backend

多 pod 场景下，**同一 sessionKey 的所有写入必须路由到同一进程**，否则 HostSeq 的内存分配会冲突。两种可行方案：

1. **Sticky-by-sessionKey ingress**（推荐）：ingress 按 `sessionKey` 哈希到 pod，每个 pod 用自己的 Recorder + 独立持久化目录/DB 分片
2. **共享 backend + 集中式 HostSeq**：自实现一个 `Backend`，用 Redis `INCR` 或 Postgres sequence 生成 HostSeq，然后**包装 Recorder** 或绕开 Recorder 直接用该 backend——这需要把 HostSeq 分配权下放到 backend，跟当前"Recorder 集中分配"的设计冲突，建议等有真实需求再立项重构（设计备忘：引入 `BackendWithSeqAlloc` 可选扩展接口）

本期不预先为方案 2 建框架——过早抽象大概率走形。

## 7. 前端接入建议

"持久化 + 跨 run 恢复"在后端就位之后，UI 层还要做几件事才能让"刷新浏览器也不丢 session"落到用户体验上。下面按层次给出建议：

- **L0** 是**架构 invariant**（见下节 L0），定义 AG-UI 输入到 adaptor 之间的消费边界，一旦违反会让 L1-L3 的任何前端优化（尤其 B 路径的带宽裁剪）**静默失灵**，新接 driver / bridge 的作者必须遵守
- **L1 / L2 / L3** 是**按改动成本 / 收益分层的前端工程建议**，`examples/showcases/web-copilotkit-hitl/web` 已落地 L1+L2，L3 作为 follow-up 标注

### L0 —— 前置合同：AG-UI `messages[]` 全量上行是协议冗余，**且必须保持如此**

#### 事实

1. **AG-UI 协议**规定客户端在每次 `POST /agent` 时以**完整 thread messages 数组**作为权威来源。CopilotKit HttpAgent、@ag-ui/client、AG-UI Dojo 都按此行为默认工作；这是 **client-authoritative** 的会话模型。
2. **agent-adaptor 的 AG-UI bridge** 在 [`pkg/bridges/agui/input.go`](../pkg/bridges/agui/input.go) 只消费 `RunAgentInput.LastUserText()`——即 messages 数组里**最后一条 role=user 的文本内容**。`messages[0..N-2]` 从未离开过 HTTP handler。
3. **会话连续性**（"第二轮 run 能看到第一轮说了什么"）来自**两条独立机制**，都与 `messages[]` 无关：
    - `WithSessionKey(namespace, threadID)` 让 SDK 把多次 run 绑到同一 session 记录（见 `options.go`）；
    - driver 自身的 resume 能力——Claude driver 在 `req.Session.State.ResumeID != ""` 时拼 `--resume <id>`（见 `claude/driver.go`），codex driver 用同样模式绑定 vendor session。driver `SessionCapability.SupportsResume = true` 是契约正式声明。

#### 后果

| 维度 | 现状 |
|---|---|
| **功能正确性** | 0 影响。裁剪到 `messages: [lastUser]` 仍完全工作 |
| **单轮 POST body** | 第 k 轮 body ≈ **O(k)**：50 轮 × 500 tokens/条 × 4 B/token ≈ **100 KB** 单轮；200 轮可达 **400 KB** |
| **整个会话总上行** | **O(N²)**（每轮都重发之前全部历史，前 N 轮累加 ≈ N(N+1)/2 × 平均消息字节） |
| **感知度** | 桌面 + wifi 无感；移动端 / 弱网 / 长会话 agent（planning / coding 跑几百轮）肉眼可见 |
| **可观测性陷阱** | 新接 bridge 的工程师常误以为 messages[] 是 driver 上下文来源，导致"删 SessionKey 看看会不会挂"一类的误诊断 |

#### 决策（本 workstream 明确选择）

- **保留 client-authoritative 协议语义**——adaptor 不试图改变 AG-UI 规范；bridge 继续接受全量 messages 数组，JSON 层 round-trip 所有已知字段（`state` / `tools` / `context` / `forwardedProps`）保证未来 [`docs/workstream-streaming-chat.md`](./workstream-streaming-chat.md) §18.2 passthrough 能无缝展开。
- **bridge 层不扩大 messages[] 消费面**——即使未来实现 §18.2 passthrough，也只允许把 **整个** messages 数组作为 opaque payload 交给特定 driver（在 driver 能力声明里 opt-in），**禁止**任何 bridge 代码出现"读 messages[i].content 做路由 / 判断 / 前后文推断"这类逻辑。违反此条会让 B 路径（下文）的带宽裁剪静默破坏功能。

#### 可选优化：B 路径（CopilotRuntime 路由层裁剪）

宿主若遇到实际带宽压力（一般发生在移动 / 弱网 / 每次对话 >100 轮的 agent 场景），最低成本的做法是在 **CopilotRuntime → Go 后端**的转发路由上裁剪 messages 到只保留最后一条 user，大致 20 行 TS：

```ts
export const POST = async (req: Request) => {
  const body = await req.json();
  const trimmed = { ...body, messages: lastUserOnly(body.messages ?? []) };
  return copilotRuntimeHandler({ ...req, json: async () => trimmed });
};

function lastUserOnly(messages: Message[]): Message[] {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === "user") return [messages[i]];
  }
  return [];
}
```

这条路径是 **opt-in** 的，`examples/showcases/web-copilotkit-hitl/web` **刻意不默认启用**：
- 作为"协议如何工作"的参考实现，保留全量传输让读者通过网络面板观察到 AG-UI 合规流量
- 避免给读者造成"必须这么裁"的误导——多数部署根本不需要
- 一旦 §18.2 passthrough 落地并开始消费 `state` / `tools` / `context`，裁剪会变成 breaking（删的不再只是冗余）

#### 何时**不要**走 B 路径

- 宿主 driver 未来启用 §18.2 passthrough（让 adapter 真实消费 `messages[]` / `state` / `tools` / `context`）
- 宿主在 bridge 之上自建 thread state reconstruction（不走 SessionKey + driver resume）
- 集成第三方 AG-UI 客户端做严格协议一致性校验的测试工具链

这三种场景下，裁剪会删掉 adapter 或校验器**真正依赖**的字段。

#### 不在本 workstream 做的决定

- **不**修改 bridge 去接受"增量 messages + bootstrap"这类分叉协议。真 stateless 化的路径（C 方案：driver 从 messages[] 重建上下文、放弃 `--resume`）属于 §18.2 passthrough workstream，与 session-recorder 正交。
- **不**在 example 里引入 B 路径开关。这份 example 的定位是"协议 + 会话恢复的参考实现"，引入性能 knob 会稀释它的演示价值；真实宿主自行取舍。

### L1 —— 响应契约对齐

后端 `/session/events` 现在返回 `events: Record[]`（每条是 `{host_seq, recorded_at, payload}`），前端必须按 `record.host_seq` 做 React key 和增量 cursor。字段名 `last_seq` 保持不变、语义从 per-run `StreamPayload.Seq` 切换到 `HostSeq`——这是一次**无形**破坏：老前端只要不拿 `last_seq` 当下一次 `after` 用就照跑，需要改的只是 TS 类型定义。

### L2 —— 增量拉取 + 事件驱动刷新（而不是定时轮询）

canonical 三步协议（§4.3.1: `history → pending → subscribe`）的第 3 步订阅在 example 里刻意**不用 SSE**，用浏览器原生生命周期事件替代：

```ts
useEffect(() => {
  const onVisibility = () => {
    if (document.visibilityState === "visible") refresh("incremental");
  };
  window.addEventListener("focus", () => refresh("incremental"));
  window.addEventListener("online", () => refresh("incremental"));
  document.addEventListener("visibilitychange", onVisibility);
  // backstop: 30s 间隔，只在前台跑
  const id = setInterval(
    () => { if (document.visibilityState === "visible") refresh("incremental"); },
    30_000,
  );
  return () => { /* unregister all */ };
}, [refresh]);
```

三条取舍：

1. **不开 EventSource 订阅 `/session/stream`**：这是一个 example，开第二条长连接（主聊天已经有 CopilotRuntime 的 SSE）性价比低；且会让 example 必须实现 `Last-Event-ID` / heartbeat 等生命周期管理。生产宿主如果业务离 chat 更远（比如长时间无用户操作的批处理任务仪表板），才值得加 SSE。
2. **浏览器事件覆盖 95% 场景**：`visibilitychange` + `focus` + `online` 刚好对应"切后台 → 切回"、"睡眠 → 唤醒"、"网断 → 恢复"三大恢复点。对正在盯着 panel 看的用户，30s backstop 兜底。
3. **增量模式用 `after=lastHostSeq`**：带宽从"每 3s × N 全量"下降到"Δ events × 恢复次数"，在上万 events 的会话里差两个数量级。

### L3 —— 聊天流本体的 replay（follow-up，本期不做）

上面两层让 **SessionPanel**（审计/协议可视化面板）能正确恢复，但**主聊天窗口 CopilotChat 刷新后还是会空**——CopilotKit 的内部 thread state 不是来自 `/session/events`，它只消费 `/agent` 的实时 SSE。

要让"刷新后聊天流丝滑恢复"，正确做法是把 history records 聚合回 CopilotKit 的 `initialMessages`：

```
for each SessionRecord in /session/events?after=0:
  switch record.payload.Kind:
    case "text.content":          // token-level delta
      → 聚合到同 message_id 的 assistant message 里
    case "message.start/end":     // 消息边界
      → flush 成一个完整 CopilotMessage
    case "tool_call.start":       // 工具调用开始
      → 建立 tool_call 对象，绑定 tool_call_id
    case "tool_call.args":        // 工具参数 delta
      → 累加到同 tool_call_id 的 args 字符串
    case "tool_call.result":      // 工具返回
      → 关联到同 tool_call_id
    case "hitl.requested/resolved":
      → 映射成 tool_call 的 dec.* 形态
```

聚合算法的挑战：

- `StreamPayload` 是 fine-grained，要重建成 `{role, content, tool_calls}` 的 CopilotMessage 需要一套完整的"延迟聚合 + flush on boundary"状态机
- CopilotKit 的 message schema 有自己约束（`id` 的形状、tool_call 的 `status` 字段），host 需要跟 CopilotKit 版本对齐
- 有 HITL 决策还没 resolve 时，要在初始消息里保留它为 pending 状态，保证用户能继续点卡片

落地这层要单独立一个 workstream（大概 200-400 行 TS + 一套覆盖所有 Kind 的测试）。本期先用 SessionPanel 旁路方案满足"能恢复"的正确性语义，L3 放到下一个迭代。

### 生产交互的收敛方向（建议，非硬要求）

对**生产应用**（不是 example）而言，上面的 SessionPanel 只是"底层协议可视化"，不是终端用户应该看到的。推荐的收敛形态：

- **主聊天 UI**：CopilotChat + `initialMessages`（L3）；pending 决策内嵌在消息流里；用户完全感知不到底层有 `host_seq`
- **SessionPanel**：作为 dev/admin 工具保留（类似 Chrome DevTools 的 Network panel），默认折叠，按需打开做 troubleshoot
- **ThreadID 迁移能力**：当用户换设备/换浏览器，提供"导入 thread_id"入口，避免历史被关联到新浏览器的新 id

这部分完全是宿主产品决策，本 workstream 只给出建议，不替宿主拍板。

## 8. 示例迁移

`examples/showcases/web-copilotkit-hitl` 已顺带迁移：

- `thread_store.go` 的 `history` 字段（朴素 `[]StreamPayload` + 500 cap）改为 `sessionrecorder.Recorder`
- `historyCap` 常量删除（SessionRecorder 不截断；如需截断请换 backend）
- 响应里 `last_seq` 的语义从 per-run `StreamPayload.Seq` 切换到 `HostSeq`；字段名不变，保证前端兼容
- `/session/events` 的 `after=` 参数现在按 `HostSeq` 解析——前端无需改动（当前前端永远发 `after=0`）
- 新增环境变量 `THREAD_STORE_DIR`：指向 JSONL 目录时启用持久化，不设时退回 Memory backend

`examples/showcases/web-copilotkit-hitl/thread_store_test.go` 的断言全部围绕新契约重写：跨 run HostSeq 连续、Since cursor 语义、resolve fallback。

## 9. 验收清单（DoD）

- [x] 新包 `pkg/hosttools/sessionrecorder` 编译通过；对外 API 只暴露 `Recorder` / `Backend` / `Record` / `SessionInfo` / `Option` / `JSONLOption` / `KeyValidator` + 构造函数
- [x] 单元测试覆盖：跨 run 单调、per-session 隔离、Since cursor 四类边界、并发 Record 不丢号、backend 失败 HostSeq 回滚、JSONL 重启恢复、JSONL 坏行默认 skip、bad key 拒绝、Clock/Sessions 排序
- [x] `go build ./...` 全绿
- [x] `go test ./pkg/hosttools/... ./examples/showcases/web-copilotkit-hitl/...` 全绿
- [x] `examples/showcases/web-copilotkit-hitl` 迁移到新包；响应契约兼容；README `生产化 checklist` 里把"`(run_id, seq)` 作主键"更正为"`(session_key, host_seq)` 作主键"
- [x] 设计文档落地（本文件）

## 10. 明确保留的开放问题

1. **多 pod HostSeq 分配**：见 §6.2，留给后续 workstream
2. **`BackendWithSeqAlloc` 扩展接口**：见 §6.2，暂不落地
3. **fsync 策略**：见 §4.5 讨论；JSONL backend 不内置
4. **retention / TTL**：本包不内置清理逻辑；宿主按业务 SLA 自己定期 prune 目录或维护 db 表
5. **与未来 "vendor session replay"（§8.3 of hitl-v2）的关系**：vendor replay 走 adapter 自家 session 源，本包走 host-recorded 持久化；两条路径正交，同一宿主可以同时用——一份给 UI 恢复，一份给审计/归档
