# C04 文档片段（中央 owner：G00）

本片段记录已冻结、尚待后续任务实施的 W06/W11 合同。G00 应先合并方案事实勘误与接口分配；后续各批 gate 在对应功能真正落地后更新使用文档与 CHANGELOG，不能提前把合同设计写成已验证功能。

## 公开语义前后变化

当前生产基线只有 `WithInstructions`；新增 `WithAppendSystemPrompt(string) SharedOption` 让宿主向 provider 原生追加通道传递文本。它保留 provider 默认提示词，与规则文件/Instructions 和 user prompt 分开。构造与调用使用同一名字；近处替换、同作用域最后一次胜出，空串清除 SDK 默认值。合法文本原字节保留，包括首尾空白。

拟落地示例：

```go
d := claude.Driver(claude.Config{})
agent := adaptor.New(d,
    adaptor.WithAppendSystemPrompt("回复正文使用中文。"),
    adaptor.WithThreadStore(memory.NewStore()),
)
// 对本次无状态调用覆盖：
result, err := agent.Run(ctx, "解释这段代码", adaptor.WithAppendSystemPrompt("使用简短句子。"))
// 对本次调用清除构造默认值：
_, err = agent.Run(ctx, "解释这段代码", adaptor.WithAppendSystemPrompt(""))
_ = result
_ = err
```

该示例只是供后续 API 文档使用的片段，B00 未执行；T24 必须补齐 imports 和真实编译验证。Thread append 文本变化属于兼容性变化：ResumeOnly 拒绝，默认 Thread 仅通过既有安全新建/原子保存路径转换。单纯重启进程不足以允许旧会话继续。Cursor 非空请求启动前返回可 errors.Is/As 的 `ErrSystemPromptUnsupported` / `SystemPromptUnsupportedError`，空串仍沿原有行为。

W06 事实勘误：当前 Claude 普通 Permission Ask 已支持；当前 schema+Ask 因 WorksWithHITL=false 统一拒绝，不能声称已自动 fallback。本次目标为 native Question/PlanReview，Permission native 未证明时自动 prompt+本地校验。T31 的 `NativeHITL` / `PromptValidateHITL` 逐 Kind 矩阵保留 nil 的旧 bool 语义；SDK 仍只有一个自动协商结果，消费者无机制选择器。

## 合并目标和具体段落

| 时点/文档 | 应合并内容 |
|---|---|
| G00 `docs/internal-history-alignment-plan-2026-09-07.md` W06；任务包分工 | 更正 Permission 和 fallback 的基线事实；B01 T31 提供精确矩阵/core 算法，G01 后 T07 消费；所有公共文件明确 ownership |
| G00 合同冻结清单 | 纳入 `contracts/prompt-transport.md` 文件 SHA；记录 C02 结果/预算、C03 rich transport 与本合同的交界 |
| T31/G01 `driver/driver.go`、`adaptertest/doc.go` | 逐机制逐 Kind 的矩阵、nil/全false语义、RunPolicyCaps 蕴含、native 优先/自动 fallback |
| T07/G02 `claude/README-streaming.md`、`docs/structured-output.md`、`docs/run-policy.md` | native schema+Question/PlanReview 双向 stream-json；Permission fallback；result-only 终局；Thread native 临时进程形态；版本/live 证据边界 |
| T10/G03 root `options.go`、`errors.go`、`inspect.go` godoc；`docs/api-reference.md` | 唯一 SharedOption、setter、原字节与清除、typed preflight 错误、Inspect 默认值只读验证、不新增 ConfigSchema 字段 |
| T10/G03 `AGENTS.md` API 冻结计数说明 | 原 root With* 实数26，新增1后27；不把 WithSpawn/WithTools 两个既有名字重复计数 |
| T14–T17/G04 各 Driver godoc/README；`docs/structured-output.md`、`docs/run-policy.md` | 原生参数、Cursor unsupported、三 Driver startup signature 与 checkpoint guard、Codex三 handshake/两 transport、文件退出后清理；inline32KiB及完整Windows命令行/shim限制 |
| 每批 CHANGELOG 的对应未发布版本条目 | 明确 schema协商行为修复与 append 新公共API；附源码实现 SHA/实际验证，按 minor 评估新增公开面 |
| T24 README 各语言版、examples、API reference 最终校对 | 使用最终 API；说明 append 与 Instructions 的区别；编译且运行适当 fake-driver 示例；不可用代码片段需明确标记 |

建议落地后的 CHANGELOG 文本：

- 增加 `WithAppendSystemPrompt`，在构造和单次调用向受支持 Driver 的原生追加提示通道传递文本，支持覆盖与显式清除；Thread 兼容指纹、进程签名和恢复参数同步覆盖该内容。Cursor 非空请求明确拒绝。
- 修正 Claude schema/HITL 的精确协商：Question/PlanReview 可使用双向 native schema；Permission native 不可用时自动采用 prompt 与本地校验，保留既有普通 Permission Ask。

以上两条只能在相应实施批验收后作为已完成变更发布。

## 新增公共声明与 golden 理由

根包：`WithAppendSystemPrompt`、`(*RunSettings).SetAppendSystemPrompt`、错误 var/type alias。SPI：`SystemPromptCapability{Append bool}`、`Descriptor.SystemPrompt`、`Request.AppendSystemPrompt`、`ErrSystemPromptUnsupported`、`SystemPromptUnsupportedError{Driver,Reason}` 及 Error/Unwrap；T31 另增 `StructuredOutputHITLCapability` 和既有结构的 `NativeHITL` / `PromptValidateHITL` 字段。

root AST 守卫更新 `testdata/root_api.golden`；SPI AST 更新 `adaptertest/testdata/driver_api.golden`。先审查新增声明再精确更新，禁止自动重写掩盖范围外漂移。Codex 只改手写 `union.go`、`run.go` 等映射；三份正式 schema 已有字段，generated.go/schema 本项预期不变。

## 不采用的 internal 行为

不引入默认选项镜像、SDK/binding/Admin、OutputSchema.Mode、WithStart；不 TrimSpace 或拼入 user prompt；不接受只换 process sig 而忽略 Thread fingerprint；不使用同名同长度即命中的全局缓存；不漏 Codex resume/fork；不把 Cursor 伪装支持；不将历史 live 记录复制为本轮证据。

文件采用每次真实 spawn 私有临时目录/原子内容物化，进程持有期间保留，实际退出后清理，File handle 限定删除归属。CodeBuddy 和 Codex exec 的原生 inline 参数存在 OS argv 可见性；SDK 不再把内容复制进诊断/事件/wire。Windows npm cmd shim 的二次 shell 解释不能靠字节长度检查解决，无法无损表达的文本必须提前明确拒绝。

## 验证边界

本 C04 在 macOS 只做源码/正式 schema/官方文档读取及任务包静态校验；无 Go 生产改动、无 provider 实际调用、无 Linux/原生 Windows 行为测试、无付费 live。具体执行命令、SHA 与日志见 result.json/evidence，由协调者外部收集，不进入其引用的合同提交。

后续 T26–T30 必须在同一 G05 SHA 上记录真实 CLI 版本、profile 隔离、三 handshake/两 transport、Ask 响应与结构化终局等证据；缺 CLI/认证/授权记 blocked。模型随机标记对照用来验证 native 文本确实到达，单纯 argv fixture 或历史文档不能代替 live。
