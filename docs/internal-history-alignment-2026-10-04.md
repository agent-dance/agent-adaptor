# internal 后续增量迁移（2026-10-04）

本轮从目标 `969514bf34d267a20681419c197c1858cf20cef6` 继续，源固定为拉取后的
`origin/master=2e57139e31891e359c4fe74b9186d6c146eca143`。实施分支为
`codex/internal-sync-20261004`，与用户原工作区隔离。没有改写 internal 的工作区，
也没有纳入其未提交的 vision 文档。

## 完整性与范围

对所有 refs/tag 可达的 **69 条提交**逐项登记：旧方案 45 条全部保留，新增 24 个
hash 去重为 8 个语义组。既有实现按实际代码和测试核对，重复分支按最终路径字节
及 patch-id 归并，不把相同修复重复移植。每条原提交、归组、处置、基线证据和
分支比较见 [完整历史清单](alignment-tasks/2026-10-04/history-inventory.md) 与
[机器可读清单](alignment-tasks/2026-10-04/history-inventory.json)。

依用户要求，精确排除 `1921636510ced4c830c0287fe68d41218f1ed185` 的本轮增量
迁移。它是 skill/MCP/agent 调用观测提交；不因此删除当前已验收的 capability
功能，也不排除它之后独立的修复。那些独立修复经核查已在当前实现中覆盖。

## 迁移结果与设计

| 任务 | 来源语义组 | 实现与新 API 边界 |
|---|---|---|
| M01 | N02：Claude result/后台任务 | assistant message stop 不再提前关闭控制输入；正式后台任务未完成时的 result 保留为中间审计，最终 result 才完成本轮。正式 parser 是唯一终局权威。 |
| M02 | N03/N04：历史窗口与完整起点 | 在可选 `sessionrecorder` 增加 Range、Tail 及 FromRunStart 包函数，保留既有接口和持久格式；按 typed RunID/消息生命周期查找已知下界，固定上界、不猜缺失归属；用户附图恢复到同消息的文本起点。 |
| M03 | N01：上下文/自动压缩 | 仅扩展 Claude/Codex 的真实 Config；校验、Inspect、指纹、进程复用与原生 transport 一致。拒绝显式冲突，不恢复旧 CommonConfig 开关或落盘修改 Codex 配置。 |
| M04 | N05/N06/N07：图片与可信提问者 | ImageContent 走唯一 Event 管线及 AG-UI/SSE/recorder；空 role 投影为 assistant。UserID 由宿主显式提供，保持原 UserTurnEvents 方法签名。 |

实现没有增加第二套执行、身份、审批或存储框架。已有较强的错误、checkpoint、
Raw、取消、单 writer、安全资源与 API 冻结合同继续适用。图片是宿主发布的内容
引用，不能据此宣称 provider 已支持图片输入或生成。AG-UI 原生文本 wire 没有
UserID 字段，保留原形状；raw SSE/recorder 保留用户文本标识，AG-UI 图片 CUSTOM
保留用户图片标识。A2A v1 不跨认证域转发图片或 UserID。

上下文设置属于构造期配置，`WithModel` 不会替宿主重新推导窗口。Claude 的原生
compaction window 与 Codex 的原生 token limit 不是同一阈值；具体范围、模型
限制和字段见 [API reference](api-reference.md#14-built-in-provider-drivers)。
新增 Config 字段会保守改变旧 Thread 的配置指纹，包括新增字段为零的情况；
零值保留原生执行默认值，不保证旧持久 checkpoint 的指纹兼容。

## 已检查但不纳入的独立分支

N08（`2f935a6`，后续合流 `b0dba48`）是未合入 internal 主线的 A2A 跨 HTTP
托管运行 feature。当前宿主已能通过 `OnApproval` 与 live `ApprovalRequest`
应答；缺少的是结束当前 HTTP 响应后继续托管同一 Stream、再由后续 message
恢复的内置便利。这项差异保留在清单中，没有标成“已覆盖”。

上游直接脱离请求取消、park 后停止 drain、另设 expiry 取消，并缺少容量、Close
和并发应答/取消归属。搬入将破坏当前审批超时、背压与资源生命周期合同。本轮
不引入该运行托管器；以后采纳必须先定义有界容量、可关闭宿主生命周期、持续
唯一 drain、原 Policy 超时和唯一终局归档，再基于已有 Stream/responder 实现。

## 并发、审查与验收

四个实现任务从同一目标基线进入隔离 worktree，精确文件所有权避免同批写冲突；
协调、实现、独立审阅最多六路并发。[任务包](alignment-tasks/2026-10-04/manifest.json)
保留每组来源与验收条件。独立审阅发现的问题须返还 owner 修复，不能以作者
测试通过代替合流验收。

合流执行完整 `go test -count=1 ./...`、`go vet ./...` 和任务包校验；race、
最低支持 Go、fuzz、前端及原生 Windows 由现有 CI 验证。普通检查关闭全部
provider live 门，不使用登录凭据或产生模型调用。工作期间沙箱拒绝本地监听的
失败和由此造成的测试等待保留原日志；允许 loopback 的定向反例用于区分环境
限制与产品缺陷，不以放宽断言或 skip 消除失败。

旧 47 任务、96 要求、45 处置、G04 基线及 G05 原命令保持。当前新增范围以
R052 的精确文件加入整合验收，没有删除原要求或覆盖旧失败。旧 G06 的 Cursor
失败仍是历史未关闭项；本轮迁移、离线测试或 CI 不能充当新 SHA 的四 provider
live 证据。不会据旧候选的通过记录宣称当前分支已可合入或发布。
