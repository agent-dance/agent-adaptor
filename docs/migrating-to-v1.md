# 迁移到 v1：旧 API → 新 API 完整对照

本文面向从仓库实际冻结的 `v0.12.0` API 迁移到 v1 的使用者。v1 是一次干净切换：中央 `SDK`、命名 Agent 注册表、`Start`、`RunHandle`、双事件通道和根包中的 Driver SPI 都不会保留兼容壳。

公共 API 的完整语义见 [api-v1-redesign.md](./api-v1-redesign.md)。本文解决的是迁移问题：旧代码中的每个主要入口、全部 66 个选项和常见辅助类型应该改到哪里。

## 1. 先换心智模型

v1 的应用侧只需要六个核心名词：

| 名词 | 含义 |
|---|---|
| `Agent` | 一个配置完整、构造后即可执行的智能体 |
| `Thread` | 由宿主 key 标识、可续接或分叉的有状态对话 |
| `Stream` | 一次正在进行的执行 |
| `Event` | 执行中的一件 typed 事件 |
| `Result` | 最终输出、用量和审计信息 |
| `Driver` | provider/CLI 的扩展方 SPI |

最重要的变化是：

- 不再先创建中央 `SDK`，也不再向其中注册默认或命名 Agent。每个 Agent 都是普通 Go 变量。
- 唯一构造入口是 `adaptor.New(driver, opts...)`。
- 唯一执行动词是 `Run` 和 `Stream`。`Agent` 与 `Thread` 都实现 `adaptor.Runner`。
- 一次运行只有一条 `Events()` 流；文本、thinking、tool、生命周期、进程、丢弃标记和审批请求都在其中。
- 成功返回 `*Result, nil`；业务失败返回携带完整结果的 `*RunError`；不存在 `Result.Failure` 第二判定面。
- Driver 扩展合同只在 `driver/`；应用代码通常只需要根包和具体驱动包。

## 2. 入口与顶层类型

| 旧 API | v1 API / 迁移方式 |
|---|---|
| `agentadaptor.Build(opts...) (SDK, error)` | `adaptor.New(d, opts...) *adaptor.Agent`。nil Driver 属于编程错误，会 panic；Driver 配置和环境错误在 `Inspect` 或执行前结构化返回。 |
| `agentadaptor.New(opts...) SDK` | `adaptor.New(d, opts...) *adaptor.Agent`。同名但含义不同：第一个参数现在必须是 `driver.Driver`。 |
| `SDK` | 删除。多 Agent 使用多个 Go 变量；宿主若需要动态注册表，自行维护 `map[string]*adaptor.Agent`。 |
| `sdk.Run(ctx, prompt, opts...)` | `agent.Run(ctx, prompt, opts...) (*adaptor.Result, error)`。 |
| `sdk.Start(ctx, prompt, opts...)` | `agent.Stream(ctx, prompt, opts...) adaptor.Stream`。启动前错误由关闭后的 `stream.Result()` 返回。 |
| `sdk.Default()` / `sdk.Agent(name)` | 删除。v1 没有内置默认 Agent 或字符串查找。 |
| `sdk.Admin()` / `AdminAPI` / `AgentAdmin` | `agent.Inspect()` 提供 `Environment`、`Models`、`Quota`、`ConfigSchema`、`Skills`；profile 操作是 `agent.ProfileState(ctx)`、`agent.SyncProfile(ctx)`；skill 选择是 `agent.SelectSkills(ctx, keys)`。Inspector 没有模型猜测入口。 |
| 旧 `Runner`（`Run` / `Start`） | `adaptor.Runner`（`Run` / `Stream`）；`*Agent`、`*Thread` 和宿主装饰器可互换。 |
| `RunHandle.Events()` + `RunHandle.StreamEvents()` | 合并为 `stream.Events() <-chan adaptor.Event`。不再维护第二条通道。 |
| `RunHandle.RunID()` | `stream.RunID()`，`Stream` 返回时立即可用。 |
| `RunHandle.Wait(ctx)` | `stream.Result()`；可并发、多次调用，结果一致。等待由创建 Stream 时的 context 和 `Cancel()` 控制。 |
| `RunHandle.Cancel()` | `stream.Cancel()`；幂等。 |
| `RunHandle.DecisionRequests()` | `*adaptor.ApprovalRequest` 直接出现在统一事件流中，或用 `adaptor.OnApproval` 安装回调。 |
| `RunHandle.ResolveDecision(id, response)` | `req.Approve(ctx)`、`req.Deny(ctx, reason)`、`req.Answer(ctx, choice)`。请求自身持有 exactly-once responder。 |
| `Bind` / `BindTyped` / `AgentBinding` / `TypedAgentBinding` | 删除；内置与第三方 Driver 都直接交给 `adaptor.New`。 |
| `codex.New(cfg, ...)`，以及 Claude/Cursor/CodeBuddy 同形入口 | `<provider>.Driver(<provider>.Config{...})`，再传给 `adaptor.New`。配置由 Driver 捕获，`Run` 与 `Inspect` 观察同一份配置。 |
| `codex.NewAdapter()` / `DriverAdapter` | 实现 `driver.Driver`；可选能力也在 `driver/` 中声明。 |
| `RunResult` | `*adaptor.Result`：`Text`、`Summary`、`Usage`、`Model`、`Provider`、`Metadata`，以及 `Raw()`、`Transcript()`、`Services()`、`Decode()`。 |
| `RunResult.Output` | `result.Text`。 |
| `RunResult.Failure` | 删除；用 `errors.As(err, *adaptor.RunError)` 判断业务失败，并从 `runErr.Result` 读取部分或完整结果。 |
| `RunResult.SessionID` | Thread 连续性由宿主 key 管理；需要审计 Driver resume 状态时调用 `thread.Checkpoint(ctx)`。 |
| `AgentIdentity{ID, TenantID, ProfileID, Name}` | `adaptor.Identity{ID, Tenant, Profile, Name}`。 |
| `SessionStore` / `SessionRequest` / `SessionMode` | `threadstore.Store` + `Agent.Thread` / `Thread.Fork` / `ResumeOnly`。 |
| `memory.NewSessionStore()` | `memory.NewStore()`。 |

## 3. 全部旧选项的去向

v1 使用一套词汇、两个作用域：

- `adaptor.Option` 只能传给 `New`。
- `adaptor.CallOption` 只能传给 `Run` / `Stream`。
- `adaptor.SharedOption` 同时适用于两处，调用处覆盖构造默认值；skills 追加，其余选项按合同替换或显式合并。

作用域错误会尽可能成为编译错误。

### 3.1 原 SDK 级 `Option`（1–9）

| # | 旧选项 | v1 迁移方式 |
|---:|---|---|
| 1 | `WithDefaultAgent(binding)` | 删除。直接把对应 Driver 传给 `adaptor.New`。 |
| 2 | `WithAgent(name, binding)` | 删除。每个 Agent 是一个变量；动态映射由宿主管理。 |
| 3 | `WithSessionStore(store)` | `adaptor.WithThreadStore(store)`，仅构造作用域。 |
| 4 | `WithWorkspaceManager(m)` | `adaptor.WithWorkspaceManager(m)`，仅构造作用域。 |
| 5 | `WithSkillProvider(p)` | `adaptor.WithSkillProvider(p)`，仅构造作用域；合同在 `skill.Provider`。 |
| 6 | `WithSkillSet(set)` | 将 `skill.Set` 作为 `adaptor.WithSkillProvider(set)` 传入。`skill.Set` 同时实现 `skill.Provider` 与 `skill.Catalog`。 |
| 7 | `WithSkillMaterializer(m)` | `adaptor.WithSkillMaterializer(m)`，仅构造作用域；合同在 `skill.Materializer`。 |
| 8 | `WithRuntimeServiceManager(m)` | `adaptor.WithServiceManager(m)`，仅构造作用域。它是 v1 的 runtime service 合同，不兼容旧 `runtimeservice` mixin。 |
| 9 | `WithEventBuffer(runBuf, streamBuf, policy)` | 单流后改为 `adaptor.WithEventBuffer(n)`；默认仅允许丢弃的高频增量会聚合为 `Dropped`。需要全阻塞背压时再加 `adaptor.WithBlockingEvents()`。两者仅构造作用域。 |

### 3.2 原 `AgentOption`（10–29）

| # | 旧选项 | v1 迁移方式 |
|---:|---|---|
| 10 | `WithDefaultPermissionHandler(h)` | `adaptor.OnApproval(h)`；在 handler 中按 `req.Kind == adaptor.ApprovalPermission` 分流。 |
| 11 | `WithDefaultPlanReviewHandler(h)` | 同一 `adaptor.OnApproval(h)`，Kind 为 `ApprovalPlanReview`。 |
| 12 | `WithDefaultQuestionHandler(h)` | 同一 `adaptor.OnApproval(h)`，Kind 为 `ApprovalQuestion`，用 `req.Answer` 回答。 |
| 13 | `WithDefaultIdentity(id)` | `adaptor.WithIdentity(adaptor.Identity{...})`。 |
| 14 | `WithDefaultWorkspace(spec)` | `adaptor.WithWorkspaceSpec(convertedSpec)`。`SharedWorkspace`、`GitWorktreeWorkspace` 名称不变；`AdapterManagedWorkspace` 改名为 `DriverManagedWorkspace`。工作目录现在与 provisioning spec 分离，另用 `adaptor.WithWorkspace(dir)` 设置。 |
| 15 | `WithDefaultSkills(refs...)` | `adaptor.WithSkills(skill.Dir(...) / skill.FS(...) / skill.Inline(...) / skill.Key(...) / skill.Archive(...))`。 |
| 16 | `WithDefaultMCP(specs...)` | `adaptor.WithMCP(mcp.Stdio(...) / mcp.HTTP(...) / mcp.SSE(...))`。 |
| 17 | `WithDefaultProfileResources(res)` | `adaptor.WithProfileResources(profile.Resources{...})`。 |
| 18 | `WithDefaultAgents(specs...)` | `adaptor.WithProfileResources(profile.Resources{Agents: []profile.SubAgent{...}})`。最终字段名是 `Agents`。 |
| 19 | `WithDefaultHooks(specs...)` | `profile.Resources.Hooks`，元素使用最终的 `profile.Hook{Event, MatcherSpec, Handler, ...}` 结构。 |
| 20 | `WithDefaultProfileConfig(patches...)` | `profile.Resources.Config`，元素为 `profile.ConfigPatch{Key, Capability, Values, Native}`；provider 原生文件坐标放在 `NativeConfigPatch` 内。 |
| 21 | `WithDefaultRuntimeServices(services...)` | `adaptor.WithServices(specs...)`。 |
| 22 | `WithDefaultRunPolicy(p)` | `adaptor.WithPolicy(adaptor.Policy{Sandbox, WebSearch, Browser, Approvals})`。 |
| 23 | `WithDefaultInstructions(ref)` | 只有纯内联 `Content` 才可简化为 `adaptor.WithInstructions(text)`。完整旧 bundle 必须重建为 `adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{ID: ..., Path: ..., Content: ..., Fingerprint: ..., Scope: ..., Mode: ..., Native: ...}})`，并把旧 Scope/Mode 常量换成 `profile` 包对应常量；`profile.Text(text)` 只是纯内联便利函数。 |
| 24 | `WithDefaultMetadata(key, value)` | `adaptor.WithMetadata(key, value)`；按 key 合并。 |
| 25 | `WithDefaultStreaming(...)` | 删除。选择 `Run` 或 `Stream` 即可。 |
| 26 | `WithNativeProfile()` | `adaptor.WithProfile(profile.Native())`，仅构造作用域。 |
| 27 | `WithDedicatedProfile(dir)` | `adaptor.WithProfile(profile.Dedicated(dir))`。 |
| 28 | `WithCloneProfile(dir, opts)` | `adaptor.WithProfile(profile.CloneNative(dir, ...))`；使用 `profile.CopySettings()`、`CopyMCP()`、`CopySkills()`、`CopyAuth()` 或 `LinkAuth()`。旧、新 options 是不同命名类型，需重建 `profile.CloneOptions`；旧 `IncludeAuth: true` 映射为 `AuthMode: profile.AuthCopy`（或 `profile.CopyAuth()`），OAuth 场景优先 `profile.LinkAuth()`。重建后才可传给 `profile.WithOptions(newOpts)`。 |
| 29 | `WithCloneProfileFrom(src, dst, opts)` | `adaptor.WithProfile(profile.CloneFrom(src, dst, ...))`；options 按上一行重建。 |

### 3.3 原 `RunOption`（30–56）

| # | 旧选项 | v1 迁移方式 |
|---:|---|---|
| 30 | `WithPermissionHandler(h)` | 调用处使用 `adaptor.OnApproval(h)`，覆盖构造默认 handler。 |
| 31 | `WithPlanReviewHandler(h)` | 同上，在统一 handler 中按 Kind 分流。 |
| 32 | `WithQuestionHandler(h)` | 同上。 |
| 33 | `WithSession(req SessionRequest)` | 删除结构体入口；根据意图改用下面三个有名字的 Thread 动作。 |
| 34 | `WithSessionKey(namespace, key)` | `agent.Thread(hostKey)`：有则续、无则建。v1 只保存一个宿主提供的不透明 key；合并 namespace 等维度时必须使用无碰撞编码，不能依赖未转义分隔符。 |
| 35 | `WithContinueSession(id)` | `agent.Thread(hostKey, adaptor.ResumeOnly())`：只续不建。Driver resume ID 不再是消费者身份；审计状态用 `Checkpoint`。 |
| 36 | `WithNewSession(namespace, key)` | 为新对话分配新的、未使用过的宿主 key，再调用 `agent.Thread(newHostKey)`；v1 不提供同 key 主动重绑入口。 |
| 37 | `WithForkSession(fromID, namespace, key)` | `parent.Fork(newHostKey)`；父 Thread 保持不变，已存在目标返回 `adaptor.ErrThreadAlreadyExists`。 |
| 38 | `WithWorkspace(spec)` | `adaptor.WithWorkspaceSpec(convertedSpec)`，同名 WorkspaceSpec 按第 14 项转换。单次调用的工作目录另用 `adaptor.WithWorkspace(dir)` 覆盖。 |
| 39 | `WithRuntimeServices(services...)` | `adaptor.WithServices(specs...)`，SharedOption。 |
| 40 | `WithSkills(refs...)` | `adaptor.WithSkills(refs...)`，参数使用 `skill` 包词汇；调用处在构造默认 skills 后追加。 |
| 41 | `WithMCP(specs...)` | `adaptor.WithMCP(servers...)`；调用处声明替换构造默认声明。 |
| 42 | `WithProfileResources(res)` | `adaptor.WithProfileResources(profile.Resources{...})`。 |
| 43 | `WithAgents(specs...)` | `adaptor.WithProfileResources(profile.Resources{Agents: ...})`。 |
| 44 | `WithHooks(specs...)` | `adaptor.WithProfileResources(profile.Resources{Hooks: ...})`。 |
| 45 | `WithProfileConfig(patches...)` | `adaptor.WithProfileResources(profile.Resources{Config: ...})`。 |
| 46 | `WithModel(model)` | `adaptor.WithModel(model)`，同名 SharedOption。 |
| 47 | `WithRunPolicy(p)` | `adaptor.WithPolicy(p)`；调用处整体替换构造默认 Policy，不做字段级合并。 |
| 48 | `WithInstructions(ref)` | 纯内联文本可改为 `adaptor.WithInstructions(text)`；若旧 ref 使用 ID、Path、Fingerprint、Scope、Mode 或 Native，必须在调用处用 `adaptor.WithProfileResources(profile.Resources{Instructions: &profile.Instructions{...}})` 保留全部字段，不能只取 Content。 |
| 49 | `WithMetadata(key, value)` | `adaptor.WithMetadata(key, value)`，按 key 合并。 |
| 50 | `WithAgentIdentity(id)` | `adaptor.WithIdentity(adaptor.Identity{...})`。 |
| 51 | `WithStreaming()` | 删除；调用 `runner.Stream(...)`。 |
| 52 | `WithoutStreaming()` | 删除；调用 `runner.Run(...)`。批处理/事件消费方式与 provider transport 协商是两件事，v1 不提供平行的 token 增量抑制开关。 |
| 53 | `WithOutputSchema(schema)` | Go 类型使用 `adaptor.WithSchema[T](...)`；已有 JSON Schema 使用 `adaptor.WithSchemaJSON(schemaJSON, ...)`。 |
| 54 | `WithJSONSchemaOutput(schemaJSON, opts...)` | `adaptor.WithSchemaJSON(schemaJSON, opts...)`。 |
| 55 | `WithJSONSchemaOutputFile(path, opts...)` | 宿主先 `os.ReadFile(path)`，再调用 `adaptor.WithSchemaJSON(data, opts...)`；SDK 不代读文件。 |
| 56 | `WithJSONSchemaOutputFor[T](opts...)` | 批处理优先用 `adaptor.RunAs[T](ctx, runner, prompt, callOpts...)`；流式或手动解码用 `adaptor.WithSchema[T](schemaOpts...)` + `result.Decode(&value)`。 |

### 3.4 其余选项族（57–66）

| # | 旧选项 | v1 迁移方式 |
|---:|---|---|
| 57 | `WithDirSkillKeyPrefix(prefix)` | `skill.WithDirSkillKeyPrefix(prefix)`，传给 `skill.LocalSkillsFromDir`。 |
| 58 | `WithDirIgnore(patterns...)` | `skill.WithDirIgnore(patterns...)`。 |
| 59 | `WithDirSkillFile(name)` | `skill.WithDirSkillFile(name)`。 |
| 60 | `WithSkillCacheRoot(dir)` | `skill.WithSkillCacheRoot(dir)`，传给 `skill.NewDefaultSkillMaterializer`。 |
| 61 | `WithMaxArchiveSize(n)` | `skill.WithMaxArchiveSize(n)`。 |
| 62 | `WithMaxFileSize(n)` | `skill.WithMaxFileSize(n)`。 |
| 63 | `WithMaxArchiveEntries(n)` | `skill.WithMaxArchiveEntries(n)`。 |
| 64 | `WithArchiveHeader(key, value)` | `skill.WithArchiveHeader(key, value)`，传给 `skill.ArchiveURL`。 |
| 65 | `WithArchiveHTTPClient(client)` | `skill.WithArchiveHTTPClient(client)`。 |
| 66 | `WithCallerIdentity(ctx, id)` | 传播身份用 `adaptor.WithIdentity(id)`；运行中的读取端改为 `adaptor.IdentityFromContext(ctx) (adaptor.Identity, bool)`。 |

## 4. 叶子包和辅助 API

### 4.1 skill

| 旧 API | v1 API |
|---|---|
| `LocalSkill(dir)` | `skill.Dir(dir)` |
| `FSSkill(fsys, root)` | `skill.FS(fsys, root)` |
| `InlineSkill(key, skillMD)` | `skill.Inline(key, skillMD)` |
| `Key(key)` | `skill.Key(key)` |
| `Require(value, reason)` | `skill.Require(value, reason)` |
| `ArchiveFromBytes` / `ArchiveFromPath` / `ArchiveFromURL` | `skill.Archive(key, skill.ArchiveBytes(...)/ArchiveFile(...)/ArchiveURL(...), archiveOpts...)` |
| `LocalSkillsFromDir(root, opts...)` | `skill.LocalSkillsFromDir(root, opts...)` |
| `SkillsAsRefs(skills)` | `skill.SkillsAsRefs(skills)`；具体 `skill.Skill` 本身也能直接作为 `skill.Ref` 传给 `WithSkills`。 |
| `NewDefaultSkillMaterializer(opts...)` | `skill.NewDefaultSkillMaterializer(opts...)` |

归档选项都在 `skill` 包：`WithFormat`、`WithSubpath`、`WithFingerprint`、`WithArchiveHeader`、`WithArchiveHTTPClient`。`WithFingerprint` 声明稳定的来源身份/版本，不是缓存 key，也不提供完整性校验；内置 materializer 的缓存来自实际解包内容。

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/skill"
)

agent := adaptor.New(
    codex.Driver(codex.Config{}),
    adaptor.WithSkills(
        skill.Dir("./skills/review"),
        skill.Archive(
            "deploy-kit",
            skill.ArchiveFile("./deploy-kit.tgz"),
            skill.WithFormat(skill.FormatTarGz),
            skill.WithFingerprint("deploy-kit-v3"),
        ),
    ),
)
```

旧 `providers.MarkRequired` 不迁移；需要把某个 Provider 的全部返回项标成 required 时，用普通装饰器组合 `skill.Require`：

```go
import (
    "context"

    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/driver"
    "github.com/agent-dance/agent-adaptor/skill"
)

type requireAll struct {
    skill.Provider
    reason string
}

func (p requireAll) GetSkills(ctx context.Context, keys []string) (map[string]skill.Skill, error) {
    values, err := p.Provider.GetSkills(ctx, keys)
    if err != nil {
        return nil, err
    }
    for key, value := range values {
        values[key] = skill.Require(value, p.reason)
    }
    return values, nil
}

func newRequiredAgent(d driver.Driver, provider skill.Provider) *adaptor.Agent {
    return adaptor.New(d, adaptor.WithSkillProvider(requireAll{provider, "host-required"}))
}
```

### 4.2 MCP

应用代码使用 `mcp.Server` 与一行式构造器，不需要导入 `driver`：

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/claude"
    "github.com/agent-dance/agent-adaptor/mcp"
)

agent := adaptor.New(
    claude.Driver(claude.Config{}),
    adaptor.WithMCP(
        mcp.Stdio("repo", "repo-mcp", mcp.Args("--root", "/repo")),
        mcp.HTTP("search", "https://mcp.example.com", mcp.WithBearerTokenEnv("MCP_TOKEN")),
    ),
)
```

`mcp.Required(reason)` 可标记必须成功解析的服务。transport 与字段不匹配、重复 key 或 Driver 能力不足会在启动前返回明确错误，不会静默忽略。

### 4.3 profile

`profile.Resources` 的最终结构是：

```text
type Resources struct {
    Skills       []skill.Ref
    MCP          []mcp.Server
    Agents       []SubAgent
    Hooks        []Hook
    Instructions *Instructions
    Config       []ConfigPatch
}
```

`MCP`、`Agents`、`Hooks`、`Config` 为 nil 表示该资源族未声明，非 nil 空切片表示显式清空 SDK 管理的条目；`Skills` 采用追加语义；`Instructions != nil` 才表示声明。

Hook 的 matcher 与 handler 是嵌套结构；ConfigPatch 的 provider 文件坐标也只放在 `Native` 内：

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/claude"
    "github.com/agent-dance/agent-adaptor/profile"
)

agent := adaptor.New(
    claude.Driver(claude.Config{}),
    adaptor.WithProfile(profile.CloneNative(
        "/profiles/tenant-42",
        profile.CopySettings(),
        profile.LinkAuth(),
    )),
    adaptor.WithProfileResources(profile.Resources{
        Instructions: profile.Text("Review every change before editing."),
        Agents: []profile.SubAgent{
            {Key: "reviewer", Description: "Reviews proposed changes"},
        },
        Hooks: []profile.Hook{
            {
                Key:   "format-go",
                Event: profile.HookEventPostTool,
                MatcherSpec: profile.HookMatcher{
                    Subject: profile.HookMatcherSubjectTool,
                    Syntax:  profile.HookMatcherSyntaxExact,
                    Pattern: "Edit",
                },
                Handler: profile.HookHandler{
                    Type:    profile.HookHandlerCommand,
                    Command: "gofmt",
                    Args:    []string{"-w", "."},
                },
            },
        },
        Config: []profile.ConfigPatch{
            {
                Key:        "review-policy",
                Capability: "review",
                Values:     map[string]any{"required": true},
                Native: &profile.NativeConfigPatch{
                    Provider: "claude",
                    FileKind: profile.ConfigFileJSON,
                    Path:     "settings.json",
                    Section:  "review",
                    Values:   map[string]any{"required": true},
                },
            },
        },
    }),
)
```

`agent.ProfileState(ctx)` 只读取 desired/observed 状态；真正物化使用 `agent.SyncProfile(ctx)`。profile、skill 或 schema 准备失败都会阻止 Driver 启动。

### 4.4 结构化输出

v1 不再让消费者选择结构化输出 mode。旧 mode 选项全部删除：只保留 schema
声明与生成选项，core 自动优先使用 provider 原生约束，不支持时回退到 Prompt
加本地校验。

| 旧 API | v1 API |
|---|---|
| `NativeStrictOutput()` | 删除，不需要替代；使用自动协商 |
| `PreferNativeOutput()` | 删除，不需要替代；这就是 v1 固定行为 |
| `PromptValidateOutput()` | 删除，不需要替代；原生不可用时自动回退 |
| `StructuredOutputName(name)` | `adaptor.SchemaName(name)` |
| `StructuredOutputDescription(text)` | `adaptor.SchemaDescription(text)` |
| `ReturnInvalidStructuredOutput()` | `adaptor.SchemaReturnInvalid()` |
| schema 派生选项 | `adaptor.SchemaInlineReferences()`、`SchemaAllowAdditionalProperties()`、`SchemaRequireExplicitTags()`、`SchemaUseGoComments(base, path)` |
| `JSONSchemaFor[T]` | 由 `WithSchema[T]` / `RunAs[T]` 自动派生；外部已有 schema 时使用 `WithSchemaJSON` |
| `DecodeStructuredOutput[T](result)` | `result.Decode(&value)` |
| `RunStructured[T](...)` | `adaptor.RunAs[T](ctx, runner, prompt, callOpts...)` |

```go
import (
    "context"

    adaptor "github.com/agent-dance/agent-adaptor"
)

type Review struct {
    Summary string   `json:"summary" jsonschema:"required"`
    Risks   []string `json:"risks" jsonschema:"required"`
}

func review(ctx context.Context, runner adaptor.Runner) (Review, *adaptor.Result, error) {
    return adaptor.RunAs[Review](ctx, runner, "Review the current diff")
}
```

流式场景使用：

```go
import adaptor "github.com/agent-dance/agent-adaptor"

stream := agent.Stream(
    ctx,
	"Review the current diff",
	adaptor.WithSchema[Review](
		adaptor.SchemaName("change_review"),
    ),
)
for range stream.Events() {}
result, err := stream.Result()
if err == nil {
    var value Review
    err = result.Decode(&value)
}
```

校验失败默认使运行失败。`SchemaReturnInvalid()` 会保留无效结构化结果，但随后调用
`Decode` 仍返回校验错误。

### 4.5 policy 与 HITL

| 旧 API | v1 API |
|---|---|
| `RunPolicy` | `adaptor.Policy{Sandbox, WebSearch, Browser, Approvals}` |
| `IsolationLevel` | `adaptor.SandboxLevel`：`SandboxInherit`、`ReadOnly`、`WorkspaceWrite`、`Unrestricted` |
| `PolicyHostReview` | `adaptor.PolicyWorkspaceWrite` + `adaptor.OnApproval(hostHandler)` |
| `PolicyReadOnlyReview` | `adaptor.PolicyReadOnly` + `adaptor.OnApproval(hostHandler)` |
| `PolicyAutonomous` | 在 Driver 声明对应能力时，使用 `Policy{Sandbox: Unrestricted, Approvals: ApprovalPolicy{Permission: ApprovalAutoApprove, PlanReview: ApprovalAutoApprove, Question: QuestionAutoDeny}}`；若 Driver 支持 Ask、且希望请求仍经过宿主，可改用 `PolicyUnrestricted` + `OnApproval(adaptor.ApproveAll())`。 |
| `HumanDecisionPolicy` | `adaptor.ApprovalPolicy`，放在 `Policy.Approvals`。 |
| `EffectiveHumanDecisionPolicy` | 仅 Driver 实现侧使用 `driver.EffectiveHumanDecisionPolicy`；应用不需要调用。 |
| `HumanDecisionKind` | `adaptor.ApprovalKind`：`ApprovalPermission`、`ApprovalPlanReview`、`ApprovalQuestion` |
| `DecisionRequest` / `DecisionResponse` | `*adaptor.ApprovalRequest` + `Approve` / `Deny` / `Answer` |
| `DecisionChoice` | `adaptor.Choice` |
| mode / fallback | `ApprovalMode`、`QuestionMode`、`FallbackAction`；常量使用 `ApprovalAsk`、`ApprovalAutoApprove`、`ApprovalAutoDeny`、`QuestionAsk`、`QuestionAutoDeny`、`FallbackAbort`、`FallbackContinue`、`FallbackRetry` 等根包名字。 |

`ApprovalsAutoDeny` 是严格依赖能力声明的 preset：只有 Driver 对 Permission、PlanReview 和 Question 都声明相应 AutoReject 能力时才可使用。显式 Policy 维度与 approval mode 会在启动前按 Driver capability 校验；跨 provider 最可移植的写法是保留零值，让 SDK/Driver 采用默认值，而不是假设所有 Driver 都支持某个显式 mode。

回调形态：

```go
import (
    "context"

    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/codex"
)

agent := adaptor.New(
    codex.Driver(codex.Config{}),
    adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
        switch req.Kind {
        case adaptor.ApprovalPermission, adaptor.ApprovalPlanReview:
            return req.Approve(ctx)
        case adaptor.ApprovalQuestion:
            return req.Answer(ctx, "continue")
        default:
            return req.Deny(ctx, "unsupported request kind")
        }
    }),
)
```

Web/UI 形态直接在 `stream.Events()` 中处理 `*adaptor.ApprovalRequest`。应答 exactly-once；重复应答、Kind 不匹配、过期和未绑定请求分别返回稳定错误。timeout、retry 和 fallback 只由 `Policy.Approvals` 控制。

v1 不提供 `ApprovalRequest.Risk()`：当前 Driver SPI 没有真实风险信号，SDK 不伪造风险级别。

## 5. Stream、Event 与 Result

### 5.1 事件对照

| 旧事件 | v1 typed Event |
|---|---|
| `text.start/content/end` | `adaptor.TextDelta{MessageID, Text, Role, Phase}` |
| `reasoning.start/content/end` | `adaptor.Thinking{MessageID, Text, Phase}` |
| `tool_call.start/args/end` | `adaptor.ToolCall{ID, Name, Args, ArgsDelta, Result, Phase}` |
| `tool_call.result` | `adaptor.ToolResult` |
| `run.started` | `adaptor.RunStarted` |
| `run.finished` / `run.error` | `adaptor.RunFinished{Failed, Reason, Message, Usage, ...}`；最终判定仍以 `Stream.Result()` 为准 |
| 进程 spawn / stdout / stderr | `adaptor.ProcessInfo` |
| invocation / lifecycle / runtime / step / transcript item | `adaptor.Notice` 与对应 `Notice*` kind |
| `hitl.requested` / `hitl.resolved` | `*adaptor.ApprovalRequest` + 审批生命周期 `Notice` |
| `stream.dropped` | `adaptor.Dropped{Count, ByKind, FirstSequence, LastSequence, Reason, Source, Details}` |
| subagent side stream | 主事件流中的 `adaptor.SubagentUpdate{Agent, Kind, Delta, Data}` |

每个 Event 都通过 `Meta()` 暴露 SDK 权威的 `RunID`、`ThreadKey`、严格递增 `Sequence`、`Time` 与可选 provider `Source` 坐标。

### 5.2 正确消费 Stream

```go
import (
    "fmt"

    adaptor "github.com/agent-dance/agent-adaptor"
)

stream := agent.Stream(ctx, "Explain this repository")
for event := range stream.Events() {
    switch value := event.(type) {
    case adaptor.TextDelta:
        if value.Phase == adaptor.PhaseContent {
            fmt.Print(value.Text)
        }
    case *adaptor.ApprovalRequest:
        _ = value.Deny(ctx, "interactive approval is disabled")
    case adaptor.Dropped:
        fmt.Printf("\n[dropped %d incremental events]\n", value.Count)
    }
}
result, err := stream.Result()
```

默认背压只允许丢弃可重建的高频增量；审批、生命周期、terminal、tool result、transcript 和 Dropped 等关键事件不能丢。因此消费者应持续 drain `Events()`；若提前停止读取，必须调用 `stream.Cancel()`。`WithBlockingEvents()` 适合要求不丢增量的消费者，但同样必须持续 drain 或取消。

`Run` 与 `Stream` 不是两套执行管线：`Run` 等价于启动 Stream、drain Events、再取 Result。是否使用 provider 原生 streaming transport 由已解析调用与 Driver 能力协商，并不由调用 `Run` 还是 `Stream` 单独决定。

### 5.3 Result 与错误

`Result` 的层次不可互相替代：

- `Text` 是最终 assistant-facing 文本，不含 stdout dump、Summary 或 terminal JSON。
- `Summary` 是有界的宿主摘要；缺失时允许为空。
- `Usage` 是 `*adaptor.Usage`：nil 表示 provider 未报告用量；非 nil 的零值表示用量已被观察，只是所有归一化指标明确为 0。
- `Raw()` 返回完整 stdout、stderr 和 Driver 识别的 provider terminal payload。
- `Transcript()` 只包含 Driver 从正式协议解析的标准化语义条目。
- `Services()` 只报告实际观察到或 SDK 实际确保的 runtime service 状态，不回显声明冒充执行证据。
- `Decode()` 解码已校验结构化输出；没有 schema 时才从 `Text` 做便利 JSON 解码。

```go
import (
    "errors"

    adaptor "github.com/agent-dance/agent-adaptor"
)

result, err := agent.Run(ctx, "Apply the requested change")
if err != nil {
    var runErr *adaptor.RunError
    if errors.As(err, &runErr) {
        result = runErr.Result
        // runErr.Reason / Message / Details 是唯一业务失败判定面。
    }
    return
}
_ = result.Text
```

错误终表如下。所有哨兵均可用 `errors.Is`；表中列出的 typed error 可用 `errors.As`。

| 类别 | v1 错误 |
|---|---|
| 业务运行失败 | `ErrApprovalDenied`、`ErrApprovalTimeout`、`ErrAgentFailed`、`ErrRunCancelled`、`ErrPolicyViolation`；对应 `RunError.Reason` 为 `ReasonApprovalDenied`、`ReasonApprovalTimeout`、`ReasonAgentError`、`ReasonCancelled`、`ReasonPolicyViolation`。 |
| Approval responder | `ErrApprovalResolved`、`ErrApprovalExpired`（同时包装 `ErrApprovalResolved`）、`ErrApprovalKindMismatch`、`ErrApprovalUnavailable`。零值或 nil `ApprovalRequest` 会立即返回 unavailable，不会阻塞。 |
| Thread | `ErrThreadStoreRequired`、`ErrThreadNotFound`、`ErrThreadBusy`、`ErrThreadIncompatible`、`ErrThreadLeaseLost`、`ErrThreadCheckpointMissing`、`ErrThreadAlreadyExists`、`ErrResumeRejected`。 |
| Driver config / policy | `ErrInvalidDriverConfig` / `InvalidDriverConfigError`；`ErrInvalidPolicy` / `InvalidPolicyError`；非审批维度能力缺失使用 `ErrPolicyCapabilityUnsupported` / `PolicyCapabilityUnsupportedError`；审批 mode 能力缺失使用 `ErrHumanDecisionModeUnsupported` / `HumanDecisionModeUnsupportedError`。 |
| Skill | `ErrSkillNotFound`、`ErrSkillKeyConflict` / `SkillKeyConflictError`、`ErrSkillMaterializationFailed` / `SkillMaterializationError`、`ErrSkillSourceMissing`、`ErrSkillKeyMissing`。 |
| MCP | `ErrInvalidMCPConfig`、`ErrMCPUnsupported`、`ErrMCPTransportUnsupported`。 |
| Structured output | `ErrInvalidOutputSchema` / `InvalidOutputSchemaError`、`ErrStructuredOutputUnsupported` / `StructuredOutputUnsupportedError`。 |

旧 Agent 注册表错误（`ErrAgentBindingRequired`、`ErrAgentNameRequired`、`ErrAgentNotFound`、`ErrDefaultAgentAlreadyConfigured`、`ErrDefaultAgentMissing`、`ErrReservedAgentName`）随注册表删除，不再有对应失败模式。

## 6. Thread 迁移

只有显式注入 `WithThreadStore` 才启用状态；`Agent` 默认无状态。

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/memory"
)

agent := adaptor.New(
    codex.Driver(codex.Config{}),
    adaptor.WithThreadStore(memory.NewStore()),
)

thread := agent.Thread("host-ticket-123")
first, err := thread.Run(ctx, "Investigate the failure")
second, err := thread.Run(ctx, "Now propose the smallest fix")

existing := agent.Thread("host-ticket-123", adaptor.ResumeOnly())
fresh := agent.Thread("host-ticket-124") // 新对话由宿主分配新的稳定 key
branch := existing.Fork("host-ticket-123-alternative")
checkpoint, err := existing.Checkpoint(ctx)

_, _, _, _, _, _ = first, second, fresh, branch, checkpoint, err
```

Thread key 是宿主提供的单一、不透明字符串，SDK 会逐字保存和比较。若宿主必须从 tenant、ticket 等多个维度生成 key，应使用 length-prefix、结构化序列化后编码或等价无碰撞方案；不要直接拼接未经转义的分隔符。Driver resume ID 只存在于 checkpoint，不应成为第二套外部身份。

同一 Thread 同时只允许一个持有效 lease 的运行。`Fork`、resume reject fallback 和 checkpoint 持久化都遵守原子更新语义；失败时不会覆盖先前健康的 active 记录。需要主动开始无上下文的新对话时，宿主必须分配新的 Thread key，而不是重绑已有 key。

### 常驻进程配置

旧分支中 provider `CommonConfig.PersistentProcess` 的布尔开关不再存在：Claude、CodeBuddy 和 Codex 对显式 Thread 默认允许常驻复用，Cursor 与无状态 Agent 调用仍逐轮启动。

- 旧 `PersistentProcess: true`：删除该字段即可。
- 旧 `PersistentProcess: false`：在 `adaptor.New` 中加入 `adaptor.WithSpawn()`。
- 只要求某一轮使用新进程：把 `adaptor.WithSpawn()` 作为该次 `Run`/`Stream` 的 `CallOption`。
- 旧中央对象的 `Close(ctx)`：改为对每个 `*adaptor.Agent` 调用幂等的 `agent.Close(ctx)`；Close 开始后的新运行匹配 `adaptor.ErrAgentClosed`。

这只是 Driver 内部进程生命周期选择；不要增加新的执行入口或事件通道。`Run`/`Stream.Result()` 的 Result、error、Raw、Transcript、Services 和 checkpoint 合同保持一致。

## 7. Inspector、bridges 与 hosttools

### 7.1 Inspector

```go
import adaptor "github.com/agent-dance/agent-adaptor"

inspector := agent.Inspect()
environment, err := inspector.Environment(ctx)
models, err := inspector.Models(ctx)
quota, err := inspector.Quota(ctx)
configSchema, err := inspector.ConfigSchema(ctx)
skills, err := inspector.Skills(ctx)

_, _, _, _, _, _ = environment, models, quota, configSchema, skills, err
```

可选 probe 不受支持时返回明确 unavailable/unsupported 语义，不伪造结果。所有 probe 使用 `Agent` 构造时 Driver 捕获的真实配置。

### 7.2 bridges

bridge 只消费公开 `Runner` / `Stream` / `Event` / `Result`：

- SSE：`sse.Handler(runner, sse.Options{})`
- AG-UI：`agui.Events(stream, opts...)`；请求级取消优先使用 `agui.EventsContext`
- A2A：`a2a.NewServer(runner, a2a.ServerOptions{...})`
- subagent stream：使用 `bridges/subagentstream` 合并公开 Stream，不直接调 Driver

AG-UI 的机械迁移如下：import `pkg/bridges/agui` 改为 `bridges/agui`；`NewTranslator` 改为 `NewEventTranslator`；`WithDecisionMode` 改为 `WithEventDecisionMode`；`Wrap(handle)` / `WrapWithContext(ctx, handle)` 分别改为 `Events(stream)` / `EventsContext(ctx, stream)`。旧 `ResolveDecision` 已删除，宿主直接保留 Event 中的 `*adaptor.ApprovalRequest` 并调用其 `Approve`、`Deny` 或 `Answer`。

SSE/A2A 的外部会话 ID 映射使用 bridge 内部无碰撞编码。A2A 默认最小暴露；reasoning、tool、审批和诊断只有在 `ExposurePolicy` 明确允许时才越过协议边界。

### 7.3 delegation 与运行期服务

`adaptor.WithRunServices(providers...)` 是通用的 run-scoped 扩展点。`hosttools/a2adelegation.Service.Option()` 已通过它接线：每次运行建立带认证的 MCP sidecar，绑定清理生命周期，并把委托进度作为 `adaptor.SubagentUpdate` 合入 leader 的唯一事件流。

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/hosttools/a2adelegation"
)

reviewer := adaptor.New(codex.Driver(codex.Config{}))
team, err := a2adelegation.NewService(a2adelegation.Config{
    Agents: []a2adelegation.AgentRef{
        a2adelegation.Local("review", reviewer, a2adelegation.Policy{}),
    },
})
if err != nil {
    return err
}
defer team.Close()

leader := adaptor.New(codex.Driver(codex.Config{}), team.Option())
stream := leader.Stream(ctx, "Implement and ask review to verify it")
for event := range stream.Events() {
    if update, ok := event.(adaptor.SubagentUpdate); ok {
        _ = update
    }
}
_, err = stream.Result()
return err
```

`team.Option()` 是首选写法；根包不会新增团队注册表、自动路由或 `WithTeam`。

旧 import `pkg/hosttools/a2adelegation` 改为 `hosttools/a2adelegation`。`WithStatusPartDecoder` 名称与职责不变，但必须从最终 import 路径引用。

### 7.4 session recorder

session recorder 从旧 `StreamPayload` 双栈彻底切换为唯一的 typed `Event` 信封；import 从 `pkg/hosttools/sessionrecorder` 改为 `hosttools/sessionrecorder`。主要符号逐项迁移如下：

| v0.12.0 | v1 |
|---|---|
| `Record{Payload: StreamPayload}` | `EventRecord{Event: adaptor.Event}` |
| `Recorder` / `Backend` | `EventRecorder` / `EventBackend` |
| `New` | `NewEventRecorder` |
| `Option` / `WithClock` / `WithKeyValidator` | `EventOption` / `WithEventClock` / `WithEventKeyValidator` |
| `NewMemoryBackend` | `NewMemoryEventBackend` |
| `JSONLOption` | `JSONLEventOption` |
| `NewJSONLBackend` | `NewJSONLEventBackend` |
| `WithJSONLKeyValidator` / `WithJSONLFileMode` / `WithJSONLDirMode` | `WithJSONLEventKeyValidator` / `WithJSONLEventFileMode` / `WithJSONLEventDirMode` |
| `WithJSONLBadLineHandler` | 删除。v1 不允许跳过损坏审计记录；读取畸形、截断或不一致日志返回可用 `errors.Is(err, sessionrecorder.ErrJSONLEventLogCorrupt)` 判断的错误。 |
| `PendingTracker` / `NewPendingTracker` / `PendingDecisions` | 删除。宿主按 ID 保存运行中的 `*adaptor.ApprovalRequest` 以执行应答；只读 pending 视图从已记录的 `ApprovalRequest` 与 `NoticeApprovalResolved` Event 历史派生。重放的 ApprovalRequest 不带 live responder，不能用于应答。 |

`HostSeq`、`SessionInfo`、`KeyValidator`、`DefaultKeyPattern`、`DefaultKeyValidator` 与 `ErrInvalidSessionKey` 保留名称。JSONL v1 默认每次 append 同步到存储；只有明确接受 buffered durability 时才使用 `WithoutJSONLEventSyncOnAppend()`，并由宿主检查 `Flush` 或 `Close` 错误。

## 8. Driver 扩展与一致性测试

第三方扩展实现 `driver.Driver`，直接走与内置 Driver 相同的 `adaptor.New`。Descriptor 必须如实声明能力；正式协议解析、Transcript、terminal payload 和 checkpoint 归 Driver 所有。

一致性测试的最终 import 路径是 `github.com/agent-dance/agent-adaptor/adaptertest`：

```go
import (
    "testing"

    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/adaptertest"
    "github.com/agent-dance/agent-adaptor/driver"
)

func TestMyDriver(t *testing.T) {
    newDriver := func() driver.Driver {
        return mydriver.Driver(mydriver.Config{})
    }
    adaptertest.TestDriver(t, newDriver, adaptertest.WithConfig(mydriver.Config{}))

    // 第三方 Driver 与内置 Driver 使用同一消费者构造入口。
    var _ adaptor.Runner = adaptor.New(newDriver())
}
```

按 Driver 能力补充 `adaptertest.WithSessionState`、`WithSessionKeys`、`WithGuardKeys`、`WithWorkspace`、`WithRequiredConfigFields`、`WithSyncSkillsProbe` 和显式双门 live 选项。普通测试不得触发付费调用。

## 9. import 路径与删除项

v1 应用代码使用根包：

```go
import (
    adaptor "github.com/agent-dance/agent-adaptor"
    "github.com/agent-dance/agent-adaptor/claude"
    "github.com/agent-dance/agent-adaptor/codebuddy"
    "github.com/agent-dance/agent-adaptor/codex"
    "github.com/agent-dance/agent-adaptor/cursor"
    "github.com/agent-dance/agent-adaptor/mcp"
    "github.com/agent-dance/agent-adaptor/profile"
    "github.com/agent-dance/agent-adaptor/skill"
)
```

Driver 作者另外导入 `github.com/agent-dance/agent-adaptor/driver`；Thread store 实现导入 `threadstore`，内存实现使用 `memory`；协议和宿主组件分别位于 `bridges/...` 与 `hosttools/...`。

以下旧表面不迁移：

- 中央 SDK、默认/命名 Agent registry、binding、`Start`、`RunHandle`、双事件通道。
- 旧根包 Driver SPI aliases、provider sugar、`pkg/` forward 包和迁移期别名。
- `providers/` 包；单个 skill 的 required 语义改用 `skill.Require`，整个 Provider 的装饰策略由宿主实现。
- 旧 `runtimeservice/` mixin；改用 `WithServices`、`WithServiceManager` 或 `WithRunServices`。
- 因缺少真实 Driver 风险信号而无法诚实实现的 `ApprovalRequest.Risk()`。

迁移完成后，应用代码不应再依赖旧中央对象、字符串查找 Runner、平行执行入口或兼容 metadata key。
