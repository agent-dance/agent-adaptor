# 文档地图

本文档把 `docs/` 下的材料分成“当前可直接按此集成”和“历史 / workstream 记录”两类，避免宿主把草案当成公共 API。

## 当前入口

这些文档描述当前代码的公共语义，优先阅读：

| 文档 | 用途 |
|---|---|
| [`../AGENTS.md`](../AGENTS.md) | 已拍板的工程边界与硬约束。 |
| [`api-reference.md`](./api-reference.md) | 当前公开 API 面、构造方式、Run 结果分层。 |
| [`usage-guide.md`](./usage-guide.md) | 单 Agent、多 Agent、session、skills、MCP、profile 与宿主命名陷阱。 |
| [`run-policy.md`](./run-policy.md) | `RunPolicy`、HITL mode、adapter capability 矩阵。 |
| [`streaming.md`](./streaming.md) | `WithStreaming`、`RunHandle.StreamEvents`、AG-UI / SSE bridge 用法。 |
| [`streaming-adapter-contract.md`](./streaming-adapter-contract.md) | 新 adapter 接入 streaming 时必须满足的合同。 |
| [`skill-api-design.md`](./skill-api-design.md) | 当前 skills API 合同与迁移说明。 |
| [`public-errors.md`](./public-errors.md) | 公开错误清单与宿主建议映射。 |
| [`workstream-transcript-contract.md`](./workstream-transcript-contract.md) | 输出 / transcript / raw stream 分层合同。 |
| [`workstream-builtin-probes.md`](./workstream-builtin-probes.md) | built-in adapter 管理面 probes 的当前能力。 |
| [`workstream-session-codec.md`](./workstream-session-codec.md) | adapter session 参数 codec 合同。 |
| [`workstream-session-recorder.md`](./workstream-session-recorder.md) | hosttools session recorder / pending decisions。 |
| [`workstream-adapter-conformance-kit.md`](./workstream-adapter-conformance-kit.md) | `adaptertest` 的范围与用法。 |

## 设计记录

以下文档保留为设计背景、迁移依据或路线图。阅读时以当前入口文档和代码为准：

| 文档 | 状态 |
|---|---|
| [`v0.5.0-release-notes.md`](./v0.5.0-release-notes.md) | v0.5 迁移说明，仍可用于升级背景。 |
| [`v0.5.0-host-integration-plan.md`](./v0.5.0-host-integration-plan.md) | v0.5 设计计划与判据，历史记录。 |
| [`workstream-hitl-v2.md`](./workstream-hitl-v2.md) | HITL v2 详细设计，当前合同以 [`run-policy.md`](./run-policy.md) 为入口。 |
| [`workstream-hitl-claude-phase3.md`](./workstream-hitl-claude-phase3.md) | Claude Phase 3 双向回填实施记录。 |
| [`workstream-hitl.md`](./workstream-hitl.md) | HITL v1 / early design，历史记录。 |
| [`workstream-streaming-chat.md`](./workstream-streaming-chat.md) | streaming chat 设计记录，当前用法见 [`streaming.md`](./streaming.md)。 |
| [`workstream-streaming-claude.md`](./workstream-streaming-claude.md) | Claude streaming workstream 记录。 |
| [`workstream-runtime-service-lifecycle-v2.md`](./workstream-runtime-service-lifecycle-v2.md) | runtime service lifecycle 设计记录。 |
| [`workstream-mcp-profile-materialization.md`](./workstream-mcp-profile-materialization.md) | MCP profile materialization 设计记录。 |
| [`workstream-profile-user-experience.md`](./workstream-profile-user-experience.md) | profile option 设计背景。 |
| [`workstream-bridges-profiles-host.md`](./workstream-bridges-profiles-host.md) | bridge/profile/host 集成背景。 |
| [`profile-resolver-api.md`](./profile-resolver-api.md) | core SDK 之上的 profile resolver 草案；不是 core API。 |
| [`paperclip-alignment-roadmap.md`](./paperclip-alignment-roadmap.md) | 宿主对齐路线图。 |
| [`workstream-output-transcript-impl-spec.md`](./workstream-output-transcript-impl-spec.md) | 输出合同落地记录；当前合同见 [`workstream-transcript-contract.md`](./workstream-transcript-contract.md)。 |

## 维护规则

- README / examples 只能引用当前已经存在的 public API。
- workstream 文档可以保留历史上下文，但若会被 README 或 usage guide 作为入口引用，必须在开头说明当前状态。
- `Admin().*.SetSelectedSkills` 是 process-local override；文档不得再称为 `SyncSkills` 的宿主持久化能力。
- `SessionKey` 是 `(Namespace, Key)` 术语，不是导出的 Go 类型。
- adapter capability 矩阵必须以 `DriverDescriptor.RunPolicyCaps` 的真实实现为准。
