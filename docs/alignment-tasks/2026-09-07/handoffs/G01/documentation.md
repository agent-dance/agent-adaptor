# G01 集成文档与验收边界

本批 T01/T02/T03/T04/T05/T31 从同一 G00 SHA 独立实施，按 manifest 顺序合流；各 worker 的原始提交、返修报告和测试日志由外部 execution-state 追溯。

- T01：中央 streaming 与 CHANGELOG 同步一次性 stdin/result-only/root-nested 行为；常驻和完整输出合同保留。
- T02：tools/public-errors 同步 safe invalid_input、extra-key 边界、MCP 同一 Invoke 验证和 descriptive schema defaults。
- T03：a2a 与 CHANGELOG 同步历史快照、live/显式恢复、完整制品及冲突降级，RunError 主原因和允许暴露的 partial Result。
- T04：tools/API/public-errors 同步 Dedicated 持久目录、身份分区、所有权错误、离线恢复、Close 阶段及 unlock 后重试；x/sys 原版本改 direct 的依赖评估已记录。
- T05：根 godoc、全部 README、errors/API/streaming/policy/schema 与 AGENTS 同步 Driver entry 后 RunError/Cause、部分审计、schema 中断和已提交后 cleanup 失败。
- T31：schema/policy/API/CHANGELOG 同步逐机制有效 Ask、nil 兼容、资源前唯一协商；内置 Claude 能力仍由 T07 交付。

root AST 增量仅 RunError.Cause 和两种 Reason；SPI AST 增量仅三字段 StructuredOutputHITLCapability 与两个指针字段。没有旧 SDK/Start、模式选择器或根 With* 新入口。C01/C04 正文及 frozen hashes/ownership 同步 R003–R008，保留历史 G00 证据。

R003/W02-R06 最终动态资源快照、R006/W09-R14 所有运行的 core 终局、后续 provider 适配及独立跨层/平台/live 验收仍打开。T31-F01 与 T03-F01 经失败复现后返修关闭；T04 R007 两项有独立只读复审及本地 fixture，但 Windows cross-compile 不计原生验收。

实际 G01 最终 SHA、包校验、全量 Go 测试和 vet 的日志/计数在源码提交后的外部 result/evidence；不把 worker 测试当合流成功，不把 opt-in skip 当 live 通过。未推送、未创建 tag、未运行付费 provider。

G01 attempt 1 的全仓 E2E 发现旧 Dedicated source/删除/随机目录前提未同步（G01-F01）。R008 重开 T04 并扩充真实子进程的持久/临时两种夹具，新增必需完整 e2e 检查。原失败日志单独保留；只有返修后的合流 SHA 验证才能放行。

R008 返修已审阅合入：原生产实现不变；真实 MCP 子进程的 Dedicated/CloneFrom 三轮、实际会话文件与凭据拒绝均有覆盖。最终合流门禁另见 attempt 2 外部报告。
