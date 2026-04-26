# Claude Streaming

Claude adapter 在 `DriverRunRequest.Streaming == true`（宿主 `WithStreaming()`）时在既有 `stream-json` CLI 参数上追加 `--include-partial-messages`，由 `streaming_parser.go` 将 `stream_event` 映射到 `StreamPayload`。不改动 core SDK、`pkg/bridges/*`。

权威设计与依赖评估见：`docs/workstream-streaming-claude.md`。

## CLI

在 `--print - --output-format stream-json --verbose` 基础上追加：

```
--include-partial-messages
```

与扩展思考（thinking）可同时开启：Sonnet 4.5+ / claude-code 2.1+ 下 `thinking_delta` 可与 partial 共存；`StreamCapability.Reasoning = true`。

## StreamCapability

```go
func (adapter) StreamCapability() agentadaptor.StreamCapability {
	return agentadaptor.StreamCapability{
		Native:       true,
		TokenLevel:   true,
		Reasoning:    true,
		ToolCallArgs: true,
		HITL:         true,
	}
}
```

## Wrapper 顶层 `type` → StreamKind

| Wrapper type | 行为 |
|---|---|
| `system.init` | `StreamRunStarted`（ThreadID=`session_id`）；与批量路径的 `TranscriptInit` 并存 |
| `system.api_retry` | 不透明透传（`Kind` 空，`Raw`）；连续 5xx / `willRetry:false` 可升级为 `StreamRunError` |
| `stream_event` | 见下文 `event.type` |
| `assistant` | **仅批量路径**：落 `Transcript`/Output；不向 `EmitStream` 二次发送正文（避免两套真相） |
| `user`（`tool_result`） | `StreamToolCallResult`（与 transcript 同步） |
| `result` | `StreamRunFinished`（usage / Raw 中带 cost、`stop_reason`） |
| `error` | `StreamRunError` |
| `permission_request` | `StreamHITLRequested`（广播事件；Permission=Ask 仍未声明支持，PlanReview / Question 的双向回填走 Phase 3 control_request） |
| 其它未列顶层类型 | `Kind=""`，`Raw` 透传 |

## `stream_event.event` 映射

与 Anthropic Messages streaming 形状对齐的部分：

| event.type | delta.type | StreamKind |
|---|---|---|
| `message_start` | — | （记录 `message.id`，不发 `StreamTextStart`） |
| `content_block_start`（text） | — | （记账；首个 `text_delta` 先发 `StreamTextStart`） |
| `content_block_delta` | `text_delta` | `StreamTextContent` |
| `content_block_stop`（text） | — | `StreamTextEnd` |
| `content_block_start`（tool_use） | — | `StreamToolCallStart`（可有 `Args`） |
| `content_block_delta` | `input_json_delta` | `StreamToolCallArgs`（**原样片段**，adapter 不做 JSON 拼接） |
| `content_block_stop`（tool_use） | — | `StreamToolCallEnd` |
| `content_block_start`（thinking） | — | `StreamReasoningStart` |
| `content_block_delta` | `thinking_delta` | `StreamReasoningContent` |
| `content_block_delta` | `signature_delta` | 累积审计，不向宿主流式 emit |
| `content_block_stop`（thinking） | — | `StreamReasoningEnd` |
| `message_delta` | — | 累积 usage / stop_reason |
| `message_stop` | — | 不向宿主流式 emit（仍以 `assistant`/`result` 为准） |

## Tool 参数片段

`input_json_delta.partial_json` 单片**不一定合法 JSON**。`StreamToolCallArgs.Delta` 的合同是「原始字符串片段」，宿主按需用 partial-json 库拼接；完整合法 JSON 仍以 `assistant` 全量帧里的 `tool_use.input`（批量路径解析）为准。

## 验收

- `go test ./claude/... -short` — fixture round-trip（`testdata/streaming-*.jsonl`）
- `go test -tags=claude_live ./claude/...` — 需本机 `claude` 已安装且可登录（端到端冒烟）

Bedrock / Vertex 组合的 streaming **未做全回归**；理论与 API 路由一致，若遇差异以 issue 追踪。
