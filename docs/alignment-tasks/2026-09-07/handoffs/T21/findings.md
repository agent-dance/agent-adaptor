# T21 独立生产发现

本文件记录固定旧 G04 base `2421fe470cf67b22697fef796b038c0be6e395c8` 上的独立发现。F01/F02 的 QA 复现代码提交 `b2f7b9293906b9feca27806b61fb0ebabc89eb13`；F03 首次完整 race 红日志对应 `8b52ce06bdd34eeff1315cc3e897db950ed000a5`。均只增加 T21 测试和私有 fixture，无生产修改。三项已于 2026-09-07 交 root；修复重放前保持 open，不把其他绿项视为关闭证明。

## T21-F01 — CodeBuddy 增量工具起始同时携带占位 Args

- 归属：T15 / W09-R07；影响公开 ToolCall 的增量参数合同。
- 输入：正式 `system.init` → `stream_event.message_start` → MCP `content_block_start`，其中 `input:{}` → 两条 `input_json_delta` 分别为 `{"query":` 和 `"PRIVATE"}` → `content_block_stop` → 同 ID 完整 assistant wrapper → 同 ID 两条成功 tool_result 重放 → 正式成功 result。MCP catalog 明确声明 Unicode canonical key `知识_库`。
- C03 §1：增量 start 的 Args=nil，只发布每条真实 ArgsDelta，完整 wrapper 不重新 start 或重复参数。
- 期望：start/end/result 各一次；Args=nil；合并 delta 精确为 `{"query":"PRIVATE"}`；Capability Started/Completed 各一次。
- 实际：CodeBuddy `ToolCall.Args` 为 `map[input:map[]]`；delta 文本正确。相同 hand-written bytes 走 Claude 正式 parser 对照通过。
- 定位：`codebuddy/streaming_parser.go` 的 `handleContentBlockStart` 将空 `input` 包在 `Args["input"]` 中。
- 不变复现选择器：`TestAlignmentProtocolPartialWrappers/codebuddy/abnormal=false`。
- 协议字节：`evidence/F01-success-protocol.jsonl`，SHA256 `dfad5ed8857c5e46300bfeeb309d6d34f453178bdea054caba68fe38f41c70c8`。

## T21-F02 — CodeBuddy 正式 error 后丢失未闭合 Capability 终态

- 归属：T15 / W09-R07；影响关键 Capability 生命周期完整性。
- 输入：与 F01 相同已确认 MCP start/完整 wrapper，省去 tool_result，直接给正式 `{"type":"error","message":"T21_FORMAL_FAILURE"}`。
- C03 §3、§10：没有工具结果的异常收尾为 Interrupted/RunInterrupted；不能只有 Started。明确工具失败可 Failed，不能因 run 终局代替工具终态。
- 期望：同一真实 parser/Agent Stream 交付 Started 和 Interrupted 两条 Capability；无伪造 ToolResult；同一错误 Result 保留正式 error terminal/部分审计，core 唯一末尾 RunFinished Failed。
- 实际：只交付一条 Started Capability。Claude 同样 bytes 的异常对照通过。
- 定位：`codebuddy/parser.go` completeStream 调用 closeObservations；`codebuddy/streaming_parser.go` 早先 error terminal 已置 finishedEmitted，后续 emitStream 拒绝收尾事实。
- 不变复现选择器：`TestAlignmentProtocolPartialWrappers/codebuddy/abnormal=true`。
- 协议字节：`evidence/F02-error-protocol.jsonl`，SHA256 `7860d1a0f006409d247cf8bf21bf8c79aa0edee733afd428f5f6da56a6b1a8bb`。

## T21-F03 — AG-UI 已发布工具快照仍被后续翻译修改

- 归属：`bridges/agui/subagent.go`；跨层 W05-R05 验证失败。
- 固定复现：`8b52ce06bdd34eeff1315cc3e897db950ed000a5`，完整原 V02 加 `-json`：`go test -race -count=20 . -run TestAlignmentProtocol -json`，进程外 900 秒 watchdog，未触发超时。
- 输入链：手写正式 Claude nested tool/wrapper bytes → 真实 Claude Driver parser → 公共 Agent → Service Local → 实际 parent Agent 的 SubagentUpdate → 公开 `agui.Events`。消费者收到输出后只调用标准库 `json.Marshal`，没有修改输入或输出值。
- C03 §1 要求 map/slice/Args/Result 递归深复制，AGENTS §7/§11 要求稳定事件与协议保真。期望已经发布的活动 patch 保持该时刻的工具状态，正常序列化无需与内部 translator 协調。
- 实际：`subagentActivityState.apply` 把 `s.content.ToolCalls` 直接作为 JSONPatchOperation.Value 返回；下一条 args/result/end 修改同一 backing slice 中的 `Args`/`Result`/`Status`。首次 race 读栈为 root QA 的 `apJSON:58 → apOtherBridges:713 → ServiceRelay:922`，写栈为 `bridges/agui/subagent.go:164 → :82 → events.go:335/:224/:137`。同一完整日志还记录 map 内容的读写竞争。
- 首次完整 race 块：`evidence/F03-AGUI-first-race.txt`，SHA256 `c1f59f6aa9715e96bb9d32f61caac615422d358bd0a48c3fff6b1b81206c8ada`。完整 V02 日志：`evidence/oldbase-V02.jsonl`，SHA256 `351711959cec836f76c5d264a2213527f96d1f3f2acfd84e00740eec454ebe90`。
- 后续 QA 在原 `agui.Events` 消费检查之后追加同步 snapshot 检查：同一批真实 core 事件逐条进入公开 EventTranslator，保存每个输出及当时的 JSON，全部翻译/CloseResult 后再比较。此断言不制造 core 事件，不串行化或移除原异步消费者，不靠 race 调度证明快照不变。

## 原不变 fixture 的后续 CodeBuddy 重放问题

T15 owner 在修复 F01/F02 后报告：同一个 normal fixture 中两份精确相同的 `tool_result` 仍产生两条 typed ToolResult；root 已授权它在 CodeBuddy 范围修复。该控制是本任务最初的明确意图：同 `(scope,id)` 与同完整 payload 重放只发一条 typed result，Raw/Transcript 保留两份原文。`TestAlignmentProtocolPartialWrappers` 的输入和 `results == 1` 原断言不变。本任务尚未在 root 验收的修复合流 SHA 上重放；不把 owner 报告计为新增独立通过数，也不把它冒称当前旧树中越过 F01 早期失败的新红结果。

## 旧 base 的完整预验证

`8b52ce06bdd34eeff1315cc3e897db950ed000a5` 的原完整 V01/V02 均实际执行且无 skip、无 timeout：

| 原检查（实际均仅追加 -json） | Exit | Pass / Fail / Skip（含 parent/subtest） | Leaf pass / fail / skip |
|---|---:|---:|---:|
| `go test -count=1 . -run TestAlignmentProtocol` | 1 | 189 / 3 / 0 | 172 / 2 / 0 |
| `go test -race -count=20 . -run TestAlignmentProtocol` | 1 | 3778 / 62 / 0 | 3439 / 41 / 0 |

V01 完成 192 项；V02 完成 3840 项（20 轮），不是 3840 个不同测试。V01 仅 CodeBuddy 两个子项及 parent 失败；V02 另有 ServiceRelay/local 及 parent 的 race 失败。V01 日志 SHA256 `f6509f0a37f32c9063bbcbb88d4753155eb1647537a287aef60e45870d009ab4`。完整 argv、私有环境、OS/Go、耗时、计数和日志 hash 在 `evidence/oldbase-V01-command.json` 与 `evidence/oldbase-V02-command.json`。这些结果只证明所列固定旧 SHA；追加同步断言与本文件后必须重新执行，不能移用作最终 HEAD 证据。

## 固定复现、环境及日志

完整真实 argv：`go test -count=1 -timeout=120s . -run TestAlignmentProtocolPartialWrappers`。这是旧 base 的补充预验证，包含 Go timeout，不能冒充最终 task 原 V01/V02。退出码 1；Claude success/error 两个子项通过，CodeBuddy 两个子项失败，无 skip。

私有 HOME/USERPROFILE `/private/tmp/agent-adaptor-alignment-20260907/t21-home`，XDG_CONFIG_HOME 为其 `/config`；PATH 前置 `/Users/blurooo/.xvm/sdk/go/1.26.5/bin`，GOROOT 同真实 1.26.5，GOTOOLCHAIN=local，GOCACHE `/private/tmp/agent-adaptor-alignment-20260907/go-cache`，GOMODCACHE `/Users/blurooo/go/pkg/mod`，GOPROXY=off。移除 CODEX_HOME/CLAUDE_CONFIG_DIR/CODEBUDDY_CONFIG_DIR/CURSOR_HOME；LIVE_CONFORMANCE/E2E/UPDATE_API_GOLDEN 三门全部 0。provider Command 仅指向本测试 binary 的显式 fixture init，子进程有 t.TempDir HOME，不调用真实 CLI、不使用凭据。

原红日志复制自 `/private/tmp/t21-dev-83262.log` 到 `evidence/F01-F02-fixed-b2f7b929-red.log`，SHA256 `f9280cf25c73831a2e62e742d8c6f60642c5b57a39bb4257e3a77bd95add9f92`。协议文件是对固定提交中 literal/builders 的精确 UTF-8 字节展开，JSON field 排序遵循标准库 json.Marshal；不由生产编码 helper 产生预期。

## 已排除的台架误用

- HTTP 请求 context 取消不等于服务器执行 context 取消。原台架 red 保存在 `evidence/public-http-request-context-cancel-original-red.log`。root 已确认公开 Handler 无 executor 入口且执行与连接分离；现在通过真实 CancelTask 触发 Cancel/drain，校验唯一 Stream、完全 drain 后唯一 Result、幂等 Cancel、无伪造 limit。CancelTask ack 与 executor 最后分类不同，不能用 ack 证明私有收集器分类。
- JSONL 重开将通用 JSON Data 内 []string 读成 []any，比较应按 JSON 语义；新 typed Capability/Todo 与完整 EventMeta 仍须 DeepEqual。不是持久化丢失。
- CodeBuddy TodoWrite 正式确认消息需 `Todo list updated successfully`；任意 `tool reply` 不证明 Todo 成功。fixture 已使用正式确认。
- Codex 外来 thread/turn notification 必须 fail closed；不能混入成功 fixture 后要求整轮成功。保留独立负向。
- strict decoder 接收 raw bytes 时检验 duplicate key/UTF-8/surrogate；已解码 map 无法恢复原始重复 key，C03 已明确此边界。网络结构负向仍通过真实 HTTP/delegator 检验。
