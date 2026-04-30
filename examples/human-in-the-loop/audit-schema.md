# audit ndjson schema

[简体中文 / Chinese Version](./audit-schema.zh-CN.md)

`.spotlight/human-in-the-loop/audit/session.ndjson` is an **append-only newline-delimited JSON** file. Each line = one physical record of a HITL decision the host actually dispatched. Design goals:

- **No dependency on SDK internals**: every field is a primitive JSON type; the schema is stable and does not drift with SDK refactors
- **ETL-friendly**: pipe straight via `tail -F` into Splunk / ELK / Datadog / Loki; filter with `jq`
- **Reconcilable for compliance**: one-to-one with `RunResult.RunID`; can be correlated with the host's own request IDs

> This doc is written for **audit / compliance / ops-integration engineers**, not SDK users. Once you've read it, you can integrate without touching the Go source.

## Single-line sample

```json
{"ts":"2026-04-29T13:24:53.313272Z","run_id":"8e8dc91327...","kind":"question","decision":"approve","resolved_by":"async-channel","latency_ms":13166,"note":"Scene 2 · Async Approve"}
```

## Field reference

| Field | Type | Required | Value range | Meaning |
| --- | --- | --- | --- | --- |
| `ts` | string (RFC3339Nano, UTC) | ✅ | `2026-04-29T13:24:53.313272Z` | Timestamp when the decision **completed**, not when the request was raised |
| `run_id` | string | ✅ | adapter-specific (≤64 chars) | RunID assigned by the SDK; matches `RunResult.RunID`, correlate with host request IDs |
| `kind` | string | ✅ | `permission` / `plan_review` / `question` | Decision type; same semantics as `HumanDecisionKind` |
| `tool` | string | ⬜ | adapter-specific (e.g. `bash`, `Edit`) | Tool name when kind is Permission; left empty for other kinds |
| `payload` | string | ⬜ | adapter-specific (≤256 chars) | Short summary of the original request payload (command, file path, etc.); no sensitive details |
| `decision` | string | ✅ | `approve` / `reject` / `timeout` | Final outcome of the decision (physical result **after OnTimeout / OnReject have been applied**, not the raw `DecisionResult`) |
| `resolved_by` | string | ✅ | `sync-handler` / `async-channel` / `policy` / `auto-reject` | Who / which path resolved this decision |
| `latency_ms` | int64 | ✅ | ≥ 0 | Total wall-clock milliseconds from spotlight run-start to decision completion; includes agent startup + model inference |
| `note` | string | ⬜ | free text | Human-readable label for debugging / correlation (the spotlight uses it to tag scene names); safe to ignore in ETL |

Legal combinations of `decision` and `resolved_by`:

| `decision` | possible `resolved_by` values |
| --- | --- |
| `approve` | `async-channel` (channel mode) / approve via the sync path is not yet supported |
| `reject` | `sync-handler` / `async-channel` / `auto-reject` (synthesised by policy) |
| `timeout` | `policy` (triggered by OnTimeout=Abort) |

## jq recipes

```bash
# Total count
wc -l .spotlight/human-in-the-loop/audit/session.ndjson

# Count by decision type
jq -s 'group_by(.decision) | map({(.[0].decision): length}) | add' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# Average / median latency (ms)
jq -s '[.[].latency_ms] | { avg: (add/length), median: (sort | .[length/2 | floor]) }' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# Group by run_id (all decisions within one run)
jq -s 'group_by(.run_id) | map({run_id: .[0].run_id, decisions: length})' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# Sync-handler rejects only
jq 'select(.resolved_by == "sync-handler" and .decision == "reject")' \
    .spotlight/human-in-the-loop/audit/session.ndjson

# Decisions that took ≥ 10s
jq 'select(.latency_ms >= 10000)' \
    .spotlight/human-in-the-loop/audit/session.ndjson
```

## ETL integration

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

## Deliberately omitted from the schema

The following fields **do not** appear in the ndjson (to avoid schema drift / reduce the ETL surface):

- The full content of decision prompts / payloads (the payload field carries only a summary)
- The full text of the agent output (if compliance audit needs the original, drop `RunResult.RawStreams.Stdout` into a separate sink)
- Any SDK-internal struct field names / internal error codes (only the two stable enums `decision` / `resolved_by` are exposed)
- Intermediate results from multiple retries (each decision writes exactly one line, at its final outcome)

If your compliance requirements need the "original prompt + full transcript", the recommendation is to land them in a separate `transcripts/` directory (its own dedicated sink), decoupled from the audit ndjson.
