# T13：可选 capabilityrecorder 与宿主查询

范围：W09-R04、W09-R05；依赖 G02 的 observer/demand 与 C03 第 6、9 节。交付仅覆盖 recorder，不宣告四个 Driver 正式协议观测或整个 W09 完成。

## 公开语义及可复现用法

新增 `hosttools/capabilityrecorder`。宿主显式提供 Store，以 `Recorder.Option()` 注入 Agent 默认值或单次 Run/Stream；组件使用已有 RunServiceProvider observer，声明 CapabilityInvocations demand。不存在 root 查询入口、第二执行管线或事件采集 channel。

```go
store := capabilityrecorder.NewMemoryStore()
recorder, err := capabilityrecorder.New(capabilityrecorder.Config{Store: store})
if err != nil {
    return err
}
agent := adaptor.New(configuredDriver,
    adaptor.WithIdentity(adaptor.Identity{ID: "assistant", Tenant: "tenant", Profile: "user"}),
    recorder.Option(),
)
stream := agent.Stream(ctx, "work")
page, err := recorder.Query(ctx, capabilityrecorder.Query{
    Scope: capabilityrecorder.Scope{
        IdentityID: "assistant", Tenant: "tenant", Profile: "user", RunID: stream.RunID(),
    },
    Limit: 100,
})
```

运行仍可能进行，首次 Query 可以为空；示例的 `configuredDriver` 是宿主已有 Driver。查询只返回实际已接受并成功 Append 的 CapabilityInvocation，不存 Todo、Thread key、显示名、来源 envelope、prompt、args、result、Raw、URL、header、env 或错误正文。调用方仍按既有合同消费/取消 Stream；阻塞模式用户不 drain 会阻塞后续执行，但不阻止查询此前已接受的事实。`TestRecorderLiveQueryBeforeUserDrain` 用公开 Runner 与 fixture Driver 可重复验证 Run、Stream、blocking Stream。

Scope 四字段逐字比较；三个 identity 维度可为空，RunID 必须非空。内部用结构体 key，不拼接分隔符。Query 不支持全库或跨 identity 扫描；缺 RunID 或 Limit 不在 0..1000 返回 ErrInvalidQuery。Limit=0 表示 100。AfterSequence 为独占下界，按权威 Sequence 升序返回；Sequence 存在空隙是正常行为。NextSequence 仅在当前还有下一页时返回本页最后 Sequence，否则 0。0 不表示 run 已结束：实时轮询应自行保留最后已收 Sequence 作为下一次 AfterSequence。

Store.Append 以 `(Scope, Sequence)` 原子幂等；同值重放不追加，冲突错误不替换旧值。Append 成功后 Query 立即可见；输入、存储值、各次返回的 Duration 指针和 Records slice 相互独立。Recorder.Query 再校验自定义 Store 返回页的 scope、游标、上限、顺序和闭集值，异常整页拒绝。Scope 过滤不构成认证；对外提供查询的宿主仍负责授权。

New 对 nil/typed-nil Store 返回 ErrStoreRequired，不回退内存。NewMemoryStore 仅进程内存、不持久化、不限制保留量；宿主负责容量和持久化方案。Recorder 无 Close，Agent.Close/DetachRun 不关闭共享 Store。自定义 Store 的可选 io.Closer 由宿主调用。

## 失败、取消与安全边界

组件同步调用 Store.Append，把排序、100ms 墙钟上限、取消收尾剩余总 cleanup budget、panic recover、首错关闭与迟到返回 fence 交由已有 core observer。第一次 error/panic/timeout 只停用当前 run 的 observer，发一次 NoticeRuntime，Data 闭集为 `code=observation_disabled`、`reason=error|panic|timeout`、`observer_index`；不修改 Result、HITL 或 checkpoint，其他 run 继续。组件没有新的超时 knob、后台重试、错误策略或 sink。

成功 Append 不会被用户背压 drop 撤回。取消收尾可在尚余窗口观察已接受的 capability 终态；用户尚未收到的事实由 core Dropped 摘要计数，不假装 UI 已同步。自定义 Store 必须在 commit 前检查 ctx；SDK 无法强杀任意 Go 回调或回滚违反取消约定的迟到副作用。超时后 core 不再为当前 observer/run 启动后续回调，迟到返回不能再发 notice 或重新启用。回调不得同步等待当前 run、Result、Events、publisher 或 Agent.Close；独立 Query 可并发执行。

缺少观察只表示没有对应事实，不能推导“未调用”、完整安全审计或准确计费。Recorder 仅校验封闭值及可信入口 envelope，不重新解析 Driver 协议或构造资源 catalog，也不凭名称判断真实调用。无 capability 支持时由 core 报 observation_unavailable，组件不编造空历史是完整历史。

## G03 应合并的中央文档

- `docs/api-reference.md`：RunServiceProvider/RunAttachment 后增加可选 capabilityrecorder 的构造、Query、Scope、Store 生命周期示例。
- `docs/streaming.md`：observer-before-user-backpressure、实时 Query、100ms/remaining cleanup/首错/迟到约束、NextSequence=0 的含义。
- `docs/run-policy.md`：观测失败是安全 notice，不加入 Result/error/HITL 策略；自定义 Store 必须遵守 callback ctx。
- `README.md` 或文档地图：增加可选 `hosttools/capabilityrecorder` 使用入口。
- `CHANGELOG.md`：新增 opt-in scoped capability recorder、显式 Store/内存实现、安全分页/原子幂等/实时查询，以及共享 Store 归宿主管理；列入本批公共新增语义。

局部 godoc 已在包内同步：`doc.go`、`types.go`、`recorder.go`、`memory.go`。公共新增声明严格限于 C03：Scope、Record、Query、Page、Store、Config、Recorder、New、Recorder.Option、Recorder.Query、NewMemoryStore、ErrInvalidQuery、ErrStoreRequired。Recorder 的 RunServiceProvider 是私有实现；不额外导出 AttachRun/DetachRun。根包/driver 声明无变化，无需修改其 AST golden。

## 来源与拒绝移植的行为

只用 `git -C /Users/blurooo/project/agent-adaptor-internal show` 读取固定 `1921636510ced4c830c0287fe68d41218f1ed185` 的 recorder 和指定 `9b1ce27faa4e46ba6a213210eaffc8748b327bdb`、`dab6933b9159894c36fa78b8367c6bc572227621` 的变更清单；未读 internal 脏工作区。拒绝 internal 的 root Admin/Store 类型、宽泛跨 identity 过滤、按 observed 时间游标、隐式 limit 修正、JSONL 持久化 API 与旧包路径，采用冻结 C03 的最终公开层和精确 Sequence 查询。provider 特有 Unicode MCP alias 规则属于各 Driver，未复制到 recorder。

依赖选型：仅标准库和既有 root/capability 公共词汇；无新增顶层 require。内存排序、原子写和封闭值校验不需要新的协议或数据库依赖，不引入内部 engine/provider import。

## 验证范围

先加入独立失败 fixture，包尚无实现时 `go test -count=1 ./hosttools/capabilityrecorder` 明确 exit 1（no non-test Go files），再交付实现。局部合同测试覆盖精确 scope/Unicode/分隔符、分页边界、取消写入、输入/查询 deep clone、冲突、并发 runs、实时查询、Result 各层、HITL、健康 checkpoint、error/panic/timeout/late、一轮失效后下一轮恢复、共享生命周期、非法闭集及自定义 Store 越界页拒绝。

最终提交 SHA 上执行 task.validation 的完整包检查及 `go test -race -count=5 ./hosttools/capabilityrecorder`，真实结果、计数和日志位于未跟踪的 result.json/evidence。环境为 Go 1.26.5 macOS arm64，普通 live/e2e/golden 更新门均设 0；没有付费或真实 provider 调用。未宣称 Linux/Windows、四 Driver live、跨层独立验收或全仓发布门禁通过，后续任务和 gate 仍各自负责。
