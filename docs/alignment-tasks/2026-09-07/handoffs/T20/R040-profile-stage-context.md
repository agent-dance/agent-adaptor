# R040 / CI-WIN-F03：profile 场景的同步阶段预算

本次基于 `0c45255c96904847a52ff5baa3ff3854f91f74f6`，写域仅为
`e2e/alignment_profile_test.go` 和本说明。没有公共 API、SDK timeout、全局
`alignmentContext` / `alignmentWaitLimit`、fixture 子进程协议或 CI runner 改动。

## 原失败及诊断边界

保留冻结源码 `dab1a24` 的 Windows Server 2025 完整 Go job：run
`35177544464` / job `105062454539`，原命令
`go test -count=1 -timeout=15m -v ./...`。原日志 SHA256 为
`f1d5517db9148c19826a650ae2302c2978c6e272a7e9072cda40b99796b45007`。
Ownership 在 owner、同进程冲突和子进程冲突之后的不同身份 Run 报 deadline；
IdentityEncoding 在七个身份之一的 Run 报 deadline，原输出不能定位其索引。
原完整诊断位于外部执行目录 `live-repair/diagnostics/T20-windows-dab1a24`。

两场景原来从开始到结束共用一个 8s context。这是测试夹具的工程预算，
不是任何公开产品操作时限。专门 T26 job 在另一 Windows 镜像上的绿色结果
不能覆盖原完整 Go job 失败，也不能据此确定 CPU、磁盘、杀毒或路径变换等慢因。

## 修复与保持的断言

两场景现在同步执行每个阶段，并为阶段建立至多 8s 的子 context；不自动重试。
Ownership 的七个阶段为 owner Run、同进程冲突、子进程冲突、不同身份 Run、
owner Close、successor Run、predecessor 幂等 Close。父 context 的 56s 总预算
仅请求协作取消；现有 contender 自己的 4s context 和 Close 辅助函数边界保持。

IdentityEncoding 的父协作预算是 `(7+1)*8s = 64s`：七次不同身份 Run 和一次
canonical source alias 冲突。七个 Agent 仍由测试 cleanup 统一回收，在循环和
alias 检查之间没有 Close，全部 claim 的并存语义保持。

同进程/子进程 `ErrInUse`、不向 provider 重放、完整 identity 目录隔离、POSIX
权限、successor 成功及旧 owner 不改 successor state、七身份无碰撞和 alias
拒绝的原断言全部保留。阶段 context 已取消时，即使操作还返回 `ErrInUse`，
阶段也报告 context 错误，不能把超时误判成期望冲突。

阶段日志只记录固定阶段名（身份阶段使用索引）、起始剩余预算、耗时、context
状态、error 类型与 RunError reason，不增加路径、token 或身份内容输出。
阶段 wrapper 没有 goroutine，也不会在操作尚未返回时遗弃它。父/子 context
均不构成 hard kill；原 `go test -timeout` 以及外部 CI 监督继续提供测试进程
边界。其他测试及现有全局 helper 的 8s 预算不变。

## 可控反例与验证边界

新增 `TestAlignmentLifecycleProfileStageBudgets` 使用 `testing/synctest`
虚拟时间和公开 Agent/Driver 调用边界。独立固定预期为：

- 两次各耗时 5s，原共享 8s 预算：第一轮成功、第二轮 deadline，总虚拟时间 8s。
- 两次各耗时 5s，独立阶段 8s 预算：两轮都成功、共 10s，完成计数恰为 2。
- 单次耗时 9s，独立阶段 8s 预算：真实 `context.DeadlineExceeded` 与公开
  `RunError.ReasonDeadlineExceeded`，8s 返回且完成计数为 0。

没有真实 sleep、真实 provider 或 Windows 仿真。该控制验证预算的因果，
不声称精确重现原 Windows 调度轨迹，也不以测试 helper 的字段值充当 oracle。
把独立阶段控制的父预算恢复为旧 8s 后，未改变成功断言的同一测试确实在
第二轮失败：外部 `shared-budget-red.jsonl`（exit 1，两个 fail 身份，包括父）
SHA256 `6e36b59ba7e15a19507c805b33c5ecb1ceeb05d9ff06efba27e597dfa099a680`。
对应 patch 与原文件快照一并保留；修复预检为两原场景和时序控制共六个 PASS，
零 skip。此预检不替代最终提交上的正式检查。

最终提交后的原 T20 三条命令、两原场景重复/race、整 e2e 与编译检查的命令、
环境、源码 SHA、结果和日志 hash 写入外部 `live-repair/repairs/R040-T20`。
本机是 Darwin；最终新 S 的原生 Windows 完整 job 与原重复命令仍须 root 验收。

公开语义、godoc、使用文档和 root API golden 均无变化。中央文档负责人可将
本说明的工程预算与平台边界纳入 R040 关闭记录，不应描述为生产 timeout 放宽。
