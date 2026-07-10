# Team Mode Default-Agent Notes

本文记录 2026-07-09 / 2026-07-10 这轮围绕 Team Mode 默认 agent 方案，对当前仓库已经做过的改造、排查结论，以及接下来准备继续做的优化。目的只有一个：避免第二天忘记“已经改了什么、为什么这么改、当前还剩什么问题”。

## 1. 目标背景

这轮工作的直接目标是：

1. 先不考虑用户自定义 agent，只考虑“leader agent 和 member agent 都是本仓库 SDK 创建出来的 Claude Code”
2. 让这条默认链路支持 Team Mode 文档中的：
   - `TextPart + DataPart(Request)`
   - `TextPart + DataPart(Artifact)`
3. 在 caller 侧（MCP / Delegator）支持：
   - 执行前上报
   - 执行后上报
   - 通过 workflow server 约束阶段调用顺序

对应参考文档：

- `../doc/flowx/team-mode/custom-agent-a2a-integration.md`
- `../doc/flowx/team-mode/todo.md`

## 2. 这轮已经做过的通用能力改造

### 2.1 `pkg/bridges/a2a`

已加的通用扩展点：

1. `RunStreamingMode`
   - 允许 bridge 侧显式关闭 SDK run 的 streaming
   - 用途：Claude 的 structured output 在 `Streaming=true` 时不稳定或不支持，member 侧需要能关闭 SDK streaming，但仍保留 A2A transport

2. `ResultBuilder`
   - 允许使用方自定义 terminal artifact
   - 可以控制：
     - 最终 `StatusText`
     - 是否替换默认 `agent-adaptor-result`
     - 自定义 artifact 的 `TextPart/DataPart/URLPart`

3. 协议边界上的 `DataPart` JSON 标准化
   - 在 bridge 侧发 artifact 时，不再把 Go struct 直接塞进 `Part.Data`
   - 统一先转成普通 JSON-compatible value（`map[string]any` / `[]any` / `string` / `float64` / `bool` / `nil`）

### 2.2 `pkg/clients/a2a`

已加的通用扩展点：

1. request 上行时的 `DataPart` JSON 标准化
   - 和 bridge 侧同理
   - 这样 caller 传 struct 也不会把 Go 具体类型漏进协议层

### 2.3 `pkg/hosttools/a2adelegation`

已加的通用扩展点：

1. `DelegationRequest`
   - 新增 `Message *clienta2a.Message`
   - 新增 `ContextID`
   - 新增 `IncludeRemoteArtifacts`
   - 新增 `StageContext`

2. `DelegationResult`
   - 新增 `RemoteArtifacts []RemoteArtifact`
   - 让 caller 侧能拿到远端完整 artifact，而不是只剩压平后的 `summary/messages/metadata`

3. `DelegationLifecycleHook`
   - `BeforeDelegate`
   - `AfterDelegate`
   - 用于 caller 侧 workflow server 的前后上报和顺序约束

4. `ToolSpec`
   - 在 MCP server 上支持挂自定义 stage tool
   - 不再只有固定的 `delegate_to_agent`

5. 失败时补带远端 `task.Status.Message`
   - 以前很多失败只有 `TASK_STATE_FAILED`
   - 现在会把更具体的远端错误文本带回来，方便调试

## 3. Team Mode 顶层包装的边界调整

一开始写过一个顶层包装在：

- `pkg/hosttools/teammode`

后来确认这层不适合留在通用 `pkg` 里，因为它已经开始表达 Team Mode 的业务语义：

- stage
- typed request / artifact
- workflow invocation context

所以这层已经挪走，改成只放在 example 里：

- `examples/decide-plan-team/stage.go`

当前边界是：

- 通用能力留在 `pkg/bridges/a2a` / `pkg/hosttools/a2adelegation`
- Team Mode 顶层包装只留在 example 层

## 4. `examples/decide-plan-team` 当前状态

这个 example 现在已经被收敛成最小可读版本：

- `examples/decide-plan-team/main.go`
- `examples/decide-plan-team/stage.go`
- `examples/decide-plan-team/demo-repo/`
- `examples/decide-plan-team/inputs/`

它现在展示的是：

1. leader 通过 `delegate_to_plan_designer` / `delegate_to_requirement_implementer` / `delegate_to_code_verifier` 三个工具工作
2. 三个 member 都是默认 Claude Code A2A server
3. request / artifact 的字段名尽量贴 Team Mode 文档里的 `DesignPlanRequest` / `DesignPlanArtifact` / `VerifyCodeRequest` / `VerifyCodeArtifact`
4. leader 调工具时填写一段文本 `instruction`；这段文本会直接进入发给 member 的 `TextPart`，而 host 继续负责补齐固定结构化参数，并缓存上一阶段的 `plan_content`
5. 当前 `code verifier` 先收敛成最小版本：只跑 `"go build ./..."` 和 `"go test ./..."`

以 `design_plan` 阶段为例，当前示例使用的是：

- 对外协议：
  - `DesignPlanRequest`
  - `DesignPlanArtifact`
- member 内部模型输出：
  - `designPlanModelOutput { summary, artifact }`

也就是说：

- 模型一次性产出 `summary + artifact`
- host 再拆成：
  - `TextPart = summary`
  - `DataPart = artifact`
- leader 看到的 tool schema 现在只暴露一个文本 `instruction` 字段，不是直接手填完整 `DesignPlanRequest`

## 5. 这轮排查过的重要问题与结论

### 5.1 为什么最开始“普通文本能跑”，改成结构化输出就坏了

结论：

- 以前 member 只需要返回普通文本，容错高
- 改成结构化输出之后，链路开始依赖 Claude adapter 的 structured output 能力
- 于是把 integration 问题暴露出来了

### 5.2 为什么 host 拼 summary 的方式不理想

最初为了让 demo 先跑通，`TextPart` 的 summary 是 host 侧根据 request / artifact 拼出来的。

这个做法不是错，但不够理想，因为：

- 更自然的语义应该是“模型自己产出摘要”
- host 只负责把模型结果拆成 `TextPart + DataPart`

所以后面改成了：

- 模型输出 `designPlanModelOutput`
- host 拆分 `summary` 和 `artifact`

### 5.3 `WithJSONSchemaOutputFor[...]` 自动生成 schema 的问题

这是本轮最重要的一个发现。

现象：

- 手写的最小 schema + Claude CLI，structured output 能稳定返回
- `WithJSONSchemaOutputFor[designPlanModelOutput]` 原先自动生成的 schema，在同样场景下不稳定

排查结果：

- 自动生成的 schema 原先带了：
  - `$defs`
  - `$ref`
  - `$schema`
- Claude 对这种更复杂 schema 的 native structured output 路不稳定
- 进一步验证后发现：
  - 仅做 `$defs/$ref` 内联还不够
  - 顶层 `$schema` 也会让 Claude native structured output 在这个场景下退化为普通文本结果

用仓库内代码打出来的自动生成 schema 示意：

```json
{
  "$defs": {
    "DesignPlanArtifact": {
      "additionalProperties": false,
      "properties": {
        "plan_content": {
          "type": "string"
        }
      },
      "required": ["plan_content"],
      "type": "object"
    }
  },
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "additionalProperties": false,
  "properties": {
    "artifact": {
      "$ref": "#/$defs/DesignPlanArtifact"
    },
    "summary": {
      "type": "string"
    }
  },
  "required": ["summary", "artifact"],
  "type": "object"
}
```

### 5.4 Claude CLI 原生命令其实是好的

这轮单独直接打过 Claude CLI，分别验证了：

1. 手写简单 schema + prompt → 能正常返回 `structured_output`
2. 手写简单 schema + `--dangerously-skip-permissions` + `IS_SANDBOX=1` + `-` stdin 模式 → 仍能正常返回 `structured_output`

所以结论不是：

- Claude 完全不会 structured output

而是：

- 当前这个仓库里“自动 schema + adapter 集成”的组合用法有优化空间
- 更具体地说，helper 默认生成的 schema 对 Claude 不够友好

### 5.5 A2A `DataPart` 不能直接塞 Go struct

这个坑是真实踩到的：

- 当 `Part.Data` 里直接放 Go struct
- A2A task store 在内部复制 artifact 时走 `gob`
- 报错：

`gob: type not registered for interface: main.DesignPlanArtifact`

这个问题已经通过“协议边界统一 JSON 标准化”修掉了。

## 6. 当前 example 的状态结论

在这轮修复后：

1. 通过模型一次性产出 `summary + artifact`
2. 通过协议边界 JSON 标准化
3. 通过优化过的 `WithJSONSchemaOutputFor[...]`

`examples/decide-plan-team` 现在已经可以成功运行，`delegate_to_plan_designer` 会返回一个结构化 `DesignPlanArtifact`，实现阶段会产出 commit，验证阶段会返回 `VerifyCodeArtifact`。

也就是说：

- example 已经重新切回继续使用 `WithJSONSchemaOutputFor[designPlanModelOutput](...)`
- 使用方不需要再手写 schema 才能把这个 demo 跑通

## 7. 社区/外部最佳实践调研结论

本轮还查过一圈外部资料，结论和我们踩到的坑基本一致：

1. 对 Claude 来说，简单、扁平、内联的 schema 更稳
2. `$defs/$ref` 虽然支持，但并不会真正降低 grammar 编译复杂度
3. 协议边界的数据最好标准化成纯 JSON-compatible value
4. 对复杂 schema，不要太乐观依赖“自动生成就一定稳定”

因此我们现在倾向的优化方向是合理的。

## 8. 这轮已经继续完成的改造

### 8.1 优化 `WithJSONSchemaOutputFor[T]`

已经完成：

- `WithJSONSchemaOutputFor[T]` 默认改为生成 **内联引用** 的 schema
- 同时移除顶层 `$schema` / `$id` 这类声明性字段
- `JSONSchemaFor[T]` 本身的默认行为不变，仍保留原始生成能力

这样使用方仍然可以写：

```go
agentadaptor.WithJSONSchemaOutputFor[MyType](...)
```

而不需要自己知道：

- `$defs`
- `$ref`
- schema 内联
- Claude grammar 稳定性

### 8.2 把 example 切回继续用 `WithJSONSchemaOutputFor[...]`

已经完成：

- `examples/decide-plan-team/stage.go`
- 已经重新切回使用：

```go
agentadaptor.WithJSONSchemaOutputFor[designPlanModelOutput](...)
```

并且 example 已验证可跑通。

## 9. 建议明天接着做什么

如果明天继续这条线，建议顺序是：

1. 评估是否要把这条 helper 优化写进 `docs/structured-output.md`
2. 评估是否要在 `docs/a2a.md` 里补一句“Claude structured output 更偏好简单/内联 schema”
3. 看要不要继续优化 `DelegationResult.Summary`
   - 当前 workflow hook 看到的 summary 已优先使用终态 `status.message`，但后续如果接更多远端 agent，仍可以继续观察这一层是否还要更统一
   - 如果希望 workflow server 收到的 summary 更像 member 真实产物，还可以继续调 caller 侧结果映射
4. 再看是否要继续扩展更多 Team Mode 阶段（如 `review_code`）

## 10. 本文档对应的代码范围

这轮涉及的主要区域：

- `pkg/bridges/a2a`
- `pkg/clients/a2a`
- `pkg/hosttools/a2adelegation`
- `examples/decide-plan-team`
- 计划下一步修改：
  - （当前 helper 优化已完成，后续更多是文档化和阶段扩展）

## 11. 双阶段 demo 已跑通

在这轮继续实现后，`examples/decide-plan-team` 已经不再只是单阶段 `design_plan` 示例，而是一个两阶段的最小 Team Mode：

1. `design_plan`
2. `implement_requirement`

执行路径：

- leader 只通过 MCP tool 调度，不直接改仓库
- `delegate_to_plan_designer` 先触发 `design_plan` stage，并产出 `DesignPlanArtifact`
- `delegate_to_requirement_implementer` 再触发 `implement_requirement` stage，按 `plan_content` 修改临时 git 工作区并提交
- `delegate_to_code_verifier` 最后触发 `verify_code` stage，只验证 `"go build ./..."` 和 `"go test ./..."`
- `implement_requirement` 与 `verify_code` 默认直接吃 host 缓存的最新 `plan_content`；leader 不需要再手工回填这类常量/派生结构化字段

最新一次成功运行的关键信息：

- 运行命令：`go run ./examples/decide-plan-team`
- 最终真实 HEAD commit：`cc18eb6b48887d894993f5996a9b5efe1f2d8d9f`
- 临时工作区最终 `git status` 为空

这说明当前默认 Claude leader + Claude member 的“先计划、再执行、全程由 leader 调度”的最小链路已经成立。

