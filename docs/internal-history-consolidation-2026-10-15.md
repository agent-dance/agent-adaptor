# Internal 历史对齐代码汇总（2026-10-15）

独立分支为 `codex/internal-history-consolidated-20261015`，综合基线为
`07f92fb7983b050904c7654aa58f716a8533c617`。在刷新 origin 后，盘点所有原有
139 个本地/远端分支引用、158 个登记 worktree、stash 和 20 个仅 reflog 可达提交。
[逐引用清单](alignment-tasks/2026-10-15/inventory.json) 保留完整 SHA、归属及合入依据。

## 覆盖结果

| 对齐相关引用 | 数量 | 纳入方式 |
|---|---:|---|
| 综合基线的祖先 | 48 | 已保留原提交 |
| 异 SHA 的相同补丁 | 42 | 对照 stable patch-id 与已合入提交，避免重复移植 |
| Windows/security 修复 | 16 | 综合提交 `06a1828` 及后续 R024–R026；补回本次发现的 Close 保护遗漏 |

以上 106 个引用覆盖原 W01–W13、9 月平台/live 返修至 R051、10 月 M01–M04
以及 CI01/CI02。最新合同 R053 和任务包 revision52 是本次汇总前的状态；
[R054](alignment-tasks/2026-09-07/amendments/R054.md) 记录本次恢复和精确范围。

33 个 worktree 目录仍存在，125 个目录已丢失；后者的管理 index 全部与各自
HEAD 一致。现存的对齐 detached HEAD 均已纳入综合基线，没有遗留的未提交
SDK 代码，也没有 stash。主工作区 56,602 项暂存内容保留原样：56,585 项运行
证据及 17 项历史验收、独立 DSH 设计/探针材料，不混入代码汇总。Cursor mlc 的
4 份旧设计文档与已合入 `3cd4149` 的 blob 相同。20 个 reflog-only 提交包括
16 个等价补丁、3 个已有后继的 rebase 版本，以及 1 个不属于本方案的旧 merge。

## 保留既有范围裁决

其余 31 个引用属于较早独立工作；另外两条旧分支 `6432e5e`、`12a1ad12`
由原方案第 7 节及 G00 frozen 合同明确要求单独协调、不自动合并。前者的旧
turn Usage 边界已由 T16 重做，后者不引回独立 ToolPolicy 公共入口。
R052 的 `1921636` 精确排除及 N08 未采纳决定保持；本次没有重新引入旧 SDK、
binding 或平行执行 API。汇总既有方案代码不等于重新迁移全部上游独立功能。

## 恢复的历史保护

`ff30d8c`（工作分支等价提交 `f2b858d`）在 Windows `File.Close` 删除提示文件
前验证 owner、DACL 和 link count。后续综合实现只保留了 `Verify` 路径的这些
检查，而 R026 明确要求原合同不变。本次恢复清理前的句柄身份和安全检查；
拒绝后不删除文件、不释放目录 pin，修正后允许重试。保留新版原子发布、
`RootDirectory=NULL` 同目录 rename、目录终生 pin、既有管理员 ACL 规则及
POSIX 行为，并恢复旧 POSIX 清理重试时拒绝目录替换的独立测试。新增代码没有
公共 API 或依赖变化。

## 验证边界

使用 `.github/go-version` 固定的 Go 1.27.1，关闭 live/E2E/golden 更新门，
在最终提交上执行全量 Go test、vet、任务包静态校验及其 31 项校验器测试。
Windows 专属新增断言需要原生 windows-latest 和 Windows Server 2022 执行；
交叉编译不能替代该证据。执行结果在受检提交外保存，不把源码提交自引用为
已经通过的 gate。

此前前端全依赖 high audit 和 G06/live 未通过记录保留。本次代码汇总与远端
推送不宣布通过新的 G05/G06，不创建 tag，也不表示已可发布。
