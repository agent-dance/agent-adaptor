# T22 发现与证据边界

截至本次交付准备，未确认生产合同失败。测试仅在 ownership.allow 中实施，没有修复生产代码，没有删除 required 检查或增加 required skip。

## 原始 HTTP oracle 校准（非生产缺陷）

固定复现提交：`d3294085be18508a29bae75d37896a36c809605a`，前置 G04：`2421fe470cf67b22697fef796b038c0be6e395c8`。原红日志在 `evidence/original-http-oracle-red.jsonl`，相邻 `.meta.json` 记录真实命令/Go/OS/退出码与安全台架环境。

最初新增 oracle 将公开 CancelTask ack 等同于 executor drain 分类，要求 HTTP 最终 approval_denied；实际收到既有无 code canceled ack。最初还要求非法 Sequence 导致 client.ProtocolError；实际正式协议库将 executor translation error 投影为 failed Task/EOF。两项原断言均失败，原始日志保留。

协调者依据已存在的 `G04-composition-execution-clarifications.md` 校准此边界：HTTP 应验证 canceled ack/failed Task、无伪造 code/limit、真实第二次 Events 及完整 drain 后一次 Result、单次 Stream。Local 新测试独立核验取消 drain 的原 RunError/部分 Result/Cause；真实 HTTP 正常提示及 pre-Driver 父 cause 与本轮预算原因对照不变。内部 executor collector 分类引用 G04/T19 的固定验收证据，不计入 T22 新独立执行数。此校准不重开合同，也不暗改生产错误映射。

## T22-QA-F01：drain oracle 与 Result 同步缺口

独立 C02 审查固定 `cb4f44ef723eb30128cf4ed2235305e7acf12a87` 后指出测试 oracle 缺口，属于测试装置缺陷，不是生产失败。旧 done 仅说明 producer 将尾事件入缓冲并关闭 channel；未消费尾事件的错误 consumer 仍可满足原检查。公开 HTTP ACK/EOF 还可能先于 Result 进入，旧次数断言缺少 happens-before。

最小独立反例已先提交为 `1810fbdb386d9ab86d45332802e72b8905111835`：第一次 Events、取消、第二次 Events、完全不接收 tail 后调用 Result。`TestT22DrainOracleConsumption/consume=false` 实际红：buffered=1、Events=2、Result=1、err=context canceled；完整消费的正向对照通过。原日志为 `evidence/qa-f01-unread-tail-red.jsonl` 及相邻 metadata。

修正仅涉及 owned 装置：将 producer 信号明确命名为 tailPublished；Result 同时验证该信号及 len(es)==0，发现早读写入 atomic earlyResult，而不只返回可能被 ACK 隐藏的 error；另在 Result 检查完成后关闭 resultDone。HTTP 用已有有限 context 等 resultDone 后直接断言 earlyResult=false 和各调用次数。Local 也复用相同直接断言，保留原 partial/cause/分类检查。装置反向控制同时要求未消费被拒绝、完整消费被接受、producer 关闭时 resultDone 仍未关闭。公开 CancelTask 和翻译错误 wire 预期完全不变。

此修正补足此前阶段文档所称完整 drain 的证据缺口；原 cb4f44e 绿色不能作为修正后 oracle 通过。原返修已在 `66a86f345495f96109b52627f217989c115c9294` 完成有限定向/race 检查，日志单独保存；协调者逐行复核确认此 finding 可关闭。该旧阶段绿色不替代本次迁移后的完整原验证。

## 当前交付基线与门禁

2026-09-08，协调者已验收 canonical22 replacement G04 `b2035bc793369fb8fefb9de229ff1dbd2b748853`，并授权正式重派。T22 的 5 个 owned 阶段提交已从旧 `2421fe4` 无冲突 rebase；原始 SHA、红日志与阶段报告保留历史身份。最终源/本片段提交后执行完整两条原验证（count=1 和 race count=20，仅加 JSON，600s 进程外 timeout），正式结果只使用 `evidence/T22-V01.jsonl`、`evidence/T22-V02.jsonl` 及实际当前 HEAD。随后生成逐项 result/evidence，并使用 canonical validator、external execution-state 与 --verify-git 核验。本任务不修改外部状态，也不宣称批次或平台/live 门禁关闭。
