# audit ndjson schema

[English Version](./audit-schema.md)

`.spotlight/human-in-the-loop/audit/session.ndjson` 是一个 **append-only newline-delimited JSON** 文件。每行 = 一个被宿主真正分派出去的 HITL 决策的物理记录。设计目标：

- **不依赖 SDK 内部结构**：每个字段都是基础 JSON 类型，schema 稳定，不随 SDK refactor 漂移
- **ETL 友好**：可直接 `tail -F` 进 Splunk / ELK / Datadog / Loki，可 `jq` 过滤
- **合规可对账**：与 `RunResult.RunID` 一一对应，可与宿主自家请求 ID 串起来

> 这份文档面向**审计 / 合规 / 运维接入工程师**，不是 SDK 用户。读完即可在不看 Go 源码的情况下集成。

## 一行样本

```json
{"ts":"2026-04-29T13:24:53.313272Z","run_id":"8e8dc91327...","kind":"question","decision":"approve","resolved_by":"async-channel","latency_ms":13166,"note":"Scene 2 · Async Approve"}
```

## 字段表

| 字段 | 类型 | 必填 | 取值范围 | 含义 |
| --- | --- | --- | --- | --- |
| `ts` | string (RFC3339Nano, UTC) | ✅ | `2026-04-29T13:24:53.313272Z` | 决策**完成**的时间戳；不是 request 发起时间 |
| `run_id` | string | ✅ | adapter-specific (≤64 chars) | SDK 分配的 RunID；与 `RunResult.RunID` 一致，可与宿主请求 ID 串联 |
| `kind` | string | ✅ | `permission` / `plan_review` / `question` | 决策类型，与 `HumanDecisionKind` 同语义 |
| `tool` | string | ⬜ | adapter-specific (e.g. `bash`, `Edit`) | Permission kind 时的工具名；其他 kind 留空 |
| `payload` | string | ⬜ | adapter-specific (≤256 chars) | 原始请求载荷的简短摘要（命令、文件路径等）；不含敏感细节 |
| `decision` | string | ✅ | `approve` / `reject` / `timeout` | 决策的最终结果（**已通过 OnTimeout / OnReject 解析后**的物理结局，不是 raw `DecisionResult`） |
| `resolved_by` | string | ✅ | `sync-handler` / `async-channel` / `policy` / `auto-reject` | 是谁/哪条路径解决了这次决策 |
| `latency_ms` | int64 | ✅ | ≥ 0 | 从 spotlight 起 run 到决策完结的总壁钟毫秒数；包含 agent 启动 + 模型推理 |
| `note` | string | ⬜ | free text | 调试 / 关联用的可读标签（spotlight 用来标 Scene 名）；ETL 上忽略也行 |

`decision` 与 `resolved_by` 的合法组合：

| `decision` | `resolved_by` 可能值 |
| --- | --- |
| `approve` | `async-channel` (channel mode) / 暂未支持 sync 路径下的 approve |
| `reject` | `sync-handler` / `async-channel` / `auto-reject` (policy 合成) |
| `timeout` | `policy` (OnTimeout=Abort 触发) |

## jq 食谱

```bash
# 总条数
wc -l .spotlight/human-in-the-loop/audit/session.ndjson

# 三类 decision 各几条
jq -s 'group_by(.decision) | map({(.[0].decision): length}) | add' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# 平均 / 中位 latency（毫秒）
jq -s '[.[].latency_ms] | { avg: (add/length), median: (sort | .[length/2 | floor]) }' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# 按 run_id 分组（一次 run 内所有决策）
jq -s 'group_by(.run_id) | map({run_id: .[0].run_id, decisions: length})' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# 仅看 sync-handler 拒绝
jq 'select(.resolved_by == "sync-handler" and .decision == "reject")' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# 找超时（>= 10s）的决策
jq 'select(.latency_ms >= 10000)' \
    .spotlight/human-in-the-loop/audit/session.ndjson
```

## ETL 接入姿势

### Splunk

```conf
# inputs.conf
[monitor:///path/to/.spotlight/human-in-the-loop/audit/session.ndjson]
sourcetype = agent_adaptor_hitl

# props.conf
[agent_adaptor_hitl]
INDEXED_EXTRACTIONS = json
TIMESTAMP_FIELDS = ts
```

### Datadog Agent

```yaml
# /etc/datadog-agent/conf.d/agent_adaptor.d/conf.yaml
logs:
  - type: file
    path: /path/to/.spotlight/human-in-the-loop/audit/session.ndjson
    service: agent-adaptor
    source: agent_adaptor
    log_processing_rules:
      - type: multi_line
        name: ndjson
        pattern: ^\{
```

### Loki / promtail

```yaml
- job_name: agent-adaptor-hitl
  static_configs:
    - targets: [localhost]
      labels:
        job: agent-adaptor-hitl
        __path__: /path/to/.spotlight/human-in-the-loop/audit/session.ndjson
  pipeline_stages:
    - json:
        expressions:
          ts:
          run_id:
          kind:
          decision:
          resolved_by:
          latency_ms:
    - timestamp:
        source: ts
        format: RFC3339Nano
    - labels:
        kind:
        decision:
        resolved_by:
```

## 不在 schema 中的故意省略

下面这些字段**不会**出现在 ndjson 里（避免 schema drift / 减少 ETL 接入面）：

- 决策 prompt / payload 的完整内容（payload 字段只放摘要）
- agent 输出的全部文本（合规审计若需要原文，请用 `RunResult.RawStreams.Stdout` 落另一份 sink）
- 任何 SDK 内部 struct 的字段名 / 内部错误码（仅暴露 `decision` / `resolved_by` 两个稳定枚举）
- 多 retry 的中间结果（每次决策只在最终结局时写一行）

如果你的合规要求需要"原始 prompt + 完整 transcript"，建议把它们落到独立的 `transcripts/` 目录（一份单独 sink），与 audit ndjson 解耦。
