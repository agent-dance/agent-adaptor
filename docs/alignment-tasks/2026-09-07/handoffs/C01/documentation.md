# C01 文档合并片段

中央 owner：G00。合同为 `contracts/profile-lifecycle.md`；本提交只有合同/文档，不是运行时实现或发布。

## G00 当批应合并的文字

在 AGENTS §14 的关闭记录后重新打开 Thread/profile 兼容审计子项：

> W02 对齐审计已重新打开：Dedicated + hosted Tools 的真实 provider 会话文件需要持久保留；Agent 生命周期目录所有权须由跨进程句柄锁和 durable generation 共同证明。正常 Close 后可接力，active/unclean generation 不允许自动复用。物化指纹不得因临时 endpoint 轮换改变，也不得忽略未证明归属的配置漂移。实现与独立平台/live 验证由对齐任务 T04、T20、T26–T30 完成后关闭。

在文档地图/对齐记录链接 `contracts/profile-lifecycle.md` 并说明它是已冻结实施合同，当前功能状态由后续 task result 证明。不要在 G00 的 CHANGELOG 写“已修复跨 Agent 恢复”；本批可写“冻结 W02 生命周期合同，等待实现验收”。

## G01 在 T04 通过后应合并的公开语义

`docs/tools.md` 的 profile/生命周期段：

> 使用显式 `profile.Dedicated(dir)` 与构造期 `WithTools` 时，SDK 在 source 的 sibling 命名空间为 Driver 与完整 identity 保存私有执行 profile。正常 `Agent.Close` 撤销 hosted MCP/gateway 凭据并释放所有权，保留 provider 会话文件。Native、Default 与 Clone 选择的 hosted clone 仍为临时目录，并在 Close 清理。相同执行目录在一个 Agent 持有期间不允许另一个 Agent 使用，冲突可通过 `errors.Is(err, profile.ErrInUse)` 判断。Thread key 不参与目录命名。

紧接上段记录 source 要求与迁移：Dedicated source 必须已经存在；seed 只做一次，source settings 后改不会重新覆盖执行 clone。已删除 transcript 不能仅凭 Thread store 的 resume ID 恢复；旧记录只沿现有一次 resume-reject fallback 处理。无法证明资源所有权的物化变化会保守触发 Thread 不兼容，不能为冷续接省略 config/skills/MCP/instructions 指纹。

`docs/tools.md` 新增异常退出/离线维护段：

> OS 锁释放不证明孤儿 provider writer 退出。上一 generation 未 clean Close 时，SDK 返回 `profile.ErrRecoveryRequired`，保留 transcript 与原 state。宿主必须停用 namespace，确认并回收旧进程树，备份文件，再在独占所有权下按精确 MCP 归属规则去投影并将状态置 ready。不要按 PID/时间自行偷锁，也不要只删除 owner.lock。历史目录的最终保留和删除由宿主负责，Agent.Close 不删除持久 session。

`docs/api-reference.md` 的 Profile/Close 说明列出四个 `profile` 包错误：ErrInUse、ErrUnsafe、ErrRecoveryRequired、ErrUnsupportedFilesystem；不增加根包执行入口/With 选项。Close 任一 cleanup 错误都保持可重试，writer drain/回收成功后去投影、关 gateway，最后解锁。

`CHANGELOG.md` 的实际实现版本条目：Dedicated + hosted Tools 正常关闭后保留 provider session；强化跨进程目录所有权和安全失败边界；说明 source 必须存在、异常退出需离线确认和 Native/Clone 临时行为不变。不要宣称从旧版本已删除文件中自动恢复。

## 可复现例子与 godoc 落点

使用当前最终 API 创建 A：`adaptor.New(driver, adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithIdentity(identity), adaptor.WithTools(def), adaptor.WithThreadStore(store))`；运行 `A.Thread(key).Run(ctx, first)`，fixture provider 创建真实 session 文件，等待成功 `A.Close(ctx)`；以相同配置构造 B，运行 `B.Thread(key, adaptor.ResumeOnly()).Run(ctx, second)`。第二次 provider 必须读取首次文件的 nonce，不能只检查 Request.Session 非 nil。A/B gateway 的 URL/token/env carrier 必须不同。

局部 godoc 由 T04 写入 `profile/selection.go:Dedicated`、`profile/doc.go`、`profile/errors.go`、`agent.go:Close`。四个 sentinel 属于 profile 词汇，不新增 root/SPI 声明，因此 root/SPI AST golden 保持不变；新增 profile 公共错误合同测试即可。x/sys 保持 v0.41.0，仅转 direct；依赖三项评估见合同 §4。

## 不采用的 internal 行为与证据边界

不采用 `<profile>.hosted-tools` 目录存在即信任、没有 identity 命名空间/跨进程锁的方案；不复活 SDK、SessionKey 或默认 binding；不把删除函数扩成任意路径；不把 `Request.Session` 存在当作真实 CLI 可恢复证据。

C01 在 macOS 运行任务包静态校验并审阅确切签名、marker schema、状态机和 fixture 的一致性。没有执行生产变更测试、Linux/race、原生 Windows ACL/进程测试或 paid live。Windows syscall 官方文档及固定 flock 源码只用于合同选型，不作为运行证据。T04 后续普通验证保持 live/E2E/golden 更新门为 0，原生平台和真实 CLI 验证由原任务 owner 按双门执行。
