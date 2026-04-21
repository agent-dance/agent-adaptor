# Cursor Streaming 落地备忘

本文件预留 Cursor Agent CLI 接入 streaming 的映射表。Cursor 尚未在本次 workstream 实际落地；实施者按此清单开工，不需要改动 `core SDK` 或 `pkg/bridges/*`。

参见：[streaming-adapter-contract.md](../docs/streaming-adapter-contract.md)、[workstream-streaming-chat.md](../docs/workstream-streaming-chat.md) §12.2

## 1. CLI flag

当 `DriverRunRequest.Streaming == true` 时，在现有 `-p --output-format stream-json --workspace ...` 参数上追加：

```
--stream-partial-output
```

Cursor 在 print 模式下**主动抑制** `thinking` 事件；`StreamReasoning*` 在 Cursor adapter 不产生。

## 2. 事件映射

基于 `cursor-agent` 0.x 的 stream-json partial 事件：

| Cursor event | StreamKind | 关键字段 |
|---|---|---|
| `{"type":"system","subtype":"init","session_id":"..."}` | `StreamRunStarted` | ThreadID=session_id, RunID |
| 首条 `{"type":"assistant", ...}` (partial) | `StreamTextStart` | MessageID=model_call_id |
| 中间 `assistant` 片段 | `StreamTextContent` | Delta=text |
| 最后一条 `assistant`（standard mode 整段） | `StreamTextEnd` + `StreamTextContent`(整段) | |
| `{"type":"tool_call","subtype":"started","tool_call":{...}}` | `StreamToolCallStart` | ToolCallID, Name, Args=完整 |
| `{"type":"tool_call","subtype":"completed","tool_call":{...}}` | `StreamToolCallEnd` + `StreamToolCallResult` | Result=tool_result |
| `{"type":"result","subtype":"success",...}` | `StreamRunFinished` | Usage=无 (Cursor 不一定提供), CostUSD |
| `{"type":"result","subtype":"error_*",...}` | `StreamRunError` | Error |
| `thinking` | — | 被 Cursor 主动抑制，不映射 |

## 3. StreamCapability

```go
func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    false,
		ToolCallArgs: false, // Cursor tool_call args 是一次性带完
		HITL:         false,
	}
}
```

## 4. Bridges 降级提示

由于 `ToolCallArgs=false`，`pkg/bridges/agui` 看到 `StreamToolCallStart` 不等 `StreamToolCallArgs`，直接配 `StreamToolCallEnd`。已在 bridge 状态机里兼容，不需要额外改动。

## 5. 文件组织建议

- 现有 `cursor/driver.go` 增加 `req.Streaming == true` 分派
- 新增 `cursor/streaming_parser.go` 解析 partial stream-json

## 6. 验收（与 codex 对齐）

- `go test -tags=cursor_live -run TestCursorStreamingHaiku` 端到端
- 断言 ≥3 条 `StreamTextContent`、`StreamRunFinished`
- `StreamRunFinished.Usage` 可能为 nil（Cursor 未必给），测试不作强制
