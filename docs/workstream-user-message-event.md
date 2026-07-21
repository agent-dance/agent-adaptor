# Workstream: 把 user-side message 纳入 StreamPayload ontology

| 字段 | 值 |
|---|---|
| 触发 issue | `docs/user_message_event_issue.md`（flowx-agent 团队提出） |
| 目标版本 | v0.9 |
| 兼容性 | 完全向后兼容（additive） |
| 涉及包 | `run_types.go`、`pkg/bridges/agui` |
| 不涉及包 | `pkg/hosttools/sessionrecorder`、所有 adapter（codex / claude / cursor） |

---

## 0. TL;DR

- 在 `StreamPayload` 上**加一个 `Role` 字段**，零值视为 `RoleAssistant`，
  既有 adapter / bridge 不需要任何修改。
- `agui.Translator` 把 `text.start/content/end` 的 `Role` 透传成 AG-UI
  `TextMessageStartEvent` 的 role 参数（沿用 AG-UI 官方 `WithRole(...)`
  option）。
- 在 `pkg/bridges/agui` 加一个**纯构造 helper** `RunAgentInput.UserTurnPayloads(runID)`，
  把"最后一条 user message"转换成一组 `text.start / text.content / text.end`
  `StreamPayload`，**由宿主自己塞给 Recorder / SSE fan-out**。
- **不**新增 `StreamUserMessage` Kind；**不**在 SDK 里加 `Bridge.Handle` 或
  任何持有 `Recorder` 的入口（违反 `AGENTS.md` §1 / §8 / §9）。

宿主侧使用方式：在既有的 fan-out 循环上游加 3 行——见 §6。

---

## 1. 目标 & 非目标

### 1.1 目标

1. 让 `StreamPayload` 在 ontology 上能表达 user-role 文本消息，且形状与
   assistant 消息**完全同构**（同一组 `text.*` 三段，仅 Role 不同）。
2. 让基于 recorder 的回放通道（`sessionrecorder.Recorder.Since`）和实时
   `/chat` 通道（`agui.Wrap`）**翻译出的 AG-UI 事件流形状一致**，前端不
   再需要分支解码。
3. 让宿主消灭私有 `host.user.message` Kind 的 workaround，迁移成本 ≤ 3
   行代码。

### 1.2 非目标（明确不做）

- 不引入 `StreamUserMessage` 这种 synthesized-only Kind（理由：与
  `StreamPayload` "adapter 一侧产出"的文档语义打架，会留长期遗产）。
- 不在 `pkg/bridges/agui` 增加 `Bridge.Handle(w, r, Options{Recorder,...})`
  这种 HTTP handler 形态（违反 `AGENTS.md` §1 "core SDK 不内置 HTTP/gRPC
  server"、§8 默认禁止清单、§9 "不允许把宿主服务能力直接塞回 core SDK"）。
- 不让 `pkg/bridges/agui` import `pkg/hosttools/sessionrecorder`
  （`sessionrecorder/doc.go` 明文写："fan-out is the host's job"）。
- 不开放 `RoleSystem` / `RoleTool` 常量。等真正出现一个 adapter 需要
  emit system/tool role 文本时再加（`AGENTS.md` §2 Simplicity First）。
- 不改任何 adapter（codex / claude / cursor）—— 它们没有 user-side
  emit 场景，Role 字段零值即可。
- 不做反向翻译（AG-UI Event → StreamPayload）。

---

## 1.5 现状说明：prompt 在 SDK 内部的实际去向

为了避免后来人重复追代码，这里把 v0.8 / v0.9 里 "user prompt 到底有没
有被 SDK 持久化" 这件事固化清楚。

### 1.5.1 SessionStore：永远不记录 prompt

`sdk.Start(ctx, prompt, opts...)` 接到的 prompt 字符串只活到 adapter
入参 `DriverRunRequest.Prompt`，之后**完全不进 SessionStore 的写路径**。

SessionStore 在 `persistSession`（`session.go`）里写入的是
`SessionRecord{ID, Namespace, Key, Fingerprint, DriverState, ...}`。
`DriverState` 进一步只持有 `{ResumeID, DisplayID, Data}`——`Data` 是
adapter 用来做 resume 校验的指纹/参数，**没有任何字段承载 prompt 或
message 内容**。

`session_types.go` 自己已经在 doc 里写死这条边界：

> SessionStore is NOT the right place for:
>   - host-facing chat / thread / conversation history payloads
>     → use pkg/hosttools/sessionrecorder with sessionKey = ThreadID

所以"第一次 run 的 prompt 会不会进 SessionStore"的答案：**不会，从来
不会，无论第几次 run。**

### 1.5.2 Driver provider session：存了，但对 SDK 是黑盒

prompt 真正"持久化"的位置是 provider CLI 自己的 conversation 文件——
例如 claude 把 prompt 当 stdin 喂给 `claude-cli`，CLI 把它写进
`~/.claude/...` 下的 conversation jsonl，下次 `--resume <ResumeID>`
时一并加载。codex appserver、cursor 同理。

但：

- SDK **不读**这份内容；
- 每个 provider 的格式都不同；
- 没有公共 API 让宿主取一个"标准化的 messages 视图"出来。

这就是 issue §2.1 第二条 bullet 描述的痛点：driver session 确实持有
transcript，但**跨 driver 不能用统一代码消费**。

### 1.5.3 sessionrecorder：等宿主主动写

`pkg/hosttools/sessionrecorder` 是 SDK 当前唯一认可的 "transcript view
真值源"。它的工作模型是：宿主在 fan-out 循环里把每条 `StreamPayload`
塞一份给 recorder。

但 adapter 按合同**不 emit user-side text 事件**——它的输出永远是
assistant / tool / reasoning。所以即便宿主已经接好了 recorder：

```
run 开始之前 → recorder[sessionKey]  = []
run 进行中   → recorder[sessionKey] += [run.started, text.*(assistant),
                                        tool_call.*, ..., run.finished]
run 结束之后 → user prompt **不在** recorder 里
```

浏览器刷新拉 `recorder.Since(sessionKey, 0)` 拿到的就是 assistant-only
的流——前端历史里 user 气泡丢失。这就是 v0.9 `UserTurnPayloads` 要
解决的核心矛盾。

### 1.5.4 一图概览

```
sdk.Start(ctx, "你好", opts...)
       │
       ▼
 DriverRunRequest.Prompt = "你好"  ← SDK 唯一持有 prompt 的位置（栈上）
       │
       ▼
 adapter.Run(req, sink)
       │
       ├──► CLI stdin / arg ──► provider conversation 文件 (黑盒，跨 driver 不一致)
       │
       └──► sink.EmitStream(...)  ← emit 的都是 assistant/tool/reasoning，
                                    不包含 user prompt
                    │
                    ▼
            RunHandle.StreamEvents()
                    │
       宿主 fan-out ┴──► sessionrecorder (assistant-only)
                       └──► SSE writer (assistant-only)

 SessionStore.Finalize:
    Record{ResumeID, fingerprint, DriverType, ...}
    不写 prompt 不写 messages 不写任何文本
```

### 1.5.5 推论

- 想让 prompt 进入"宿主自己可消费的 transcript 真值源"，**只有一条
  路径**：宿主在 fan-out 上游主动 `recorder.Record(...)` 一条
  user-side 事件。
- v0.9 之前：宿主必须发明私有 Kind（`host.user.message` 等），代价是
  ontology 分裂、AG-UI Translator 不识别。
- v0.9 之后：宿主用 `RunAgentInput.UserTurnPayloads`（AG-UI 宿主，
  §6.1）或自己手搓一个 5 行函数构造 `text.*{Role:RoleUser}` 三段
  （任意宿主，§6.4）。两条路径产出的 payload 完全同形，下游消费方
  零分支。

---

## 2. 设计

### 2.1 SDK ontology 改动（`run_types.go`）

新增 `Role` 类型和常量，并把字段加到 `StreamPayload`：

```go
// Role identifies the speaker of a text-bearing StreamPayload.
//
// The zero value is RoleAssistant: every existing adapter emits
// text.start / text.content / text.end as assistant output, so leaving
// Role unset preserves the v0.8 wire shape exactly.
//
// Role only carries semantics on text-lifecycle kinds
// (text.start / text.content / text.end). On every other Kind it MUST
// be left zero; bridges treat non-zero Role on non-text kinds as a
// programming error and may ignore it.
type Role string

const (
	// RoleAssistant is the default; emitted by every adapter today.
	RoleAssistant Role = ""
	// RoleUser marks a text lifecycle synthesized above the driver
	// layer to represent the human turn that triggered the run.
	// Adapters MUST NOT emit RoleUser themselves — it is exclusively
	// produced by bridges or hosts that want the user turn to appear
	// in the recorded / replayed StreamPayload stream.
	RoleUser Role = "user"
)

type StreamPayload struct {
	Kind StreamKind
	// ... existing fields unchanged ...

	// Role identifies the speaker for text.* kinds. Zero value =
	// RoleAssistant for backward compatibility; see Role docs.
	Role Role
}
```

`StreamPayload` 的 field-usage 文档块同步追加：

```
//   - text.start / text.content / text.end: Role optional; zero value
//     is treated as RoleAssistant. RoleUser MUST be paired with a
//     non-empty MessageID and the same MessageID across the three
//     events of a single user turn.
```

**为什么选 `Role string` 而不是 `Role` 嵌套 enum 表**：`StreamPayload` 全
仓库已经是 flat 结构（Name / Delta / Args / Result 并列），保持一致。

### 2.2 AG-UI Translator 改动（`pkg/bridges/agui/bridge.go`）

AG-UI 官方 SDK 已经支持
`events.NewTextMessageStartEvent(id, events.WithRole("user"))`
（验证过 `github.com/ag-ui-protocol/ag-ui/sdks/community/go` 当前版本的
`pkg/core/events/message_events.go`）。

`translateNonTerminalLocked` 里 `text.start` / `text.content` 两处构造
`NewTextMessageStartEvent` 的调用改为带 role：

```go
case agentadaptor.StreamTextStart:
	if p.MessageID == "" {
		return nil
	}
	if t.activeText[p.MessageID] {
		return nil
	}
	t.activeText[p.MessageID] = true
	return []aguievents.Event{aguievents.NewTextMessageStartEvent(p.MessageID, textRoleOpt(p.Role))}

case agentadaptor.StreamTextContent:
	if p.MessageID == "" || p.Delta == "" {
		return nil
	}
	out := []aguievents.Event{}
	if !t.activeText[p.MessageID] {
		t.activeText[p.MessageID] = true
		out = append(out, aguievents.NewTextMessageStartEvent(p.MessageID, textRoleOpt(p.Role)))
	}
	out = append(out, aguievents.NewTextMessageContentEvent(p.MessageID, p.Delta))
	return out
```

helper：

```go
// textRoleOpt returns the AG-UI WithRole option for a StreamPayload
// Role. Zero value maps to AG-UI's "assistant" default; non-zero values
// pass through verbatim.
func textRoleOpt(r agentadaptor.Role) aguievents.TextMessageStartOption {
	switch r {
	case agentadaptor.RoleUser:
		return aguievents.WithRole("user")
	default:
		return aguievents.WithRole("assistant")
	}
}
```

显式总是带 role 而非"仅 user 时带"是有意为之：让 wire 上 assistant /
user 两种事件的 schema 完全对称，AG-UI 验证器和前端解码器走同一分支。

**Translator 内部状态**：`activeText[MessageID]` 现在不区分 role。这
是 OK 的——AG-UI 协议本身就要求 (MessageID, role) 在一条 start/end 内
保持稳定，不会有"同 MessageID 跨 role"的合法场景。Role 不需要进 map。

### 2.3 宿主侧 helper（`pkg/bridges/agui/input.go`）

新增**纯函数**，只构造 `StreamPayload`，不写任何 store、不发任何 HTTP：

```go
// UserTurnPayloads converts the latest user-role message in this AG-UI
// input into a well-formed text.start / text.content / text.end triple
// of StreamPayloads. The triple uses RoleUser so downstream Translator
// and Recorder treat it symmetrically to assistant text.
//
// The caller owns where these payloads go:
//
//   - to a sessionrecorder.Recorder so the user turn appears in
//     recorder-backed transcript views;
//   - to an SSE writer (via agui.NewTranslator().Translate(p)) so
//     other tabs subscribed to the same thread observe the turn in
//     real time;
//   - or simply discarded if the host does not need cross-client
//     replay.
//
// runID is opaque to the SDK; passing the AG-UI RunAgentInput.RunID is
// the canonical choice for correlation.
//
// Returns nil when the input has no user-role message with non-empty
// text content. The MessageID reuses the AG-UI Message.ID when
// present; when absent the helper synthesises a stable
// "user-<runID>-<idx>" identifier so the start/content/end triple
// remains internally consistent.
//
// UserTurnPayloads never panics on a nil receiver.
func (in *RunAgentInput) UserTurnPayloads(runID string) []agentadaptor.StreamPayload {
	if in == nil {
		return nil
	}
	msgID, text := lastUserMessageWithID(in)
	if text == "" {
		return nil
	}
	if msgID == "" {
		msgID = synthesizeUserMessageID(runID, in)
	}
	now := time.Now()
	common := agentadaptor.StreamPayload{
		Role:      agentadaptor.RoleUser,
		MessageID: msgID,
		RunID:     runID,
		ThreadID:  in.ThreadID,
		Timestamp: now,
	}
	start := common
	start.Kind = agentadaptor.StreamTextStart
	content := common
	content.Kind = agentadaptor.StreamTextContent
	content.Delta = text
	end := common
	end.Kind = agentadaptor.StreamTextEnd
	return []agentadaptor.StreamPayload{start, content, end}
}
```

`lastUserMessageWithID` 是 `LastUserText` 的 ID-aware 变体；保持
`LastUserText` 不动以免触动现有调用。

**边界**：

- 一次性产 3 条 payload 而非 1 条 "user.message"，是为了让 recorder /
  Translator / 前端的处理分支与 assistant text **完全相同**——前端不需
  要"如果是用户消息就只渲一次"的特殊代码。
- `Role` 用在 `StreamTextStart/Content/End`；该 helper 是当前唯一的内置
  RoleUser 来源。Adapter 仍然只 emit Role 零值（assistant），合同清晰。

### 2.4 不改的地方

- 不改 `Wrap` / `WrapWithContext`：它们消费 `RunHandle.StreamEvents()`，
  user 事件不走 driver stream（也不应该走，这会破坏 §7.2 helper 不解
  析协议的边界）。User 事件由宿主在 fan-out 时自己 Translate。
- 不改 `CloseRun`、HITL 翻译路径、`Translator.translateNonTerminalLocked`
  里 text 之外的任何分支。
- 不改 `sessionrecorder`：它早就接受任意 `StreamPayload`，对新增 Role
  字段无感。
- 不改任何 adapter。codex/claude/cursor 不知道 Role 存在。

---

## 3. 兼容性

### 3.1 二进制 / 源码兼容

- `StreamPayload` 加字段是结构体扩展。Go 里只要消费方用具名字段
  literal 或 `:=` 解包，不会破坏。仓库内 grep 一遍 `agentadaptor.StreamPayload{`
  确认所有构造点都是具名字段；外部宿主同理（AG-UI bridge 是 SDK 自带）。
- `Role` 零值 = `RoleAssistant`，等价于现行行为。
- AG-UI 翻译现在总是显式带 role；AG-UI 服务端验证 schema 兼容
  `role` 字段，新增不破坏。

### 3.2 旧宿主与新 SDK 共存

旧宿主仍然写自己的 `host.user.message` 私有 Kind？没问题——它走
Translator 的 default 分支（`case ""` 之外的未知 Kind 返回 nil），实时
通道照旧静默丢弃；recorder 里两种事件并存，前端按 v0.8 老逻辑处理。新
宿主切到 `UserTurnPayloads` 后，自己负责删私有 Kind。

### 3.3 新宿主与旧 SDK

不支持。需要 ≥ v0.9。

---

## 4. 测试矩阵

### 4.1 单元测试

| 文件 | 用例 |
|---|---|
| `pkg/bridges/agui/bridge_test.go` | `text.* with Role=RoleUser` 翻译出 AG-UI `TextMessageStart{role:"user"}/Content/End`，且与 RoleAssistant 路径形状一致 |
| `pkg/bridges/agui/bridge_test.go` | `text.* with Role=RoleAssistant`（零值）依旧产出现有 wire（role 现在显式 = "assistant"），现存断言更新一处 |
| `pkg/bridges/agui/input_test.go` | `UserTurnPayloads` 在有/无 Message.ID、有/无 user 消息、空 content 三种情况下的返回 |
| `pkg/bridges/agui/input_test.go` | `UserTurnPayloads` 产出的三条 payload MessageID 一致、Role 都是 RoleUser、Kind 严格按 start/content/end 顺序 |
| `run_types_test.go`（若不存在则在合适处加） | `Role` 零值在 JSON marshal 中省略，避免污染既有日志 |

### 4.2 回归

- `go test ./...` 全绿。
- 重点关注 `pkg/bridges/agui/bridge_test.go` 现存的 RUN_STARTED → TEXT_MESSAGE_START → … 顺序断言：因为 Translator 现在总是显式带 role="assistant"，断言里如果断的是结构体 deep-equal 需要一并加上 role。

### 4.3 端到端（手动 / 在 flowx-agent 侧）

- 宿主切到 helper 后浏览器刷新，`/session/events` 回放出来的 AG-UI 事
  件流里既能看到 `role:"user"` 的 TEXT_MESSAGE，也能看到 `role:"assistant"`
  的 TEXT_MESSAGE，前端无需任何分支代码。
- A tab 发消息，B tab 订阅同 thread，B tab 能在 user 气泡上拿到
  `role:"user"` 的 TEXT_MESSAGE_* 三段。

---

## 5. 实施步骤

按下列顺序提交，每一步独立可 review：

1. **Step 1 — 加 Role 字段（最小核心改动）**
   - 修改：`run_types.go`（加 `Role` 类型 + 常量 + 字段 + 文档）。
   - 测试：补一个最小化的 JSON marshal/zero-value 测试。
   - 验证：`go vet ./...`、`go build ./...`、`go test ./...` 通过。

2. **Step 2 — Translator 透传 Role**
   - 修改：`pkg/bridges/agui/bridge.go`（`textRoleOpt` + 两处调用点）。
   - 测试：`bridge_test.go` 新增 RoleUser 翻译用例 + 调整既有断言。
   - 验证：`go test ./pkg/bridges/agui/...` 通过。

3. **Step 3 — `UserTurnPayloads` helper**
   - 修改：`pkg/bridges/agui/input.go`（helper + `lastUserMessageWithID`）。
   - 测试：`input_test.go` 覆盖四类输入。
   - 验证：`go test ./pkg/bridges/agui/...` 通过。

4. **Step 4 — 文档**
   - 修改：`docs/workstream-streaming-chat.md` 的 StreamKind 表追加
     "text.* 默认 assistant，可由发起方设 Role=RoleUser"一行。
   - 修改：`docs/streaming-adapter-contract.md` 加一句"adapters MUST
     NOT set Role to RoleUser"。
   - 修改：`AGENTS.md` §3.4 输出合同段无需改（Transcript / Output /
     Summary 都不受影响），但在 §10 streaming 段提一句 "Role 字段是
     可选的方向维度，零值兼容 v0.8"。

5. **Step 5 — example 同步**
   - 修改：`examples/showcases/web-copilotkit-hitl/server.go` 在 driver
     fan-out 之前写入 user turn（见 §6）；CopilotKit 已乐观渲染 user
     message，因此该 example 不再向同一 SSE 连接重复回显。

每步均不破坏旧调用方。Step 1 / 2 / 3 / 4 / 5 可拆 5 个 PR，也可合并为
1 个；推荐分两个：(1+2+4 一组：ontology + translator + 文档)，(3+5
另一组：helper + example)。

---

## 6. 宿主使用方式（v0.9）

### 6.1 标准 fan-out 形态

宿主原先就持有一个 `RunHandle` → `Recorder` + SSE writer 的 fan-out
循环（见 `examples/showcases/web-copilotkit-hitl`）。变化只是在**起 run 之
前**多走一段 user-turn 的构造 + 同样的 fan-out：

```go
// 1. 解码 AG-UI 请求
input, err := agui.DecodeHTTPRequest(r)
if err != nil { ... }

ns, key := input.SessionKey()           // ("agui", threadID)
sessionKey := ns + "/" + key            // host-owned aggregation key
runID := input.RunID                    // 让 recorder 里有相关性

// 2. 起 run（语义不变）
handle, err := sdk.Start(
    r.Context(), input.LastUserText(),
    agentadaptor.WithStreaming(),
    agentadaptor.WithSessionKey(ns, key),
)
if err != nil { ... }

// 3. 先把 user turn 落到 recorder（顺序保证 HostSeq 在 driver 输出之前）
translator := agui.NewTranslator()
sseW := mySSEWriter(w)

for _, p := range input.UserTurnPayloads(runID) {
    // 写 recorder
    if _, err := recorder.Record(r.Context(), sessionKey, p); err != nil {
        log.Warn("record user turn", err) // 不阻断业务
    }
    // 实时 echo 给当前连接的 SSE，让前端 UI 立即看到 user 气泡
    for _, ev := range translator.Translate(p) {
        sseW.Write(ev)
    }
}

// 4. 既有 fan-out：StreamPayload → Recorder + Translator → SSE
for p := range handle.StreamEvents() {
    _, _ = recorder.Record(r.Context(), sessionKey, p)
    for _, ev := range translator.Translate(p) {
        sseW.Write(ev)
    }
}

// 5. 关 run（既有写法不变）
waitRes, waitErr := handle.Wait(r.Context())
for _, ev := range translator.CloseRun(waitErr) {
    sseW.Write(ev)
}
_ = waitRes
```

**关键点**：

- "先 Record user，再 Start"——保证 `HostSeq(user) < HostSeq(driver
  first event)`，刷新回放时顺序天然正确。
- 原始 AG-UI client 若不做本地乐观渲染，可以把 user 三段 echo 给当前
  SSE 连接，让"刷新还原"和"实时通道"产物一致。
- **CopilotKit 例外：** `CopilotChat` 会先乐观渲染本次提交的 user
  message。同一连接若再 translate 合成的 `RoleUser` 三段，会把一个气泡
  追加两次。`examples/showcases/web-copilotkit-hitl` 因此仍在 driver 输出前
  把 user turn 写入 recorder，但不向当前 CopilotKit 连接回显。
- 失败语义和现有 fan-out 一致：Recorder 写失败 log warn 不阻断；
  Translator 失败不会发生（纯构造）。

### 6.2 回放端点（`/session/events`）

回放路径**不需要任何改动**，因为 user 三段早就以 `text.*` 形态进了
recorder：

```go
records, _ := recorder.Since(ctx, sessionKey, afterHostSeq)
replayTranslator := agui.NewTranslator()
for _, rec := range records {
    for _, ev := range replayTranslator.Translate(rec.Payload) {
        sseW.Write(ev)
    }
}
```

输出的 AG-UI 事件流和实时通道完全同构——前端用同一份解码器。

### 6.3 迁移 checklist（针对 flowx-agent）

1. 删除 `cmd/flowx-agent/agent_server.go` 中 `StreamHostUserMessage` 常量
   及所有引用。
2. 把 `handleAgent` 里手写的 `AppendHistory` 段替换为 §6.1 第 3 步形式。
3. 把"回放端点直接吐 `StreamPayload` JSON"改成"过 `agui.Translator`
   出 AG-UI 事件流"——这样实时通道与回放通道协议统一，前端 `host.user.message`
   分支也可以删掉。
4. CI 验证：浏览器刷新 / 多 tab / fork session 三个场景下，前端不再
   命中私有 Kind 分支即视为迁移完成。

### 6.4 非 AG-UI 宿主的推荐做法

不走 AG-UI 协议（直接调 `sdk.Start(...)`，自己定义 HTTP/RPC 入口）的
宿主，没有 `RunAgentInput`，自然也用不上 `UserTurnPayloads`。这种场景
**不需要 SDK 提供新 helper**——五行宿主代码即可：

```go
// host 自己定义；不属于 SDK 公共 API
func userTurnPayloads(threadID, runID, messageID, prompt string) []agentadaptor.StreamPayload {
    if prompt == "" {
        return nil
    }
    if messageID == "" {
        messageID = "user-" + runID
    }
    now := time.Now()
    base := agentadaptor.StreamPayload{
        Role:      agentadaptor.RoleUser,
        MessageID: messageID,
        RunID:     runID,
        ThreadID:  threadID,
        Timestamp: now,
    }
    start := base; start.Kind = agentadaptor.StreamTextStart
    content := base; content.Kind = agentadaptor.StreamTextContent; content.Delta = prompt
    end := base; end.Kind = agentadaptor.StreamTextEnd
    return []agentadaptor.StreamPayload{start, content, end}
}
```

调用形态等同 §6.1，**两个 for 循环都在同一个 goroutine 里同步执行**
（adapter 在 `sdk.Start` 内部已被 fork 到独立 goroutine，stream 通道
负责跨 goroutine 投递；recorder 写入的 HostSeq 顺序由调用顺序决定）：

```go
handle, err := sdk.Start(ctx, prompt,
    agentadaptor.WithStreaming(),
    agentadaptor.WithSessionKey("my-host", threadID),
)
if err != nil { ... }
defer handle.Cancel(ctx) // ctx 取消 / handler 提前返回时清理 run

// 1) user turn 落 recorder（同步、瞬时；3 条 payload）
for _, p := range userTurnPayloads(threadID, handle.RunID(), msgID, prompt) {
    if _, err := recorder.Record(ctx, sessionKey, p); err != nil {
        log.Warn("record user turn", err) // 不阻断业务
    }
    // 如果宿主用 AG-UI Translator 出 SSE，这里 echo；
    // 否则按宿主自己的协议把 p 序列化给前端。
}

// 2) 既有 fan-out：阻塞读 stream，直到 adapter Run 返回、sink 关闭
//    通道关闭语义保证：loop 退出时所有 driver 侧 payload 都已被消费
for p := range handle.StreamEvents() {
    if _, err := recorder.Record(ctx, sessionKey, p); err != nil {
        log.Warn("record stream payload", err)
    }
    // ... 同上序列化给前端 ...
}

// 3) 拿最终结果并写出 terminal 事件
//    handle.Wait 在 loop 2 退出后是 non-blocking 的：sink 关闭意味着 Run 已返回
waitRes, waitErr := handle.Wait(ctx)
_ = waitRes
// 如果用 AG-UI Translator：把 RUN_FINISHED / RUN_ERROR 写给前端
// for _, ev := range translator.CloseRun(waitErr) { sseW.Write(ev) }
// 自定义协议：根据 waitErr 写自己的终止帧。
```

> **并发模型小结**：`sdk.Start` 把 adapter 跑在内部 goroutine，外部
> 看到的只有一个 `RunHandle`。宿主侧 fan-out 是单 goroutine 阻塞循
> 环——通过 `StreamEvents()` 通道的 buffer + 关闭语义获得"零事件遗
> 漏 + 顺序与 adapter emit 一致"的天然保证。除非你要在同一个 run 期
> 间并发处理 `DecisionRequests()`（HITL 异步模式），否则**不需要**
> 自己起 goroutine。HITL 异步场景参考 `agui_run_session.go::watchDecisionRequests`。

如果**确实**需要一边消费 stream 一边等 `Wait`（例如想让 ctx 取消能立
即打断 stream loop），canonical 写法见
`examples/showcases/web-copilotkit-hitl/agui_run_session.go`：把 `Wait`
放在独立 goroutine，stream loop 用 `select { case <-stream: ...; case
<-ctx.Done(): handle.Cancel(); ... }` 处理取消。本节示例不展示该形态
是因为 `for p := range ch` 已经覆盖 99% 用例。

**推荐遵守的几条不变量**（与 AG-UI 路径同源）：

| 不变量 | 原因 |
|---|---|
| `Role = RoleUser` 而不是发明私有 Kind | 复用 SDK ontology，下游一律按 `text.*` 处理 |
| 三段共享同一个 `MessageID` | recorder 回放 / 任何 Translator 都依赖 message lifecycle 完整闭合 |
| 在 `sdk.Start(...)` **之后**、`handle.StreamEvents()` 消费**之前** Record | 顺序保证 `HostSeq(user) < HostSeq(driver first event)` |
| `MessageID` 缺省时用 `runID` 当后缀 | 同一次 run 内确定性可复现，方便 dedup |
| Recorder 写失败 **log warn 不 abort run** | 与 driver-side payload 的失败语义一致 |

**为什么不在 SDK 里加 `agentadaptor.NewUserTurnPayloads(...)`**：

- 这 20 行构造逻辑没有可复用的内部状态；
- 一旦加进 core 公共 API，后面就会被催着加 `NewSystemTurnPayloads` /
  `NewToolMessagePayloads`，违反 `AGENTS.md` §2 "Simplicity First"；
- 真正高频的入口是 AG-UI 协议，那条路径已经被 `RunAgentInput.UserTurnPayloads`
  覆盖；
- 其它入口形态各异（gRPC / WebSocket / 自定义 JSON），由宿主自己挑
  字段映射更清晰。

如果未来出现两个以上**非 AG-UI** 宿主都需要这段逻辑，再考虑把它升格
成 `pkg/hosttools/usermessage` 之类的可选子包；目前不进 SDK。

### 6.5 多次 turn 的累计语义

每次 `sdk.Start(...)` 都对应一次"新 user turn"，宿主每次都应该把这次
turn 的 prompt Record 一次。Recorder 按 sessionKey 累计：

```
turn 1: recorder[sessionKey] = [user("hi"), assistant(...), ...]
turn 2: recorder[sessionKey] = [user("hi"), assistant(...), ...,
                                user("继续"), assistant(...), ...]
```

刷新拉 `Since(sessionKey, 0)` 就能拿到完整的交替对话历史。这件事 SDK
不会替宿主做去重——同一个 prompt 多次 `sdk.Start` 就会 Record 多次，
这是符合预期的（每次都是一次独立的 user turn）。

---

## 7. 风险 & 缓解

| 风险 | 缓解 |
|---|---|
| AG-UI 客户端本地已乐观渲染 user 气泡，server echo 后双显 | CopilotKit example 只写 recorder、不向当前连接 echo；其它 client 按是否乐观渲染选择 echo 或去重。 |
| 未来某个 adapter 误 emit `Role=RoleUser` | `streaming-adapter-contract.md` 明文禁止；可在 PR review 时 lint，必要时在 EmitStream 内 reject |
| recorder I/O 失败导致 user turn 丢失但 run 已起 | 与现行 forward 失败语义一致：log warn 不阻断 |
| Translator 现在显式带 role="assistant" 影响既有 wire 解析 | AG-UI 规范该字段一直可选，主流客户端早就处理 role 缺省；CI 跑 `verifyEvents` 单测覆盖 |

---

## 8. 验收

- [ ] `go test ./...` 全绿。
- [ ] `pkg/bridges/agui` 的 RoleUser 翻译单测落地。
- [ ] `RunAgentInput.UserTurnPayloads` 单测覆盖 4 个分支。
- [ ] `examples/showcases/web-copilotkit-hitl/server.go` 使用新 helper。
- [ ] `docs/workstream-streaming-chat.md` / `docs/streaming-adapter-contract.md`
      更新。
- [ ] flowx-agent 团队 dogfood：删除 `StreamHostUserMessage` 私有 Kind，
      前端无需 patch 即可显示 user 气泡的实时 + 刷新两条路径。

---

## 9. 不会做的事（对照原 issue 明确闭环）

- 原 issue §4.1 设计 A（`StreamUserMessage` Kind）—— **不采纳**。
- 原 issue §4.3 `Bridge.Handle(w, r, Options{Recorder, ...})` —— **不采纳**。
- 原 issue §4.2 设计 B（`Role` 字段）—— **采纳并简化**：只开放
  `RoleAssistant` / `RoleUser` 两个常量；system / tool 等出现真实
  use case 再加。
