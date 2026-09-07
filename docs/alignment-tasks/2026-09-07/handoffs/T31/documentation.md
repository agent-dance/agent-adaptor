# T31 / W06-R03 文档交接

## 公开语义与可复现差异

基线 `93ef44f24e63ce52ad29dce2b54ff28fa0503470` 中，`WorksWithHITL=false` 同时拒绝 native 和 prompt 两种机制。`alignment_structured_hitl_test.go` 的基线失败 fixture 通过 JSON 装载冻结的新声明，因此可在旧源码运行并重现 Permission Ask 没有自动 fallback；另一 fixture 证明拒绝前已经获取 workspace/runtime/provider attachment。原始失败日志位于交接 evidence，不属于发布验证成功记录。

当前 SPI 新增真实 `driver.StructuredOutputHITLCapability{Permission, PlanReview, Question bool}`；`StructuredOutputCapability.NativeHITL` 与 `.PromptValidateHITL` 分别是该类型指针。每个非 nil 矩阵只替换本机制的 `WorksWithHITL` 判定：所有有效 Ask Kind 都必须为 true；全 false 明确拒绝，即使旧 bool 为 true。nil 保留旧 bool 的已发布语义；非 Ask 不读取相应字段；没有 schema 时不检查这些矩阵。普通 `RunPolicyCaps`、机制 `JSONSchema*`、`WorksWithRun` 与所选 transport 的 `WorksWithStreaming` 仍独立门控。

例如 fake Driver 宣告 native 支持 PlanReview/Question、prompt 支持三个 Kind，并保留旧 bool=false：Question Ask 选择 native；Permission Ask 或含 Permission 的混合 Ask 选择 prompt+本地校验。消费者仍只传 `WithSchema` 与 `WithPolicy`，不选择执行机制：

```go
res, err := adaptor.New(configuredDriver).Run(ctx, "extract project metadata",
    adaptor.WithSchema[ProjectMetadata](),
    adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
        Permission: adaptor.ApprovalAsk,
    }}),
)
```

这里的 `configuredDriver` 必须真实声明对应能力；T31 未修改任何内置 Driver 声明。Claude 的 native+Question/PlanReview 与 Permission prompt fallback 由后批 T07 接线及验证，不在本批宣布 provider 组合可用。既有无 schema Permission Ask 不变。

静态 schema/transport 协商现在在 `openStream` 中普通 policy 校验之后进行，先于 profile/workspace/runtime/skills/Thread lease；结果只保存在本次 `RunSettings` 私有状态中，clone 清空缓存，`wiring.resolveRun` 直接消费。native 优先，自动 prompt fallback；schema 不兼容 rich transport 时，只有没有有效 Ask 需求才可退到 batch。Run 与 Stream 请求和结构化结果一致；不支持或非法 schema 返回现有 typed error，资源及 Driver 都不启动。

## G01 必须合并的具体文档段落

- `docs/structured-output.md` 的 “Automatic capability negotiation”：把 “before process launch” 加强为 “before acquiring run resources”；替换一律要求 WorksWithHITL 的段落为上面的逐机制 nil/显式矩阵规则；batch fallback 增加不能丢弃有效 Ask 的条件。内置能力表维持本批实际 provider 声明，不提前填入 T07 目标。
- `docs/run-policy.md` 末尾 schema/HITL 兼容段落：schema 能力不授予普通 policy 不支持的 Ask；逐 Kind、混合 Ask 与机制 fallback 按上面的算法说明，保留无 schema 既有行为。
- `docs/api-reference.md` structured-output 协商段落：同一调用在资源获取前确定 schema/source/transport；消费者仍只有 schema CallOption，无模式选择器。
- `CHANGELOG.md` 本批 Added：Driver SPI 新的 per-kind HITL 真类型和两项可选指针，nil 兼容既有驱动。Fixed：schema+Ask 按机制固定协商，拒绝发生在资源前，不再通过 batch 降级丢弃 Ask；Run/Stream 一致。
- `docs/internal-history-alignment-plan-2026-09-07.md` §2.2 W06 总表仍残留“当前自动回退”，G01 需与已修订正文统一；已向协调者报告。

局部 godoc 已同步 `driver/driver.go` 与 `adaptertest/doc.go`。按 R003，`stream.go` 的 Result 注释同步前批 C02 冻结合同：执行后失败经 RunError 携带部分 Result/cause，执行前普通 error；实际错误实现由同批 T05 经 G01 合入，本任务不引用其新增符号。

## SPI golden、conformance 与依赖选型

Driver AST golden 精确增加两个指针字段和一个三字段真结构体；没有 root alias、Config 或机制选择器。`adaptertest.VerifyStructuredOutputCapability` 保持已有签名并校验矩阵 true⇒本机制/WorksWithRun；新增 `VerifyStructuredOutputDescriptor` 联合普通 Ask 能力检查，suite 使用完整 descriptor 守卫。负向 fixture 分机制、分 Kind 验证缺机制、缺 Run、缺 Ask 的声明；显式全 false 和 nil 不强加 Ask 能力。

没有新增依赖。逐布尔矩阵解析与受控诊断使用标准库即可，既有 schema 验证库及统一管线保持原样。不会新增协议解析或 IPC 依赖。

## 未采用的 internal 行为

只按固定 internal `426191444582f9fbbe951dbd8b54c1004464262c` 读取历史。未移入旧 `WorksWithStart`、`OutputSchema.Mode`、SDK/Start 等入口，也不采用把单个 WorksWithHITL=true 当作所有 Kind 已支持。没有修改 provider 参数、parser、checkpoint 或 persistent 逻辑；不从 internal 历史测试结果推断真实 CLI 当前能力。

## 验证边界

在 macOS arm64 运行 T31-V01 全部四包测试与 T31-V02 `-race -count=5` 专项，实际 SHA、命令、测试数量及允许 skip 由提交后的 result.json/evidence 记录。首次全包预跑受 sandbox loopback bind 限制，保留失败日志；按既有授权解除该限制后重跑完整原命令，不缩减包或次数。所有检查显式设置 live/E2E/API-golden 更新门为 0，不运行真实 provider CLI。

本地行为 fixture 覆盖 1,296 组矩阵组合、transport/policy 独立门、Run/Stream 等价、逐调用 policy 整值覆盖、无 schema、无资源获取及仅一次协商。未在本任务执行 Linux/Windows 或付费 live；这些证据由后续专门任务提供。T31 完成只代表 SPI/core 交付，不代表整个 W06、G01 或发布门禁已关闭。
