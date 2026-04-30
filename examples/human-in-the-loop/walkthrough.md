# human-in-the-loop · walkthrough

[简体中文 / Chinese Version](./walkthrough.zh-CN.md)

> This is the **static walkthrough** (what the standard should look like). Every spotlight run also produces
> `.spotlight/human-in-the-loop/last-run.md` as the **dynamic factual mirror** (what was actually observed this time).
> Use this file for PR review; pull both up side-by-side when troubleshooting after the fact.

## 1. Host scenarios

Any product where "the agent wants to run a shell / call an external API / write a file, and must ask a real human first":

- Risky-operation approvals in finance / healthcare / compliance systems
- Internal production write operations (deploys, rollbacks, data migrations)
- PR auto-fix bot: an automatic fix proposal needs reviewer approval
- IT automation: opening accounts, granting permissions, changing DNS, touching the firewall
- Any system where "operations above the safety threshold must leave an audit trail"

## 2. One-liner

```bash
go run ./examples/human-in-the-loop -agent=claude \
    -decision-timeout=6s -fake-front-end-delay=2s
```

Switching to `-agent=codex` or `-agent=cursor` also runs (the capability matrix still prints, the three acts turn into SKIP, and the reason points cleanly at the driver truth).

## 3. Terminal artifacts

After the run, the terminal prints these three independent screenshot-ready blocks in order:

### 3.1 Capability matrix (driver truth across the three families)

```
Capability matrix (driver-truth, not docs)
┌─────────────┬──────────────────┬──────────────────┬──────────────────┐
│ driver      │ Permission       │ PlanReview       │ Question         │
├─────────────┼──────────────────┼──────────────────┼──────────────────┤
│   codex     │ Auto             │ Auto             │ (none)           │
│ ● claude    │ Auto AutoR       │ Ask Auto AutoR   │ Ask AutoR        │
│   cursor    │ Auto             │ Auto             │ (none)           │
└─────────────┴──────────────────┴──────────────────┴──────────────────┘
Ask=adapter raises real decision request · Auto=adapter resolves silently · Retry=re-ask supported
● = current -agent (claude)
```

How to read it:

- **Ask** = the adapter actually fires a real decision to the host (the spotlight's three acts need at least one of these to be true to be playable)
- **Auto** = the adapter handles it silently itself (no compliance-meaningful audit; not staged in the play)
- **AutoR** = the adapter supports synthesising a local reject (the fallback when the host doesn't deploy a handler)
- **Retry** = the adapter supports re-issuing the same decision after a reject

> v1 reality: **only claude's PlanReview and Question expose a real Ask channel**. codex / cursor have no Ask today; if you want HITL governance you have to mount claude. This is not an SDK bug — it's the current capability of the underlying CLIs.

### 3.2 Three-act play (classified by RunPolicy physical outcome)

```
━━━ Scene 1 · Sync Reject ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = rejected
  decision = reject
  latency  = 10.919s
  output   = Of course! Here's my clarifying question:

━━━ Scene 2 · Async Approve ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = approved
  decision = approve
  latency  = 13.167s
  output   = Of course! Here's my clarifying question:

━━━ Scene 3 · Timeout Abort ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
  status   = timed-out
  decision = timeout
  latency  = 14.702s
  output   = Of course! Here's my clarifying question:
```

Each scene's status is drawn from the truth set below:

| status | Meaning | When it appears |
| --- | --- | --- |
| `rejected` | The host rejected the decision (`IsRejected()=true`) | Sync handler returned `ApprovalRejected`, or the channel resolved with `DecisionRejected` |
| `approved` | The host approved the decision | The channel resolved with `DecisionApproved` / `DecisionAnswered` (depending on kind) |
| `timed-out` | `RunPolicy.HumanDecision.Timeout` expired with `OnTimeout=Abort` (`IsTimedOut()=true`) | Handler is blocked / no consumer on the channel |
| `untriggered` | The agent did not raise a decision request | Prompt / driver / model factors; `OutputHead` shows what the agent answered directly |
| `skipped` | The driver's `Descriptor.RunPolicyCaps` does not support Ask for the chosen kind | The current -agent has no Ask capability whatsoever |
| `error` | Wait returned a non-cancel error | Environment issue (CLI not authenticated / network etc.) |

### 3.3 Three-banner footer

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Three short plays show how your compliance / risk gates land at the SDK boundary.
对位：compliance approvals · PR auto-fix · IT change control

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/human-in-the-loop/audit/session.ndjson
- .spotlight/human-in-the-loop/last-run.md
- examples/human-in-the-loop/walkthrough.md
- examples/human-in-the-loop/audit-schema.md

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/task-recipes -agent=claude
```

## 4. Filesystem artifacts

```
.spotlight/human-in-the-loop/
├── audit/
│   └── session.ndjson          # One line per decision; schema in audit-schema.md
└── last-run.md                 # Real snapshot of this run (dynamic, gitignored)
```

**Audit ndjson sample** (5 fields):

```json
{"ts":"2026-04-29T13:24:40.146453Z","run_id":"9f99b3d7d8...","kind":"question","decision":"reject","resolved_by":"sync-handler","latency_ms":10919,"note":"Scene 1 · Sync Reject"}
{"ts":"2026-04-29T13:24:53.313272Z","run_id":"8e8dc91327...","kind":"question","decision":"approve","resolved_by":"async-channel","latency_ms":13166,"note":"Scene 2 · Async Approve"}
{"ts":"2026-04-29T13:25:08.015253Z","run_id":"e3b0d83c9d...","kind":"question","decision":"timeout","resolved_by":"policy","latency_ms":14701,"note":"Scene 3 · Timeout Abort"}
```

For field meanings, jq recipes, and ETL integration strategies see [`audit-schema.md`](./audit-schema.md).

## 5. Where this lands in your product

| Artifact here | Panel in your product |
| --- | --- |
| **The capability matrix table** | Backend "AI integration capabilities" page: which drivers support which approval channels — used to decide which driver each task type binds to |
| **Scene 1 · Sync Reject** | Synchronous approval modal: the reviewer hits Reject in the IDE / ticketing system, the agent terminates immediately, and the run result carries `IsRejected()=true` |
| **Scene 2 · Async Approve** | Browser push notifications: the decision request lands in the notification centre, the reviewer asynchronously hits Approve; the frontend resolves through the `DecisionRequests()` channel + `ResolveDecision()` |
| **Scene 3 · Timeout Abort** | Fallback policy: if the reviewer doesn't respond in time, abort automatically (preventing a permanently hung agent); the failure carries `IsTimedOut()=true` |
| **audit ndjson** | Compliance audit ETL: `tail -F` it straight into Splunk / ELK / Datadog — fields are stable, `run_id` ties back to the host's RunResult |
| **`audit-schema.md`** | Schema doc for the audit-integration engineer: they can integrate without reading any Go source |

Integration template (pseudocode):

```go
sdk := agentadaptor.New(
    agentadaptor.WithDefaultAgent(claude.New(cfg,
        // No binding-level handler — let decisions flow through the channel
    )),
)

handle, _ := sdk.Start(ctx, prompt, agentadaptor.WithRunPolicy(agentadaptor.RunPolicy{
    HumanDecision: agentadaptor.HumanDecisionPolicy{
        PlanReview: agentadaptor.HumanDecisionAsk,
        Timeout:    8 * time.Second,
        OnTimeout:  agentadaptor.FailureAbort,
        OnReject:   agentadaptor.FailureAbort,
    },
}))

// Your frontend pulls from DecisionRequests(), renders approval cards, and resolves once the reviewer clicks
go func() {
    for req := range handle.DecisionRequests() {
        decision := waitForReviewer(req)             // your own logic
        _ = handle.ResolveDecision(req.RequestID, decision)
    }
}()

result, _ := handle.Wait(ctx)
switch {
case result.Failure.IsRejected(): /* feed back to the reviewer */
case result.Failure.IsTimedOut(): /* nudge the reviewer */
default:                          /* normal completion */
}
```

---

## Appendix · What this spotlight does not show

To keep the story clear, spotlight #4 deliberately **does not** demonstrate these (see the matching spotlight):

- profile resources / skills / instructions recipes → `examples/task-recipes`
- token streaming / SSE gateway / AG-UI frontend integration → `examples/web-chat-stream`
- multi-driver routing / multi-tenant identity / Admin control plane → `examples/multi-agent-platform`
- the 30-second shortest path → `examples/quickstart-cli`
