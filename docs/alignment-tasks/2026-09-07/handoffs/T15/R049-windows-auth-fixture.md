# R049：Windows 原生认证夹具的普通目录读取

基线 `8deec330aeb1be94e78b7b48a034ee7968d7f64f` 的两个原生 Windows full job 均在 R048 夹具的 native/legacy profile 隔离复合断言失败。旧日志没有 `os.ReadDir` 的错误及条目数；只能确认断言失败，不能倒填其具体分量或错误码。原始日志及来源散列保存在外部 `repairs/R049-T15-windows-auth-fixture/original/`。

Windows 创建目录的长期句柄原来请求 `FILE_GENERIC_READ | DELETE`，共享模式为 `FILE_SHARE_READ | FILE_SHARE_WRITE`。固定 Go 1.27.1 的普通 `syscall.Open` 也只共享 READ/WRITE。微软的 [CreateFileW 合同](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew) 要求新共享模式与已存在句柄的访问权兼容：既有 DELETE 访问权会与不共享 DELETE 的普通读取冲突。这是独立机制分析，不是旧 run 的内部轨迹。

最小修复只移除目录创建句柄请求中的 DELETE 权限。共享模式仍然不含 FILE_SHARE_DELETE，目录生命周期 pin 持续持有；创建时 protected DACL、owner/reparse 验证和文件创建语义不变。合法凭据刷新仍在初始文件句柄关闭后进行，来源与副本的隔离、关闭后的清理合同不变。

新增普通 Windows 测试分别以有/无 DELETE 访问权打开测试目录，再通过普通 `os.ReadDir` 验证精确 sharing violation / 成功与条目数，记录安全 errno，并在两种持有形态下都要求重命名失败；关闭后读取恢复。另一个测试在真实私有夹具的全部 pin 仍持有时，普通读取 HOME、profile 和 native auth 目录，并要求重命名仍失败。原三处隔离断言继续同时要求读取成功和精确条目数，只补充错误及数量诊断。

三个原 live wrapper 调用点所运行的私有 fake CLI 也执行普通 `os.ReadDir` 读取 profile/native auth 目录；其成功同时要求精确凭据字节、HOME 一致与两个目录可读。没有把原 oracle 换成绕过共享限制的特殊读取方法。全部原 DACL、凭据刷新、来源不变与清理断言保留，未更改生产 SDK、模型、prompt、live 预算或付费选择器。

本提交冻结时尚待运行原四条命令与 scoped vet；最终结果写外部 owner 报告，以真实提交 SHA 为准：

```text
go test -count=1 ./codebuddy
go test -race -count=5 ./codebuddy -run TestAlignment
go test -count=1 ./internal/profileagents
go test -race -count=10 ./internal/profileagents
go vet ./codebuddy ./internal/profileagents
```

另验新夹具 tagged fake race 与 Windows 编译。固定 Go 1.27.1、私有 HOME、GOPROXY/GOWORK off、三 live/golden 门为 0、真实 CLI 前置 canary。测试补丁的修复前快照保留，但不冒充已执行的 Windows red。root 在合流新 SHA 的两个原 full Windows job 中执行受控机制反例与真实夹具回归；交叉编译及 macOS 测试不能替代该原生证据。root 继续负责独立审查、中央记录及全部新 SHA 门禁。

中央 CHANGELOG 建议：修复 CodeBuddy Windows live 认证测试夹具的目录句柄访问权，使普通目录读取与禁止重命名的生命周期 pin 同时成立；保持创建时私有 DACL、原生凭据刷新和清理，新增原生 Windows 共享模式控制测试及真实 wrapper fake CLI 普通读取回归。旧 Windows 复合断言失败与新机制实验分别留档。
