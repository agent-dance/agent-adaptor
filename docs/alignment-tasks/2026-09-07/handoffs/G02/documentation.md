# G02 集中合同与文档整合

T06/T07/T08/T09 从同一 G01 0adb8355378b0d1c0457119f96da470f4c33366a 独立实施，经原 owner 返修及独立复审后按 manifest 顺序合流。外部 execution-state 保存所有原报告、失败日志和最终源码证据。

- T06：streaming/API/Driver合同、tools、doc.go、AGENTS、CHANGELOG 同步 Capability/Todo、parent/source、RunAttachment observer/publisher、R009一次候选协商、每次准入 core 生命周期、完整 Dropped 和最终 resolved profile snapshot。静态拒绝与准入后失败区别明确；PruneManaged/PruneBrokenManaged遵循正式writer，不抹掉无证明copied tree或IO错误。Usage godoc与至少一个有效观察的类型语义一致。
- T07：structured-output/run-policy/streaming/API/errors/CHANGELOG 同步 native Question/PlanReview 与 Prompt Permission、effective默认Ask/raw交互激活区别、临时native进程/WithSpawn、DecisionSink中止回收、partial短写排空、消息累计/合法零/未知用量与原Wait cause。
- T08：streaming/API/errors/CHANGELOG 同步 CodeBuddy部分结果、完整诊断尾部、真实ExitError与主因、message ID累计/缓存字段兼容，以及失败checkpoint不升级。
- T09：a2a/CHANGELOG 同步 opt-in实时Parts和Append/LastChunk、默认Raw收紧、累计metadata/Raw字节边界、artifact_invalid/count-limit安全诊断、独立嵌套复制。

逐声明审阅 root/Driver golden：只有 C03 叶词汇与typed Event、parent/source、ObservationDemand/Capabilities、RunEventInfo/Observer/Publisher/RunAttachment、两个SPI事件kind；无内部类型泄露、无新增执行入口/With名称。T07/T08无公共声明，T09仅leaf DTO Parts/Append/LastChunk。没有新顶层依赖。C01/C03/C04冻结hash和ownership更新，R009/R010/R011所有旧要求ID保留。

独立复审关闭T07-F01/F02、T08-F01、T06事件三项和动态profile/prune缺陷；具体测试SHA分列在外部报告，未用较早SHA复审冒充最终SHA全量验证。集中正文复审修正旧partial拒绝、Claude Ask漏项、无条件blocking不丢/codec lossless、schema与transport fingerprint及Usage每字段presence等矛盾。G02首次全仓仍发现MCP writer改变0600权限及Streaming布尔造成Thread重绑；R011纠正该合同，保留真实mode/配置/codec/资源guard，并要求原Codex复用及CodeBuddy三启动prewarm断言通过。首次675485d的失败与未执行vet保留，不能充当放行证据。

B03–B06、独立跨层、原生Windows/Linux和真实provider/live仍由原ID执行；当前没有将新Event词汇当作已实现所有provider/wire识别的证据。G02检查必须在本集中文档/source最终commit后执行；本源码片段不声称未来检查已通过。无push、tag、发布或付费调用。

R011 最终 T06 913673a 已完成全部同SHA任务检查，保留原1/3次启动断言；独立profile复核覆盖e53生产逻辑120项及913平台增量78项，CodeBuddy独立e53日志验证同会话、每prompt一次与实际预热PID复用。913未改变该transport逻辑，未把e53证据冒充913执行。原生Windows尚待T26；G02第二次整仓门禁仍在本commit后执行。
