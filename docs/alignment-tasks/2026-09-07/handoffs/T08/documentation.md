# T08 — CodeBuddy 常驻中断的部分结果

Source-Internal-Commits: `126d610dfb6afd2cca19661ab0a7c0b6f626b490`。工作项 W04-R06；实施基线为 G01 `0adb8355378b0d1c0457119f96da470f4c33366a`。本片段由 G02 在放行本批前合并到中央文档与 CHANGELOG。

## 公开行为与复现

以前，CodeBuddy 常驻进程在 prompt 交付后发生取消、deadline 或断流时，Driver 返回空 Response。即使正式 partial-message 已有文本，T05 的公共 RunError carrier 也无法恢复 Driver 丢掉的数据。

现在，常驻非 fallback 结果统一由同一个正式 parser 构建 Response，并保留 transport/context cause。`Thread.Run` 和 `Thread.Stream().Result()` 的失败返回仍为 `nil, error`；用 `errors.As` 取得 `*adaptor.RunError`，从其 `Result` 读取可用的 Text、Raw stdout/stderr/正式 Terminal、Transcript、Usage、Model、Provider、Metadata 与 Services。partial-message 中已观察到的 Usage 在尚无终局聚合用量时按正式 message ID 求和；同一 message 的累计 counter 更新及重放只计新增差额。继续识别既有 `cache_read_input_tokens` / `cached_input_tokens` 明确字段映射；前者的数值（包括零）优先，别名只表示同一个计数，不重复相加。没有可归属的 message ID 或没有合法、非负整数计数时，不推测用量。正式终局用量仍优先，已观察零值不会变成 nil。未观察到的 runtime service 不产生成功报告。

```go
result, err := thread.Run(ctx, "work")
if err != nil {
    // result == nil；包括取消时的部分输出也只通过同一个错误取得。
    var runErr *adaptor.RunError
    if errors.As(err, &runErr) {
        partial := runErr.Result
        _ = partial.Text
        _ = partial.Raw()
        _ = partial.Transcript()
    }
    // 原 context.Canceled、context.DeadlineExceeded 或 transport cause 可匹配。
    return err
}
_ = result.Text
```

失败进程退出时先排空 stdout，再等待 stderr copy 完成并 finalize parser，保留失败终局之后的原始诊断和没有换行的 stderr 尾部。初始化或复用 prompt 的 Write 提前失败也进入同一收尾，不能因 `n > 0, error` 丢掉已经观察到的诊断。断流保留原 EOF；如果 `Wait` 还取得真实 `*exec.ExitError`，错误链同时保留该原始对象，Response 使用实际退出码。没有更具体主因时，已观察的正非零退出报告 AgentError；context 取消/deadline 与已确认的审批/provider failure 不被进程收尾次因覆盖。故障 writer 的进程树终止只执行一次，私有 CommandContext 在 Wait 后释放，避免内部清理产生宿主并未请求的取消主因。停止故障 writer 导致的 signal 只作为次因，不伪造未观察到的进程状态。

无正式终局时仍使用既有 partial-message 重建和最后一段 assistant 文本；有成功终局时以其 result 文本为准，包括合法空文本，不拼接全部 assistant frame。CodeBuddy 没有独立、有界的正式 summary 字段，所以 Summary 继续为空。任意 stdout JSON 的 text/session 字段不会变成 assistant Text 或 checkpoint。

## 健康与重放边界

错误路径不生成有效 checkpoint，即使 parser 在传输失败前已经看到看似成功的终局。取消、非零断开、畸形协议和缺失终局均不得污染 store；已有健康 Thread record 保持逐字段一致。首次中断没有健康 checkpoint 时，下一轮仍只能新建。失败 writer 被回收后才能启动 replacement，交付后不自动重放；交付前仍仅允许既有的一次安全 fallback。Thread 默认常驻、WithSpawn、本轮临时进程不注册为后续 writer 的语义不变。

不采用 internal 仅凭已发送 prompt/session ID 就保存取消 checkpoint 的行为，也不使用 detached context 持久化中断状态。结构化输出、append prompt、todo、capability 以及其他 provider 不属于本任务实施范围。

## 合并目标

- `docs/public-errors.md` 的执行后部分 Result / CodeBuddy 边界：常驻错误 Response 不再为空，原 cause 与实际 Wait ExitError 均可达，已观察的非零退出有正确分类。
- `docs/streaming.md` 的取消与结果审计：partial-message、完整 Raw/Terminal/Transcript/Usage 保留，合法空 final text 与空 Summary 合同不变。
- `docs/api-reference.md` 的 Result / Thread 结果段落：上述取回例子，以及失败不改旧 checkpoint。
- `CHANGELOG.md` 的修复段落：CodeBuddy 常驻中断与提前 Write 失败保留部分结果、stderr/失败终局 stdout 尾部、按 message 累计的已观察用量和实际进程错误；保留交付后不重放与 checkpoint 健康要求。
- `AGENTS.md` §14.1：G02 验收后可记录 T08 的 provider 部分结果实现已交付；独立跨层、平台和 live 要求仍待后续任务验收。

局部 godoc 已更新 `codebuddy/doc.go`。无新增公共 Go 声明、Config、执行入口或依赖，不改变 root/Driver AST golden；无需新增 require 选型。

## 验证与边界

本地为 macOS arm64 / Go 1.26.5，使用本测试二进制的 CodeBuddy 协议子进程，未调用真实 provider。失败历史包括修复前空结果、缺少 partial Usage，以及失败终局后 Raw stdout 尾部丢失。attempt1 独立复审又实际复现了提前 Write 失败未收尾、不同 message 用量误取最大值和实际 ExitError 丢失；attempt2 补入对应回归，不将原包级测试通过当作这些合同已成立的依据。测试覆盖 Run/Stream 逐字段等价、取消/deadline/非零/缺终局/畸形 JSON/畸形终局/正式 provider error、首次中断不落 checkpoint、原健康 record 不变、replacement 单 writer、提前 Write 错误保真、同 message 累计与缓存字段别名去重，以及终局文本/Summary/零用量合同；既有包级测试继续覆盖 WithSpawn、重建、预热和安全 fallback。

必需验证为 `go test -count=1 ./codebuddy` 和 `go test -race -count=5 ./codebuddy -run TestAlignmentCodeBuddyPartial`，在源码固定 SHA 后执行，真实计数与日志由同目录的外部 result/evidence 记录。沙箱阻止的 loopback listener / profile lock 用已授权的受审查本地执行重跑，保留原失败历史。live/E2E/golden 更新环境门全程关闭。

本任务不提供 Linux、原生 Windows、真实 CodeBuddy live 或付费调用证据，不代表 G02 放行、W04 独立验收或发布门禁通过。
