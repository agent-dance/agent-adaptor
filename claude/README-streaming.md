# Claude Streaming 落地备忘

本文件预留 Claude adapter 接入 streaming 的具体映射表与 CLI flag 约定。Claude 尚未在本次 workstream 实际落地；实施者按此清单开工即可，不需要改动 `core SDK` 或 `pkg/bridges/*`。

参见：[streaming-adapter-contract.md](../docs/streaming-adapter-contract.md)、[workstream-streaming-chat.md](../docs/workstream-streaming-chat.md) §12.1

## 1. CLI flag

当 `DriverRunRequest.Streaming == true` 时，在现有 `--print - --output-format stream-json --verbose` 参数上追加：

```
--include-partial-messages
```

flag 与 `extended_thinking` 互斥（AG-UI 调研材料见 workstream doc §17.3）。若 `ClaudeConfig.ExtendedThinking` 被设置且 `Streaming == true`，`ValidateConfig` 返回明确错误。

## 2. 事件映射

基于 `claude-code` 0.x 的 `stream-json` partial message 事件集：

| Claude event | StreamKind | 关键字段 |
|---|---|---|
| `{"type":"system","subtype":"init"}` | `StreamRunStarted` | ThreadID=session_id |
| `stream_event` / `message_start` | `StreamTextStart` | MessageID=message.id |
| `stream_event` / `content_block_start(text)` | — | （延迟发 TextStart 到第一个 delta） |
| `stream_event` / `content_block_delta.text_delta` | `StreamTextContent` | Delta |
| `stream_event` / `content_block_stop(text)` | `StreamTextEnd` | MessageID |
| `stream_event` / `content_block_start(tool_use)` | `StreamToolCallStart` | ToolCallID=id, Name=name |
| `stream_event` / `content_block_delta.input_json_delta` | `StreamToolCallArgs` | Delta |
| `stream_event` / `content_block_stop(tool_use)` | `StreamToolCallEnd` | ToolCallID |
| `user.message.content[tool_result]` | `StreamToolCallResult` | ToolCallID, Result |
| `stream_event` / `message_delta` / `message_stop` | — | 累积 usage，`result` 时合并 |
| `assistant` (组装后的完整 message) | — | 不单独转发，bridges 已通过 delta 流覆盖 |
| `rate_limit_event` | `""` (opaque) | 透传 Raw |
| `system` / `api_retry` | `""` (opaque) | 透传 Raw；`error_status >= 5xx` 且连续失败时转 `StreamRunError` |
| `result` | `StreamRunFinished` | Usage, CostUSD |

## 3. StreamCapability

```go
func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    false, // partial + thinking 互斥，open question：细粒度 reasoning 走独立 run
		ToolCallArgs: true,
		HITL:         false,
	}
}
```

## 4. 文件组织建议

- 现有 `claude/driver.go` 增加 `req.Streaming == true` 分派
- 新增 `claude/streaming_parser.go` 专门解析 stream-json partial 事件
- 所有输出都通过 `sink.EmitStream`，不污染 `RunEvent` 通道

## 5. 验收（与 codex 对齐）

- `go test -tags=claude_live -run TestClaudeStreamingHaiku` 端到端
- 断言 `StreamRunFinished.Usage.InputTokens > 0`、≥3 条 `StreamTextContent`
