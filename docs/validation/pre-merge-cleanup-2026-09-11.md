# 合入 main 前的可维护性整理（2026-09-11）

本轮依据用户“质量不降低、功能不损失”的要求审查整个分支，基准为 `origin/main` 的 `919f140f64f89c80933802840c8878681a85a4d9`，整理前已验收版本为 `dd3859e4e9ee5d8b36556731c054280a384b0d2c`。未使用本地 main 的较小差异替代完整审查。

## 范围与判断

分支涉及 516 个文件，其中非 `_test.go` 的 Go 文件 165 个。按互斥目录逐个检查实际 diff、必要上下文及相关反例：

| 范围 | 文件数 | 主要处理 |
| --- | ---: | --- |
| 根包 | 16 | 拆分混合职责，统一拒绝清理与通用错误分类，保留单一执行管线 |
| Claude / CodeBuddy / Cursor | 25 | 合并同生命周期的并行状态表，保留正式协议各自的解析责任 |
| Codex / adaptertest / Driver SPI / 叶词汇 | 38 | 统一 turn/start 参数映射，MCP 表归属 observations；其中 2 个为 testdata 可执行夹具 |
| bridges / A2A client / hosttools | 46 | 去除不可达终局映射，复用已有制品过滤；消除 recorder 重复解码与复制 |
| internal | 35 | 统一 capability 错误码闭集，展开验证条件，以类型化比较替代 Todo 反射 |
| examples | 5 | 保留消费者行为、离线演示及真实进程验证入口 |

另检查全部 7 个 `.github/scripts` 文件；collector、原命令、超时、fuzz 时长、独立反例及允许 skip 的闭集均不改动。不是所有文件都需要修改；保留项及理由见本轮外部逐文件审查记录。

## 代码整理

### Core

- `sink.go` 保留 Event 交付和生命周期信封；审批调度、观测、终局判断分别移至 `sink_approval.go`、`sink_observation.go`、`sink_outcome.go`。原 receiver、锁、字段、调用次序不变。
- `tools.go` 保留 Tool 能力构造与 runtime 适配；目录所有权、清理与 materialization 放入 `tools_profile.go`；兼容快照、资源字节和 MCP 归一化放入 `tools_profile_snapshot.go`。
- `invocation.go` 保留唯一 Driver.Run、Thread Persist、结果及 teardown 管线；兼容性计算移至 `invocation_fingerprint.go`。runtime service 排序对每项只计算一次 hash，避免比较器反复序列化同一 metadata。1120 组完整旧/新指纹对照覆盖空值、单项、重复、乱序及 metadata，结果相同。
- `openStream` 的九处准入拒绝使用同一个局部清理函数，保留原校验顺序、错误包装和当前 cancellation 函数。拒绝仍返回空的 closed Events。
- 终局选择和 Result finalization 复用相同的通用错误分类。provider、审批、Thread coordination 及已选主因的优先级仍在原位置处理。
- Tool profile 去掉同一锁内的第二次相同 map 登记；第一次登记仍在可能失败的清理之前。配置规范化只适用于正式 provider 配置路径，skill 附件保持原字节。

### Providers 与内部状态

- Claude 的 Tool 指针/replay、CodeBuddy 的 ID/name/初始参数/增量参数/归属阻断、Cursor 的引用/起始 hash，各自由一个记录维护，减少六张需要同步增删的状态表。其他不同生命周期的表保留。
- CodeBuddy retry terminal 后仍可 drain 未绑定参数；清理用 `clear` 保留可写 map。新增跨 scope、block 交错、index 重用、重试后迟到参数测试先在旧实现通过，再验证整理后的实现。
- Codex one-shot 与 resident 共用纯 turn/start 参数构造，保持独立 input/schema 副本和原省略规则；RPC reader、promptSent、Close、EOF、Wait、Raw drain 均保持原位。
- `capabilityobs` 的错误码闭集只有一份；父域验证改为易读的分步判断。Todo 在原 non-nil 归一化之后使用 `slices.Equal`，保留顺序与空快照合同。

### Hosttools

- Delegation 中只登记稍后取消的方法改名为 `scheduleRemoteCancel`，去掉两个无用参数；成功和中断复用既有部分制品过滤、事件发布函数。
- 删除没有生产调用者的 `terminalEvents` / `terminalTaskText`。原测试的 JSON 正文 fixture 迁到公开 `Delegate` 的 polling 和 streaming 路径，检查最终 Summary/Messages、task/context、一次执行与唯一完成事件。没有新增终局字段语义。
- Session recorder 对每层必填对象只解码一次并复用验证结果。历史审批从描述字段构造后做一次正式深复制；仍移除 responder，保留各所有权边界的独立副本。

## 有意保留的复杂度

- Windows 创建时 DACL、owner 验证、禁止 delete-sharing 的长期 pin、原句柄发布与失败重试解决不同安全边界，不能用路径 rename 或模式位检查替代。
- one-shot/resident、正常退出/取消、收到终局/收到 EOF/实际 Wait 的时间关系不同，不能统一成一个无条件关闭函数。
- 预算 first-cause、Ask pause token、原子 Persist、清理后错误映射以及 Run/Stream 完整审计保持原合同。
- provider parser、bridge wire validator 和 adaptertest 是不同责任与独立验证方，不能共享一个宽松解析器来减少行数。
- 不新增公共 API、依赖、配置开关、兼容 shim、执行入口或事件通道，不改 generated 协议与 API golden。

## 验证与验收记录

本轮按声明比较旧/新根包 token，忽略文件位置、注释与纯空白，确认 496 个声明不变、没有声明遗漏；显式改动逐项独立复核。该检查只证明移动范围，不能替代行为测试。

各目录整理前/后测试、相关 race、provider 原 parser fuzz（各 30 秒）和新增边界反例均通过。合流后的完整 `go test -count=1 ./...`、`go vet ./...`、`go test -race -count=1 -timeout=20m ./...` 在本机 Go 1.27.1 下均通过。Test/Fuzz 函数清单由 1672 增至 1675：原死 helper 测试迁移后改名，新增 3 个边界测试，全部 fuzz 保留；公共 API golden 字节未变。合流源码使用 tracked HEAD archive 加明确新增/修改文件构建隔离快照，避免用户未跟踪的审计 `.go` 文件被架构守卫当成产品；不修改用户文件或放宽守卫。

独立跨代码验证使用 `dd3859e` 的真实 `Agent.Thread.Run` 保存 0/1/2/8/32 服务的五组 Thread record；整理后的代码装入旧 record，反序服务并轮换 secret 值，以 `ResumeOnly` 成功复用原 resumeID，record ID 与两种 fingerprint 逐字不变。

本机单线程合成 benchmark（三次中位，每个服务同时含 requested/ensured 描述及 8 项 metadata）中，完整 Thread 指纹的 8 服务用例由 1.603ms 降至 0.713ms，32 服务由 12.088ms 降至 2.919ms；0/1 项没有额外分配，2 项时间约增加 4.6%。这只衡量指纹计算，不代表整个 Agent 调用提速。

本报告不预先宣称最终 SHA 的 CI 通过。最终提交须重新执行完整 test/vet、Linux race、九个原时长 fuzz、最低 Go 版本、frontend/security，以及 Windows latest/Server 2022 和同 SHA T25/T26。结果与 source/artifact hash 由协调者保存在外部 `docs/alignment-execution/2026-09-11/pre-merge-cleanup/`，旧失败及旧验收记录保留。

R029 只扩充精确整理路径和再验收要求，47 个任务、96 个工作要求、原 owner/verifier 及 live 双门不变。T27–T30 的真实 provider 环境/授权未满足，G06 仍不能通过；本次不合并 main、不创建发布 tag。
