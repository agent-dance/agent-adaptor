# R049：CodeBuddy catalog 安全诊断补充

基线 `8deec330aeb1be94e78b7b48a034ee7968d7f64f`，独立于 Windows 认证夹具修复工作树。本补充只增加观测，当前 catalog 失败原因仍未知。原 `actualEcho` 只在 typed callback 的 `input.Text == "VERIFY"` 时递增，零值不能证明总 callback 数为零。

原 catalog 回调入口另增一个 atomic 总数。现有 `tool.Define` 窄抽成私有 `alignmentCatalogEchoDefinition`，供 live 和离线测试调用同一实际 definition；description、匿名 input schema、ReadOnly、Revision、精确判断与原样返回均保持。所有原 live 断言仍由原 `actualEcho` 控制，总数不进入成功判定。

在既有结果诊断之后增加一个固定记录：

```json
{"stage":"catalog","kind":"catalog_callback","total":0,"exact_verify":0}
```

`total` 与 `exact_verify` 均为 int，只反映实际 callback 入口及原精确谓词次数。现有 `formal_tool` 仅在 `tool_use.name` 精确为 `DeferExecuteTool` 且 `input.toolName` 精确为 `mcp__agent-adaptor-tools__alignment_echo` 时增加三个 bool：

| 字段 | 含义 |
| --- | --- |
| `text_present` | 正式一层 `params` 对象存在 `text` 键 |
| `text_is_string` | 该值具有 string 类型 |
| `text_exact_verify` | string 原字节精确等于固定字面量 `VERIFY` |

缺失/非对象 params 得到三个 false；null/非字符串 text 不升级为 string 或 exact。未知/缺失/类型错误目标、未知工具或不同工具形态不追加这三个字段。不递归猜测，不输出任何参数值、正文、名称、ID、错误或凭据；原闭集工具分类与诊断字段保留。

`TestCodeBuddyCatalogDiagnosticTextPredicates` 使用独立 canary 覆盖缺失、null、非对象、各种非 string、空串、非精确串与精确串以及未知目标边界。`TestCodeBuddyCatalogDiagnosticCallbackCounts` 直接调用实际 definition，证明总数与精确数分离、原样回传、typed 无效输入不会进入 callback，且安全序列化不泄露输入。新增名称不匹配原付费 `TestAlignmentLive` 根选择器；既有 `TestAlignmentLiveDiagnosticSafeProjection` 身份保留。

外部 `invariants-before-commit.json` 记录原函数字节对比：将 helper 还原并去除仅新增的总 counter / 日志后，整个原 catalog 函数与基线逐字相同（SHA256 `ca4d13e93a7148e9e38542e5850a3ddfddeeda06fab0d9c119e92495833374f9`）；实际 definition 去除入口总数递增后也逐字相同。这覆盖原 prompt、模型选择调用、八分钟预算、全部原 if/fatal、schema、Revision 与返回语义。

本提交冻结时检查待执行。原 T15 四命令各自在最终 worker SHA 执行一次，另验 scoped vet 与离线新诊断及既有安全投影；固定 Go 1.27.1、private HOME、GOPROXY/GOWORK off、三门为 0、真实 CLI 前置 canary。最终检查、完整 diff 和身份散列写外部 `repairs/R049-T15-catalog-diagnostic/owner/`。root 负责 collector v6 闭集与 AUX 哈希匹配、独立复验和新 SHA 门禁。本 worker 不读取真实凭据或调用原 CLI，不以新增诊断关闭 catalog 的真实失败。

中央记录建议：CodeBuddy catalog live 夹具区分总回调与精确 VERIFY 回调次数，并对明确正式 hosted_echo 参数仅记录类型/精确匹配布尔事实；保持原模型、prompt、预算和成功判定，新增离线 canary 与实际 callback 计数控制。旧失败原因保持未知，后续真实证据单独验收。
