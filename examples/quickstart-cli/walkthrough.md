# quickstart-cli · walkthrough

[简体中文 / Chinese Version](./walkthrough.zh-CN.md)

> This file is the **static walkthrough** (what it should look like). Every spotlight run also produces
> `.spotlight/quickstart-cli/last-run.md` as the **dynamic factual mirror** (what was actually seen this time).
> Read this file during PR review; pair both side by side for after-the-fact troubleshooting.

## 1. Host scenarios

Any script-style product that "feeds a prompt, takes a chunk of text, and leaves" — the SDK surface only uses `New(WithDefaultAgent(...))` + `sdk.Run(ctx, prompt)`:

- **deploy-bot**: CI feeds a build-failure stack to the agent, gets back a root-cause summary, and pastes it into the PR
- **CI step**: after lint, run the agent once and let it write fix-suggestions into the job log
- **postcommit hook**: after each commit let the agent draft a candidate changelog line
- **`git ai-fix`**: a developer resolves an unimportant lint warning with a single command
- **release-notes-generator**: cron pulls the git log diff, feeds it to the agent, gets a release-notes draft

What these products share: **no streaming / no sessions / no approvals / no multi-agent routing**. One prompt in, one chunk of `Output` out.

## 2. One-liner

```bash
go run ./examples/quickstart-cli -agent=codex
```

Switching to `-agent=claude` or `-agent=cursor` runs the same code path end to end. Other flags:

| flag | default | purpose |
| --- | --- | --- |
| `-prompt` | `"Reply with a short acknowledgement for the quickstart example."` | single prompt |
| `-model` | per-driver default (codex=gpt-5.4 / claude=sonnet-4 / cursor=gpt-5) | model override |
| `-timeout` | `3m` | hard timeout for the entire run |

## 3. Terminal artifacts

After the run the terminal prints, in this order, four independent screenshot-friendly regions plus a unified three-banner footer.

### 3.1 Four-panel display (Output / Summary / RawStreams / Transcript — the four layers of one run)

```
┌─ Output ─ what your end-user sees ──────────────────────────────────
│ The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path …
└─

┌─ Summary ─ what your notification feed shows ───────────────────────
│ The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path …
└─

┌─ RawStreams.Stdout ─ raw protocol bytes (head 3 lines / 456B) ──────
│ {"type":"thread.started","thread_id":"019dd993-310d-7ed1-a352-57c2675b1bc9"}
│ {"type":"turn.started"}
│ {"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"The quickstart exa…
│ ... +1 lines
└─

┌─ Transcript ─ parsed semantic items ────────────────────────────────
│ init × 1
│ system × 1
│ assistant × 1
│ result × 1
│ stderr × 1
└─
```

How to read it (this is the §3.4 output contract visualized):

| Panel | Field | Meaning |
| --- | --- | --- |
| **Output** | `result.Output` | The final assistant-facing text — no stdout dump, no summary concatenation, no result JSON. The exact chunk you would show your end user |
| **Summary** | `result.Summary` | Short summary, suitable for lists / notifications / issue comments. It is **deliberately separated from Output**; in this run codex wrote the same content into both, but the contract allows them to differ |
| **RawStreams.Stdout** | `result.RawStreams.Stdout` | The complete raw stdout bytes the adapter received (head 3 lines + total byte count). Audit, replay, and protocol debugging all rely on it |
| **Transcript** | The Kind histogram from `result.Transcript` | The semantic items the adapter parsed (assistant / init / result / stderr / system, etc.). timeline / message-list UI consume this directly |

### 3.2 Three-banner footer (unified close)

```
━━━ Story ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Your deploy-bot just got a one-shot answer; pick the layer your product needs to render.
对位：deploy-bot · CI step · postcommit hook · git ai-fix

━━━ Artifacts ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
- .spotlight/quickstart-cli/quickstart-cli.json
- .spotlight/quickstart-cli/last-run.md
- examples/quickstart-cli/30-second-recipe.md
- examples/quickstart-cli/walkthrough.md

━━━ Try next ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
$ go run ./examples/web-chat-stream -mode=cli -agent=codex
```

## 4. Filesystem artifacts

```
examples/quickstart-cli/
├── main.go               # ≤ 250 lines; rendering + disk writes + three-banner footer
├── 30-second-recipe.md   # 12-line main.go that copy-and-go integrators take away
└── walkthrough.md        # static walkthrough (this file)

.spotlight/quickstart-cli/
├── quickstart-cli.json   # 6-field stable schema: output / summary / raw_stdout_bytes / transcript_kinds / driver_type / exit_code
└── last-run.md           # this run's actual snapshot (dynamic, not committed to git)
```

`quickstart-cli.json` sample:

```json
{
  "driver_type": "codex",
  "exit_code": 0,
  "output": "The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.",
  "raw_stdout_bytes": 456,
  "summary": "The quickstart example looks clear and sufficient as a baseline. It shows the intended SDK path without introducing extra concepts too early.",
  "transcript_kinds": {
    "assistant": 1,
    "init": 1,
    "result": 1,
    "stderr": 1,
    "system": 1
  }
}
```

The fields are stable — pipe straight into `jq` / commit to a repo / feed downstream scripts.

## 5. Where this lands in your product

| Artifact here | Which panel of your product it maps to |
| --- | --- |
| **Output panel** | User-facing surface: the assistant message body in the chat bubble / popup / IDE side panel |
| **Summary panel** | Notification surface: Slack card title, email subject, one-line issue comment summary |
| **RawStreams.Stdout panel** | Audit surface: raw bytes shipped to ELK / S3 / a compliance vault; replay & protocol debugging both depend on it |
| **Transcript panel** | Rendering surface: timeline / message-list / multi-step displays; route by Kind into different UI components |
| **`quickstart-cli.json`** | Downstream ETL: the stable schema consumed by smoke runners / monitoring pipelines / weekly-report bots |
| **`30-second-recipe.md`** | Onboarding: the 12-line `main.go` for copy-and-go; a new hire gets it running in 5 minutes with zero SDK internals |

Integration template (pseudo-code):

```go
sdk := agentadaptor.New(agentadaptor.WithDefaultAgent(codex.New(agentadaptor.CodexConfig{Model: "gpt-5.4"})))
result, err := sdk.Run(ctx, prompt)
if err != nil { /* your error handling */ }

renderToChat(result.Output)            // user-facing surface
notifySlack(result.Summary)            // notification surface
audit.Append(result.RawStreams.Stdout) // audit surface
ui.RenderTimeline(result.Transcript)   // rendering surface
```

Four calls, mapping to four panels in your product. That is the full engineering meaning of the §3.4 output layering.

---

## Appendix · What this spotlight does not show

To keep the "30-second shortest path" story clean, spotlight #1 deliberately **does not** demo the following (head to the matching spotlight):

- streaming tokens / SSE gateway / AG-UI frontend integration → `examples/web-chat-stream`
- multi-driver routing / multi-tenant identity / Admin control plane → `examples/multi-agent-platform` (coming soon)
- dangerous-operation approval / sync reject / async approve / timeout abort → `examples/human-in-the-loop`
- task recipes / profile resources / skills × instructions × hooks layering → `examples/task-recipes`
