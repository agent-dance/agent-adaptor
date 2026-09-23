# R047 — CodeBuddy 原生 agent 名称的 loader 准入

基线为 `6f8906840639cf13fcccabafd9224fdd17de7f0f`。这是独立兼容性缺陷，不是旧 cold exact-text 或 catalog live 失败的已证原因；原 catalog 使用合法的 `alignment-catalog-agent`。本修复不运行 live，不更改其 prompt、模型、次数、时限或成功判据，也不关闭 G05/B06/G06。

## 正式依据

root 已核 CodeBuddy `2.155.0` 官方 `dist/codebuddy-headless.js`，完整 bundle SHA-256 为 `54515dc7cec9ee36bad490dc33ac2a9c39775ba1036fcc2c354c3e2a43d9673e`。外部 finding 位于 `docs/alignment-execution/2026-09-16/live-repair/repairs/R047-T15-native-agent-name/finding.json`；下列均为原 bundle 的半开 byte range。

| 片段 | byte range | SHA-256 |
|---|---|---|
| `isSafeCustomAgentPathSegment` | 2428669–2428999 | `8386838f89885c18c6351b56713db6b280666e041dc6c4dc042ce32e6c9d6d68` |
| `sr` export binding | 2428371–2428621 | `eb66551539596faa6c97fb3c31a3311136497bfc21942b0c44705de655613671` |
| `parseAgentFile` 调用 `sr` | 4655283–4655813 | `612f757b35b69efe6200ccad8d32ecc52482987c5b5e9449e51a7fe97c0ed4c3` |

loader 从 frontmatter `name` 或文件 basename 取得名称，名称不满足 safe path segment 时直接跳过。条件包括 trim 后不变、非空、不为单独 `.`/`..`、不含 `/`、`\`、NUL、`:`，以及路径非 absolute、basename/normalize 不变。core 既有资源归一化之后，Go `TrimSpace` 尚未移除的首尾 U+FEFF 也会被 ECMAScript `trim()` 改变，因此明确拒绝。新建 agent UI 的 lowercase/reserved-name 校验不是 loader 条件，不上升为 SDK 约束。

## 实施与合同

唯一生产修改在 `internal/profileagents/codebuddy.go`：对已经解析的 native name 拒绝 `/`、`\`、`:`、单独 `.`/`..` 与首尾 U+FEFF；保留原有空值、UTF-8、NUL 校验。没有新的 sanitize/rename，也不改变共享资源归一化。已有 `Sync` 先构造全部 entries，再 reconcile，因此前面的合法 entry 不会在后面的非法名称被发现之前写入，既有健康 agent 文件与 manifest 保持。

合法名称继续保留逐字 native identity；大小写、中文、内嵌点、空格、Windows 设备名、UI 保留名和长名称仍使用独立的安全 `.md` 文件名编码。含 `/` 的 opaque Key 在显式提供合法 RuntimeName 时仍可用。没有修改其他 provider 的 helper、公共 API、AST golden 或执行入口。

`SourcePath` 的 resolved RuntimeName 接受相同验证，但源文件原字节仍由调用方负责。SDK 不解析/重写 native frontmatter；调用方须保证其 name 匹配已解析 RuntimeName。已编码 filename 时不能依赖 basename fallback 来恢复原 native identity。

`SyncProfile` 返回明确名称错误。公开 `Run` 中物化发生在 CodeBuddy `driver.Run` 的 `prepareRun`，故在启动 CLI 前失败时按既有边界返回 `nil, *RunError`：`ReasonInfrastructure`、非 nil Result，Cause 保留 `invalid runtime name`；未运行的 provider 不产生 Text、Raw 或 Transcript。未新增第二条校验/执行管线，也不将“CLI 尚未启动”误等同于“Driver.Run 尚未进入”。

## 反例与验收

外部 `owner/red-tests.patch` 是固定基线上的首次测试补丁，`red-profileagents.log/json` 与 `red-public.log/json` 保存确定性红例和运行身份。旧实现接受 slash/backslash/colon/dot/dotdot/BOM 名称并替换健康文件；公开 Run 到达的是测试二进制 canary（退出 99），不是官方 CLI。NUL 和非法 UTF-8 的原有拒绝是绿色对照。外层 PATH canary 始终未触发。

首次 `green-focused` 的失败也保留：生产拒绝已成功，但测试错误地要求 Run 不能返回 RunError。按上述 Driver.Run 边界修正为检查准确分类、非 nil Result、原 cause、空 provider 审计和零 CLI；未以接受任意 error 的方式放宽测试。后续 `green-focused-contract` 同时覆盖合法 catalog 与失败资源保护。

新增和调整的合同测试：

- `TestAlignmentCodeBuddyUnsafeNativeNamePreservesResources`：inline/SourcePath 下危险名称拒绝，合法前置 entry 不写入，健康 agent 文件和 manifest 原字节不变，NUL/非法 UTF-8 旧边界保持。
- `TestAlignmentCodeBuddyPublicRejectsUnsafeNativeNamesBeforeCLI`：公开 SyncProfile/Run；默认不安全 Key、显式不安全 RuntimeName、SourcePath 均明确失败，验证零 CLI、准确错误合同、无 provider 输出、无 agent 文件污染。
- `TestAlignmentCodeBuddyAgentNameAndFileSeparation`：将错误的 slash/traversal 成功预期移入拒绝测试，保留 Unicode、大小写、长名称、散列前缀无碰撞并增加合法点、设备名和 UI 保留名控制。
- `TestAlignmentCodeBuddyPublicNativeCatalogNames`：默认 Key 改为合法名称；原不安全 default Key 已进入拒绝矩阵；safe RuntimeName + opaque Key 保持正式 fake 协议 canonical fact 验证。增加内嵌点、设备名和 UI 保留名成功控制。
- 原 `TestAlignmentCodeBuddyAgentsSyncWithoutCLI` 与 `TestAlignmentCodeBuddyPublicMaterializedCatalogExecution` 保持。

最终 SHA 运行原 T15 四命令及 scoped vet，完整日志/退出码/SHA/散列保存在外部 owner 目录，避免 commit 自引用：

```text
go test -count=1 ./codebuddy
go test -race -count=5 ./codebuddy -run TestAlignment
go test -count=1 ./internal/profileagents
go test -race -count=10 ./internal/profileagents
go vet ./codebuddy ./internal/profileagents
```

固定 `/Users/blurooo/.xvm/sdk/go/1.27.1/bin/go`，私有 HOME/USERPROFILE/XDG/CodeBuddy 配置根、PATH CLI canary、GOPROXY=off、GOWORK=off；`AGENT_ADAPTOR_LIVE_CONFORMANCE`、`AGENT_ADAPTOR_E2E`、`UPDATE_API_GOLDEN` 均为 `0`。这些是离线实施验证，root 独立验收后才可整合。

## 交中央 owner 的使用文档文字

CodeBuddy profile SubAgent 的 `RuntimeName` 必须是官方 loader 能加载的单一路径片段：不允许 `/`、`\`、`:`、单独 `.`/`..`、NUL、非法 UTF-8 或首尾 U+FEFF。未设置 RuntimeName 时采用已解析 Key；若业务 Key 包含 `/`，请显式提供合法 RuntimeName，例如 `profile.SubAgent{Key: "catalog/reviewer", RuntimeName: "reviewer"}`。SDK 在 SyncProfile/Run 启动 CLI 前明确拒绝非法名称，不静默改写身份。合法中文、大小写、内嵌点和 Windows 设备名仍保留 native 名称，并以独立安全文件名写入。SourcePath 继续保留原字节，原生 frontmatter 与 RuntimeName 一致性由调用方负责。

## 交中央 owner 的 CHANGELOG 文字

- 修复 CodeBuddy profile SubAgent 将官方 loader 无法加载的 native name 写入已编码 `.md` 文件后仍成功物化的问题。SyncProfile/Run 现在在 CLI 启动前明确拒绝不安全名称，保留健康 agent 文件；业务 Key 含分隔符但显式 RuntimeName 合法的用法继续支持。合法 Unicode/大小写/内嵌点/设备名的原生身份及 SourcePath 原字节合同不变。

依赖选型：仅标准库字符串校验，无新增依赖。
