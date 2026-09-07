# T11：A2A parent/capability/todo wire

本片段由 G03 在本批放行前合入中央使用文档及 CHANGELOG。实现只改变 bridges/a2a，消费 G02 已验收的公开 Event/ObservationDemand。W05/W09/W10/W12 的 provider 接入、delegation 域映射与后续独立验证仍由各自任务验收，本交付不代替这些门禁。

## 公开变化与例子

建议合入 `docs/a2a.md` 的 ExposurePolicy、adapter.stream.v1、错误与兼容性段，以及 `docs/streaming.md` 的 bridge 投影说明：

```go
server := a2a.NewServer(agent, a2a.ServerOptions{
    AgentCard: card,
    Exposure: a2a.ExposurePolicy{
        IncludeCapabilityInvocations: true,
        IncludeTodos: true,
    },
})
```

新增 `IncludeCapabilityInvocations`、`IncludeTodos` 均默认 false；工具、HITL、Transcript 与 Diagnostics 不会隐式开启它们。opt-out 完全省略对应事实，不发送透露 kind/key/count 的提示。无事件只表示未收到事实，不能证明未调用、完整覆盖或计费/安全审计完整。

Capability 只传封闭的 kind/key/operation/invocation/phase/evidence/source/time/duration/error-code 与父坐标，没有 args/result/Raw/URL/header/env/prompt/error 正文容器。Todo 为显式正文暴露，Items 保留顺序、真实或 SyntheticID 和状态，经过既有 inline-secret 过滤；`items: []` 是已确认清空，`null`/缺省非法。不会为了 todo 打开工具或 Transcript。

Source/Upstream 的远端坐标额外受 `Diagnostics.IncludeMetadata` 控制，与 Capability 的 provider/host/relay 来源枚举不同。当前 EventMeta 的 RunID/Sequence/Time 始终是权威，Source 不替换排序坐标。工具 ToolCall/ToolResult 保留 ScopeID、ParentScopeID、ParentToolCallID；当前 Meta、来源链、todo Items、tool maps 和 Duration 各次编解码均独立复制。

两个 A2A 请求模式都调用既有 Runner.Stream，在原 CallOption 后追加私有 demand provider；AttachRun 只返回 Observation，DetachRun 没有待释放资源，不装 Store/Observer/Events。Agent.Run 与 Agent.Stream 使用相同 demand 时得到同一 provider transport，桥不判断 CLI 协议、不猜工具名。

## wire 验证与错误

`adapter.stream.v1` 名字及 URI 保持不变，新增可选 `scope_id`、`parent_scope_id`、`parent_tool_call_id`、`capability`、`todo` 与 Source 的 scope/tool/invocation/delegation/upstream 字段。Capability/Todo 只能携带自己的内层语义值；外层 parent/工具/HITL/Raw 不得混入。新 kind 必须有合法非零 Meta；若 flat 字段出现，必须与 Meta 相符。Capability 的父引用不能指向同 scope 的自身 invocation ID，相同裸 ID 位于不同 scope 时仍可合法引用。

Capability/Todo 的 encode/decode 均校验封闭枚举、字段/类型、明确列出的 ID/Content UTF-8 字节界限、控制字符、RFC3339Nano 非零时间、JSON-safe 坐标/计数、8 节点 Source 和整个 envelope 的 65536 字节上限。合法带时区的 OccurredAt 解码为等价时刻的 UTC 公共值，时间出站也归一为 UTC。Duration nil 与真实 0 分别保留。Todo 不截列表或内容以伪造完整快照。

`DecodeAdapterEventV1(json.RawMessage(raw))` 对新 kind 直接检查原 bytes，拒绝重复 JSON key、额外顶层值、非法 UTF-8 与不配对 surrogate；即使第一次 schema 宣告为 foreign、后来才为 adapter.stream.v1，也以匹配但非法的重复宣告拒绝。新 kind 的 map/DTO 输入先检查结构值 UTF-8/类型，不能让 json.Marshal 修复坏字节；已经被客户端 decode 的 map 无法追溯原 bytes 的重复 key，不能声称原始字节审计完整。旧 wire 维持已发布兼容：旧 args/raw 中的大数和可解析零时间不因新事实规则被拒绝，RawMessage 与 map 对同一结构值给出一致结果。旧 kind 增加 parent/source 字段时，只对这些新增安全字段做严格校验，不将新解析规则套到无关的旧正文；新 source 的显式零时间、非法 UTF-8/字段及过长 scope 仍拒绝。本 schema 的未知 kind/非法值返回 matched=true,error 且无部分 Event。旧二进制不会自动理解新 kind；下游必须明确处理 unsupported/dropped，不能吞错。

opt-in 后，内容非法或总量超限时，以原 Meta 投影一份 `stream.dropped`，不新分配 Sequence。Raw 为四字段闭集：`dropped_count:1`、`reason`（invalid_payload/payload_too_large/unsupported_event_kind/relay_depth_exceeded）、`source:"a2a"`、`event_kind`（已验证 kind 或 unknown）。不回显 cause 或非法 payload。decoder 将已验证的 event_kind 保留在 Dropped.Details["event_kind"]。

ThreadKey 与 flat ThreadID mirror 保留全部合法 UTF-8 内容，包括 LF/tab 等被 JSON 正常转义的字符；不 trim、规范化或施加 2048 字节限额。既有 envelope 坐标的长度由整个 64 KiB envelope 限制；明确约定的 InvocationID/tool ID/ScopeID/ParentScopeID/DelegationID 的 2048 字节限额不变，Source 坐标的无控制字符规则不放宽。若 Meta 本身无法安全保留（例如原坐标使完整安全 drop 也超过 64 KiB、unsafe sequence、环或超过 8 节点的 Source），不截 key、不丢来源、不伪造序号来凑一个 drop。私有 translator 把安全编码错误交回同一 executor；取消并 drain 原 Stream，读取原 Result/RunError.Result，保留 ExposurePolicy 允许的部分 artifacts，然后走既有基础设施 error 路径。这一分支有 translator→executor 的实际 fixture，包括部分 RunError；未另建 error/HITL 策略。

## 新声明与冻结

新增桥级真实 DTO：AdapterCapabilityInvocationV1、AdapterTodoItemV1、AdapterTodoSnapshotV1；现有 AdapterStreamEventV1、AdapterEventSourceMetaV1、ExposurePolicy 追加 C03 指定字段。V1 后缀仅表示既存 wire 版本。根/SPI 无新增符号，不改 root golden。各字段 JSON spelling、闭集、null/unknown、来源深复制和全量清空由 `alignment_wire_test.go` 冻结。

局部 godoc 已更新 `bridges/a2a/doc.go`、`types.go`、`stream_status.go`。G03 应在 CHANGELOG 的新增/修复段说明：显式安全 observation wire、新父坐标保真、严格大小/编码验证及无法保留 Meta 的可观察基础设施错误。中央文档不由 T11 修改。

## 固定源的取舍与依赖选型

已从固定 internal `51d12bd143ae3be553673bca2d16db08e3bffb9f` 和 `eb82ed36eaf6339b00860b5506a683e6dbd74765` 的 `pkg/bridges/a2a/stream_status.go` 阅读来源。保留“同一版本可选字段”方向；不采用其无显式 capability exposure、宽松 JSON、清除混入字段后仍当有效事件、todo 不过滤/不校验/截断等行为。不恢复旧 StreamPayload 消费 API 或额外 streaming/relay 入口。

不新增依赖。标准库实现有界、封闭 DTO 校验和词法检查；既有官方 A2A 依赖继续只在桥边界内使用。新框架对这个小闭集没有明显可靠性优势；无顶层 require、generated/schema 或工具链变更。

## 验证边界

G02 base 先跑冻结 fixture，capability/todo unsupported 和 parent 丢失均实际失败，日志单独保存在 handoff evidence。修复覆盖默认 exposure、无 Store Run/Stream/executor、旧 fixtures、empty clear、UTF-8/time/枚举/长度/64KiB、safe drop 与基础设施错误、深复制和 parent。

Attempt 2 纳入独立评审 F01–F05 的原断言，保留修复前失败证据；增加旧正文与新 parent/source 混合、长 Source 坐标、opaque ThreadKey 转义及不碰撞回归。最终源码 SHA 上必须实跑 task 指定 `go test -count=1 ./bridges/a2a` 和 `go test -count=20 ./bridges/a2a`；实际命令、次数、exit、skip、SHA 与日志哈希由随后生成的 result.json 记录。普通检查显式关闭 LIVE_CONFORMANCE/E2E/UPDATE_API_GOLDEN，使用 Go 1.26.5 和本机 macOS loopback fixture，不调用真实 provider。未声明 Linux/Windows、付费 live 或发布门禁通过。
