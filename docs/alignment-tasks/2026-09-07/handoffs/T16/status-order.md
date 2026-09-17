# Codex 0.153.4 早到状态控制

> 历史范围：下述早到 status 窄修及无 RPC 描述仅适用于 3dbabb34 / 948de5bf；后续正式 multiplex 修复的当前合同见 [multiplex.md](multiplex.md)。旧实现事实与外部红绿证据仍保留。

固定官方 rust-v0.153.4 / 3d2ee51ca2d5db578f328aa75e20aa22c0197c9a 中，app-server/src/lib.rs 的 thread_created 分支进入 thread_processor.rs::try_attach_thread_listener；后者先调用 ThreadWatchManager::upsert_thread 发布全连接状态广播，再挂接线程 listener。因此不能将 thread/started 先于 thread/status/changed 作为 SDK 的强制前提。

合法 foreign thread/status/changed 仅保留在 Raw，不登记 child、不绑定父/turn、不发布 Transcript、Usage、plan、capability 或终局。缺失/畸形状态和已知 child 身份冲突仍拒绝；其他 foreign thread/turn/item/error/usage/terminal fence 不变。Subagent 完成仍只由正式 child role、当前 spawn receiver 和唯一 resolved catalog 双证明支持。没有新增队列、状态表、RPC、等待或重放。

保留原全部状态边界例，unknown/overflow case 改为证明“控制可以 Raw-only、但不能授权身份”；新增公告前后/缺失、无 spawn、同 ID 后续语义、晚错父/冲突、真实 one-shot/resident stdio、父 terminal/checkpoint 与取消负控制。原失败和作者红绿证据在外部 final-a36291e/T29/status-order-repair；官方逐 blob 校验源码及设计在相邻 status-order-review。旧 a36291e 矩阵与后续诊断分别保留，不将本轮具体分量倒填原聚合失败。

本片段供 root 同步中央 R043、AGENTS、streaming 文档和 CHANGELOG。作者只运行离线检查；最终同 S 的 G05、B06 与 T29 原矩阵仍须独立验收。

## 原 observation 用例的真实工具前置条件

官方同版 config/src/config_toml.rs 的 ToolsToml.update_plan / UpdatePlanToolConfig.enabled，以及 core/src/config/mod.rs::resolve_update_plan_enabled 明确缺省为 false。tools/spec_plan.rs 仅在该值为 true 时注册 PlanHandler；真实 handler 执行后才发 PlanUpdate，app-server 再转换 turn/plan/updated。CLI 的全局 -c 支持 TOML 布尔点路径覆盖。

因此仅 TestAlignmentLiveObservationAndPersistent 的局部 Config.ExtraArgs 追加 `-c tools.update_plan.enabled=true`，保留已有 route 参数、原 prompt、全部联合断言、5 分钟 context、两轮同配置及单 spawn 要求。通用 gate、Driver 默认、其他用例和公共 API 均不启用工具。启用是测试前置条件，不表示实际调用或计划观测已经发生；必须由原正式 TodoUpdated 断言验收。

a36291e 后续限定诊断完整116帧未见 turn/plan/updated 且无 todo_invalid，只有 plan 谓词 false；此分量仅属于该次诊断，旧矩阵聚合失败原因不倒填。保持原失败，待新 S 原矩阵独立重验。
