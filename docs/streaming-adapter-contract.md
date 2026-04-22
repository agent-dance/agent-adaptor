# Streaming Adapter Contract

本文档定义把 streaming 能力加到一个 `DriverAdapter` 时必须满足的硬合同。符合此合同的 adapter 能与 `pkg/bridges/agui` 和 `pkg/bridges/sse` 无缝协作，无需为每种 bridge 单独写适配层。

本文档是 `docs/workstream-streaming-chat.md` §12 的可执行补充。该 workstream 完成的是 core SDK 骨架 + codex 的 streaming 落地；Claude / Cursor 后续跟进时按本文件组织改动。

## 0. 结论先行

- `DriverAdapter.Run(..., sink)` 是唯一执行入口；不新增 `Stream()` / `Chat()` 之类的并行入口
- `req.Streaming == true` 时 adapter 应切到自家最细粒度的 token-level 通路；否则走原有批量路径
- 所有结构化事件都通过 `sink.EmitStream(StreamPayload)` 发射
- `StreamPayload.Sequence / Timestamp` 由 SDK 统一赋值，adapter 不自己写
- 不覆盖的 notification 透传到 `StreamPayload.Raw`，不得吞掉

## 1. 必须实现

### 1.1 `StreamAwareDriver`

```go
func (a adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,  // 协议原生事件流
		TokenLevel:   true,  // 字符级文本 delta
		Reasoning:    true,  // 思考/推理 delta
		ToolCallArgs: true,  // 工具调用参数 delta
		HITL:         false, // v1 不实现审批回路
	}
}
```

`StreamCapability` 是 bridges 侧降级决策的依据；如实填写即可，宁可保守不可虚报。

### 1.2 `req.Streaming` 分派

```go
func (a adapter) Run(ctx context.Context, req DriverRunRequest, sink EventSink) (DriverRunResult, error) {
	if req.Streaming {
		return a.runStreaming(ctx, req, sink) // 走 token-level 通路
	}
	return a.runBatch(ctx, req, sink)         // 走原有通路
}
```

两条路径**必须返回等价的 `DriverRunResult`**（尤其是 `Checkpoint / Output / Usage`），否则宿主在 streaming 与非 streaming 之间切换会感到不一致。

### 1.3 `StreamPayload` 发射规则

| 事件 | 关键字段 | 说明 |
|---|---|---|
| `StreamRunStarted` | `ThreadID`, `TurnID` | 当且仅当 run 首次开始 |
| `StreamRunFinished` | `Usage` | 最终 usage；缺失时由 bridge 合成空 payload |
| `StreamRunError` | `Error` | 致命错误；bridges 会据此关闭所有挂起的 message/tool lifecycle |
| `StreamTextStart` | `MessageID` | 每个 message 只发一次 |
| `StreamTextContent` | `MessageID`, `Delta` | `Delta` 非空；同一 `MessageID` 可多次 |
| `StreamTextEnd` | `MessageID` | 配对关闭 |
| `StreamToolCallStart` | `ToolCallID`, `Name`, 可选 `Args` | `Args` 是整段快照或起始 |
| `StreamToolCallArgs` | `ToolCallID`, `Delta` | 可选；`StreamCapability.ToolCallArgs=false` 的 adapter 不发 |
| `StreamToolCallEnd` | `ToolCallID` | 配对关闭 |
| `StreamToolCallResult` | `ToolCallID`, `Result` | `Result` 是 map；bridges 负责序列化 |
| `StreamReasoningStart / Content / End` | `MessageID` | 三元组；`Delta` 非空 |

未覆盖的协议事件必须透传：

```go
sink.EmitStream(agentadaptor.StreamPayload{
	Kind: "",
	Name: providerMethod,
	Raw:  map[string]any{...},
})
```

bridges 会把 `Kind == ""` 的 payload 映射成 AG-UI `CUSTOM` 事件。

## 2. 硬约束

### 2.1 进程与 goroutine 生命周期

- adapter 可以在 `Run` 内部开启 goroutine 监听 CLI notification
- **`Run` 返回前必须等所有 goroutine 退出**；否则 `sink.close()` 会和进行中的 `EmitStream` 产生 race
- SDK 的 `dualSink` 在 Block 背压模式下依赖这个合约保证无 race

### 2.2 事件顺序

- 同一 `MessageID` / `ToolCallID` 的 Start / Content / Args / End / Result 必须严格按照时序发射
- 不同 id 之间可以交错
- SDK 保证 `Sequence` 在 `EmitStream` 时原子自增，bridges 据此做事件排序

### 2.3 Session 等价

- Streaming 与非 streaming 路径产生的 `DriverCheckpoint` 结构必须一致
- 包括 `State.Data[SessionParamCWD] / SessionParamWorkspaceID` 等校验字段

### 2.4 不得污染 `RunEvent`

- 结构化事件**只**走 `sink.EmitStream`，不得也塞到 `sink.Emit(RunEvent)`
- `Emit` 继续承担生命周期元事件（spawn、runtime、stderr chunk）

### 2.5 HITL（v1 暂不实现）

- 如果底层协议有 server-initiated request（如 codex 的 `item/commandExecution/requestApproval`），adapter 在 v1 内**自动 deny** 并发射：

```go
sink.EmitStream(agentadaptor.StreamPayload{
	Kind: agentadaptor.StreamHITLRequested,
	Name: approvalKind,
	Raw:  requestPayloadMap,
})
```

- 不得阻塞等待宿主的响应（v2 另行设计 `HITLRequestHandler`）

## 3. 背压与背后语义

- SDK 默认 `BackpressureDropStream`：stream channel 满时丢 payload + 发 `StreamDropped` marker
- adapter 不需要关心背压；`sink.EmitStream` 总是立即返回
- 严格模式（`WithEventBuffer(_, _, BackpressureBlock)`）下 SDK 会让 adapter goroutine 阻塞；**adapter 须在 `ctx.Done()` 时尽快退出**，否则会撑住整个 run

## 4. 映射表模板

每个 adapter 应在自己包下维护 `README-streaming.md`，给出 provider 事件到 `StreamKind` 的完整映射表。参考：

- `codex/appserver/translate.go` §8.3 的表格化映射
- `claude/README-streaming.md` (Phase 5 交付)
- `cursor/README-streaming.md` (Phase 5 交付)

表格式建议：

| provider event | 触发条件 | 映射到 StreamKind | 关键字段 |
|---|---|---|---|

**脚注（Claude `input_json_delta`）**：`StreamToolCallArgs.Delta` 承载的是协议层原始字符串片段，单片未必合法 JSON；宿主自行累积与解析；完整参数快照以 provider 终局/全量帧为准。

## 5. 验收

新 adapter 接入 streaming 时必须补上：

- [ ] `go test ./yourdriver/... -short` 全绿（不启用 streaming 的原路径回归）
- [ ] `yourdriver/run_live_test.go`（`-tags=<driver>_live`）：haiku prompt 看到 ≥ 3 条 `StreamTextContent`、合法 `StreamRunFinished.Usage`
- [ ] `pkg/bridges/agui` 的 fixture 测试：使用采集到的 StreamPayload 序列 round-trip 到 AG-UI 合法流
- [ ] `README-streaming.md` 映射表完整

## 6. 违反合同的后果

- adapter 在 `Run` 返回后继续调 `sink.EmitStream` → `dualSink` close 时 race，Block 模式下 panic
- `Sequence` 手写 → 和 SDK 分配冲突，宿主看到乱序
- 把结构化事件塞进 `RunEvent.Data` → bridges 不识别，数据丢失
- `DriverCheckpoint.State.Data` 与批量路径不一致 → 在两种模式间切换时 session resume 失败

违反任一条将触发 CI 的 adapter conformance 测试失败。
