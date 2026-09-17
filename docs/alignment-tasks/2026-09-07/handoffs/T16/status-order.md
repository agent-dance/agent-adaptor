# Codex 0.153.4 早到状态控制

固定官方 rust-v0.153.4 / 3d2ee51ca2d5db578f328aa75e20aa22c0197c9a 中，app-server/src/lib.rs 的 thread_created 分支进入 thread_processor.rs::try_attach_thread_listener；后者先调用 ThreadWatchManager::upsert_thread 发布全连接状态广播，再挂接线程 listener。因此不能将 thread/started 先于 thread/status/changed 作为 SDK 的强制前提。

合法 foreign thread/status/changed 仅保留在 Raw，不登记 child、不绑定父/turn、不发布 Transcript、Usage、plan、capability 或终局。缺失/畸形状态和已知 child 身份冲突仍拒绝；其他 foreign thread/turn/item/error/usage/terminal fence 不变。Subagent 完成仍只由正式 child role、当前 spawn receiver 和唯一 resolved catalog 双证明支持。没有新增队列、状态表、RPC、等待或重放。

保留原全部状态边界例，unknown/overflow case 改为证明“控制可以 Raw-only、但不能授权身份”；新增公告前后/缺失、无 spawn、同 ID 后续语义、晚错父/冲突、真实 one-shot/resident stdio、父 terminal/checkpoint 与取消负控制。原失败和作者红绿证据在外部 final-a36291e/T29/status-order-repair；官方逐 blob 校验源码及设计在相邻 status-order-review。旧 a36291e 矩阵与后续诊断分别保留，不将本轮具体分量倒填原聚合失败。

本片段供 root 同步中央 R043、AGENTS、streaming 文档和 CHANGELOG。作者只运行离线检查；最终同 S 的 G05、B06 与 T29 原矩阵仍须独立验收。
