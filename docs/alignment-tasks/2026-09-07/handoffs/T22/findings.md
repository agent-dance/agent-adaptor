# T22 发现与证据边界

截至阶段交付，未确认生产合同失败。测试仅在 ownership.allow 中实施，没有修复生产代码，没有删除 required 检查或增加 required skip。

## 原始 HTTP oracle 校准（非生产缺陷）

固定复现提交：`d3294085be18508a29bae75d37896a36c809605a`，前置 G04：`2421fe470cf67b22697fef796b038c0be6e395c8`。原红日志在 `evidence/original-http-oracle-red.jsonl`，相邻 `.meta.json` 记录真实命令/Go/OS/退出码与安全台架环境。

最初新增 oracle 将公开 CancelTask ack 等同于 executor drain 分类，要求 HTTP 最终 approval_denied；实际收到既有无 code canceled ack。最初还要求非法 Sequence 导致 client.ProtocolError；实际正式协议库将 executor translation error 投影为 failed Task/EOF。两项原断言均失败，原始日志保留。

协调者依据已存在的 `G04-composition-execution-clarifications.md` 校准此边界：HTTP 应验证 canceled ack/failed Task、无伪造 code/limit、真实第二次 Events 及完整 drain 后一次 Result、单次 Stream。Local 新测试独立核验取消 drain 的原 RunError/部分 Result/Cause；真实 HTTP 正常提示及 pre-Driver 父 cause 与本轮预算原因对照不变。内部 executor collector 分类引用 G04/T19 的固定验收证据，不计入 T22 新独立执行数。此校准不重开合同，也不暗改生产错误映射。

## 阶段门禁状态

R017 重新打开 G04，阶段源提交和旧基线预验证不能冒充正式 T22 complete。待协调者派发新 G04 SHA 后迁移本任务提交，在最终已提交 HEAD 重跑两条原验证（尤其 race count=20），生成逐项 result/evidence，并使用 canonical validator、external execution-state 与 --verify-git 核验。本任务不修改外部状态。
