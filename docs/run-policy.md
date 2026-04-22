# Run policy（`RunPolicy`）合同与实施说明

本文档是 **agent-adaptor** 对「一次执行要遵守哪些安全/能力边界」的**唯一**公开合同；实现与 `AGENTS.md` §2.1 单一路径执行模型一致。已移除的 API：`PermissionProfile`、`WithPermissions`、`WithDefaultPermissions`、各 adapter 配置中重复的权限类 toggle（如 `skip_permissions`、`bypass_approvals_and_sandbox`、`search`、`auto_trust`、`chrome` 等），由 **`RunPolicy` + 绑定/单次 `WithRunPolicy`** 表达。

## 1. 类型与语义

### 1.1 `RunPolicy` 字段

| 字段 | 说明 | 空值 |
|------|------|------|
| `Approvals` | 是否需要人类审批再执行敏感步骤 | `""` 表示**继承**绑定默认 |
| `Isolation` | 工作区/系统隔离强度 | `""` 表示继承 |
| `WebSearch` | 是否允许联网搜索类能力 | `""` 表示继承 |
| `Browser` | 是否允许浏览器/受控页工具 | `""` 表示继承 |
| `Trust` | 委托信任（如 Cursor 的 yolo 语义） | `""` 表示继承 |

### 1.2 枚举

- **Approvals**：`ask` / `auto` / `off`（`off` 映射各 CLI 的「跳过/不再询问」类行为）。
- **Isolation**：`read_only` / `workspace_write` / `unrestricted`（`unrestricted` 映射各 CLI 的「全访问/危险沙箱 off」等）。
- **WebSearch / Browser**：`allow` / `deny`。
- **Trust**：`ask` / `auto` / `deny`（仅部分 adapter 使用）。

### 1.3 具名预设

包级变量（可直接用于 `WithRunPolicy` / `WithDefaultRunPolicy` 或手抄同结构）：

- `RunPolicyInteractive`：`Approvals=ask`，`Isolation=workspace_write`。
- `RunPolicyReadOnly`：`Approvals=ask`，`Isolation=read_only`。
- `RunPolicyTrusted`：`Approvals=off`，`Isolation=unrestricted`（在各 adapter 中映射到最激进的「bypass/全访问」组合，**仅限可信环境**）。

## 2. 合并规则

1. 从 `AgentBinding.Defaults().RunPolicy` 得到绑定默认（可全空，表示不预设任何非继承字段）。
2. 与本次 `RunOption` 中的 `WithRunPolicy` **按字段合并**：`WithRunPolicy` 中非「继承」的字段覆盖绑定对应字段；未在 `WithRunPolicy` 中写的字段（继承）沿用绑定。
3. 合并结果写入 `resolvedInvocation` / `DriverRunRequest.Policy`（**值类型** `RunPolicy`），**唯一**进入 adapter。

与 `WithSession*`、`WithStreaming` 等正交，可同时传。

## 3. 宿主在各场景的用法

| 场景 | 做法 |
|------|------|
| 同进程 `Run` / `Start` | `sdk.Start(ctx, prompt, agentadaptor.WithRunPolicy(p), ...)` |
| 绑定级默认 | `claude.New(cfg, agentadaptor.WithDefaultRunPolicy(p))`（或 `codex` / `cursor` + `Bind`） |
| 多 Agent | `r, _ := sdk.Agent("codex"); r.Run(ctx, prompt, WithRunPolicy(p))`；命名绑定上同样可用 `WithDefaultRunPolicy` |
| SSE / AG-UI 桥 | `sse.Options{ RunOptions: []agentadaptor.RunOption{ agentadaptor.WithRunPolicy(p), } }`；**策略由受信任侧决定**，请求体不解析 `RunPolicy`（会话仍由协议如 `threadId` 等解析） |
| 会话 + 策略 | 同一轮调用同时传 `WithRunPolicy` 与 `WithSessionKey` 等，顺序无要求 |

## 4. 内置 adapter 映射（摘要）

| 维度 | Codex | Claude | Cursor |
|------|-------|--------|--------|
| 审批/隔离 | `app-server`：`mapApprovalPolicy` / `mapSandbox`；`exec`：`--dangerously-bypass-...` 仅当 `Isolation=unrestricted` 等 | `--dangerously-skip-permissions` 当 `Approvals=off`；`--chrome` 当 `Browser=allow` | `--yolo` 当 `Trust=auto` |
| 搜索 | 非流式：`--search` 当 `WebSearch=allow` | — | — |

`DriverDescriptor.RunPolicyCaps` 标明各 driver **建模**的维度；未打勾的维度在 `Policy` 里出现时被忽略或仅为将来扩展保留。

## 5. 适配器/桥接/示例改动清单（实施完成）

- **根包**：`run_policy.go`、`config_types.go` 去掉旧 `PermissionProfile` 与 config 重复权限位；`api.go` `AgentDefaults.RunPolicy`、`DriverRunRequest.Policy`、`Descriptor.RunPolicyCaps`。
- **`options.go`**：`WithDefaultRunPolicy`、`WithRunPolicy`；`runOptions.runPolicy`。
- **`binding.go` / `runner.go` / `run_types.go`**：`mergeRunPolicy`、`cloneRunPolicy`。
- **claude / codex / cursor driver**：读 `req.Policy`；`Descriptor` 去掉已删 config 字段；`RunPolicyCaps`。
- **codex `run_streaming.go`**：`mapApprovalPolicy` / `mapSandbox` 只接受 `RunPolicy`。
- **examples**：`WithRunPolicy` + `AGUIExampleRunPolicy` 等；mock playground 校验 `request.Policy`。
- **测试**：`run_policy_test.go` 合并；既有集成测试在零 `Policy` 下行为与「全继承」一致。

## 6. 与 profile 文档的衔接

外置 profile/持久化层若在结构中存储策略，应使用 `*RunPolicy`（与 `AGENTS` 中绑定默认字段对齐），见 `docs/profile-resolver-api.md`。
