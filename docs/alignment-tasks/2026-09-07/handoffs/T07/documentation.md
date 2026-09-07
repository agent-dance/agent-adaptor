# T07: Claude partial results and schema/HITL

This handoff implements W04-R05 and W06-R01/R02/R04/R05 against G01 `0adb8355378b0d1c0457119f96da470f4c33366a`. T31 already owns the SPI/core eligibility algorithm; this change only supplies Claude's truthful matrices, its transport and response mapping. It does not close W04/W06's independent, platform or live gates.

## Public behavior and examples

Before this change, Claude advertised neither native nor prompt schema with Ask. Resident errors also returned an empty Response, discarding already parsed protocol output. Now Question and PlanReview Ask can share native schema and bidirectional stream-json in one execution. Native Permission Ask remains unsupported; its supported prompt-validation fallback omits `--json-schema`. Ordinary no-schema Permission Ask is preserved.

The native matrix is `{Permission:false, PlanReview:true, Question:true}`; the prompt-validation matrix is all true. Both pointers are fresh Descriptor snapshots. The legacy `WorksWithHITL=false` summary remains conservative; non-nil matrices replace it per mechanism. There are no new public declarations, root options, API golden changes or dependencies.

| Raw approval policy | Effective schema eligibility | Existing transport activation |
| --- | --- | --- |
| Zero policy | Permission/PlanReview inherit Ask; prompt validation | Observational; no automatic Permission approval |
| QuestionAsk, Permission unset | Permission/PlanReview inherit Ask; prompt validation | Bidirectional; real Permission, Question and PlanReview requests can be answered |
| PermissionAutoApprove, QuestionAsk | PlanReview inherits Ask; native schema | Bidirectional; Question and PlanReview use the same sink |
| PermissionAutoApprove, PlanReviewAsk | Native schema | Bidirectional |
| PermissionAsk without schema | No schema negotiation | Existing bidirectional Permission support |

Schema eligibility uses effective Ask defaults; transport activation still uses the existing raw policy rule. The zero-policy fixture proves prompt selection, local validation and absence of both interactive activation and the permission bypass flag. It is not evidence that an actual Permission request was answered. The QuestionAsk + unset Permission fixture supplies that real inherited-Ask evidence. Neither path silently changes policy to maintain native schema.

A caller can make the native-compatible Permission decision explicitly:

```go
result, err := agent.Run(ctx, "选择目录并生成结果",
    adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"directory":{"type":"string"}},"required":["directory"],"additionalProperties":false}`)),
    adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{
        Permission: adaptor.ApprovalAutoApprove,
        Question: adaptor.QuestionAsk,
    }}),
    adaptor.OnApproval(func(ctx context.Context, request *adaptor.ApprovalRequest) error {
        if request.Kind == adaptor.ApprovalQuestion {
            return request.Answer(ctx, "docs")
        }
        return request.Approve(ctx)
    }),
)
```

Native interactive requests always use `--output-format stream-json --json-schema … --input-format stream-json --include-partial-messages --replay-user-messages --permission-prompt-tool stdio`, even when Request.Streaming is false. Managed duplicate ExtraArgs remain stripped, and unrelated flags survive. There is no caller mechanism selector. Question's internal `question_type` does not return to the CLI; real same-stdin control responses retain request/tool IDs, original questions/plan and selected answers.

Native schema remains a temporary process shape, including on Thread. A live old writer stops before a native turn starts; healthy output may prewarm the next resident process. WithSpawn suppresses that registration/prewarm. Tests warm an actual resident writer first and check that replacement processes never overlap. They do not claim native rounds share one PID.

Resident EOF/cancel/deadline and interactive/transport/fork/resume errors now build the available Response from the original parser before returning the cause. Public failures remain `nil, *RunError`; Raw, formal terminal, Transcript, final Text, Usage and Services are available through RunError.Result. No terminal Text means empty Text; intermediate assistant text stays in Transcript. Terminal usage is authoritative even when truly zero; absent terminal usage falls back only to observed formal stream usage. Interrupted schema is not validated as a new failure that masks the cause. A cause, malformed/missing terminal, nonzero exit or invalid schema cannot promote the checkpoint. The previous healthy record remains unchanged.

The failed resident process drains before the response snapshot, and per-turn observer release waits for in-flight stderr parsing. A terminal arriving with cancellation does not erase cancellation. Safe pre-delivery fallback still dispatches at most one prompt-bearing replacement; successful follow-up prewarming carries no prompt. Post-delivery failure never replays.

## Central integration required from G02

- `docs/structured-output.md`, Claude support table and HITL combinations: replace blanket rejection with native Question/PlanReview versus prompt Permission; explain inherited Ask, raw transport activation and the zero-policy compatibility change. Include the explicit-policy example above.
- `docs/run-policy.md`, Claude mapping/defaults and structured-output interaction: distinguish non-nil effective-Ask matrix checking from unchanged raw transport activation; preserve ordinary no-schema Permission Ask, exactly-once, abort/continue and core-owned timeout policy.
- `docs/streaming.md`, provider transport and failure/result paragraphs: native interactive always remains bidirectional, native Thread is temporary, error output and cause survive, unhealthy checkpoints do not persist.
- `CHANGELOG.md`, current Unreleased changes: record Claude partial Response/cause/observed usage retention and the schema/HITL behavior above, including ordinary zero-policy schema switching to prompt validation. Do not phrase all native schema+Ask as supported.
- `AGENTS.md` §14.1: mark T07 implementation delivered while retaining independent T20/T22/T27 and final gates. T08 and other workstreams are separate.
- C04 §3 explanatory wording: record the coordinator's clarification that zero raw policy remains observational and does not constitute live Permission round-trip evidence.

Local godoc and `claude/README-streaming.md` have been synchronized in this commit. No central file is written by this worker.

## Deliberately rejected internal behavior

Read-only fixed source evidence: internal `126d610dfb6afd2cca19661ab0a7c0b6f626b490` and `426191444582f9fbbe951dbd8b54c1004464262c`. Retained the useful partial-output and double-direction schema ideas. Did not copy session-ID-only checkpoint promotion, detached cancellation persistence, old SDK APIs, public schema modes, coarse WorksWithHITL=true, or the old claim that ordinary Permission Ask was unsupported. No append/capability/todo work from T14 is included.

## Evidence boundary

The new TestAlignmentClaude tests use the current test executable as a fake CLI with real OS pipes. Real control_request/control_response/result frames, stdout tails after EOF, both public result entry points, Thread/WithSpawn, resident handoffs, malformed and failed terminals, denial/timeout/cancel, old-record protection and fallback boundaries are exercised. Existing unisolated fake CLI tests were confined to temporary HOME/CLAUDE_CONFIG_DIR so they do not materialize resources in the developer's real profile.

Initial failing fixtures and development diagnostics are retained outside the source commit. The final result.json records the exact post-commit SHA and complete `go test -count=1 ./claude` plus `go test -race -count=5 ./claude -run TestAlignmentClaude` logs and actual test counts. Execution is local macOS/arm64, with live/E2E/golden-update gates explicitly zero. Loopback fixture execution uses already authorized escalation. No real Claude provider/model/paid call, Linux runtime, native Windows runtime or live compatibility claim is made; T27 must verify the actual supported CLI version on the G05 SHA.

## Attempt 2: resident DecisionCapableSink abort

Independent review of attempt 1 found T07-F01: a valid custom DecisionCapableSink could return an error without canceling the caller context. The parser stored that error, but the resident turn handle's Close intentionally did nothing; without a terminal the reader hung, and with a buffered terminal it could register the failed process for reuse before Response construction rejected the outcome.

The resident reader now observes the parser's decision error, stops that process immediately, and continues draining available stdout/stderr before returning. A buffered success result cannot clear the abort, validate a checkpoint or register the writer. Context cancellation racing the abort/drain remains in the cause chain alongside the original typed decision error. Additional buffered control requests do not invoke the sink again after an abort. Normal terminal completion retains resident stdin and process reuse.

`TestAlignmentClaudeResidentDecisionAbort` uses real OS pipes and a custom sink that never cancels context in its principal case. It covers absent/buffered terminal, cancellation inside the sink and during stdout drain, errors.Is/As, original partial transcript/usage/raw, one spawn, no response/replay/reuse and invalid checkpoint. `TestAlignmentClaudeResidentDecisionSuccessKeepsInput` makes two successful interactive turns on the same resident process. The initial attempt-2 fixture failed on both the hang and failed-writer reuse before the fix; its log is retained separately from attempt 1. Final full-package and five-repeat race checks are rerun on the new source SHA.

G02 should include this behavior in the existing error/result paragraphs of `docs/streaming.md`, `docs/run-policy.md` and CHANGELOG. This is a repair to the already-declared DecisionCapableSink abort contract, not a new policy, event channel or public API. The native capability and R005/R009 boundaries above are unchanged. Attempt-1 reports/logs remain historical evidence and do not constitute acceptance of this fix.

## Attempt 3: partial write, message usage and Wait cause

Independent review reopened T07-F02 with three concrete gaps in attempt 2: short/partial stdin writes returned before draining stdout or finalizing observed stderr; multiple messages were reduced to per-field usage maxima; and the real process Wait error was discarded behind EOF. The original independent overlay was rerun before fixing production code and failed all three top-level cases plus both partial-write subcases. Prior attempt reports and evidence remain preserved.

All prompt-write failures now enter the resident stop/drain/finalize path. Even one accepted byte keeps the no-replay boundary. Buffered stdout and a stderr line without its trailing newline are retained in Raw and the formal parser's Transcript. The actual Wait error is joined into the original cause chain. EOF permits a bounded graceful exit before forced termination, so an independently observed provider exit such as code 23 is classified as agent_error while preserving the original *exec.ExitError. Earlier decision, transport or actual caller cancellation retains its priority. Internal process cleanup kills the configured process group without canceling the private command context before Wait; it therefore does not fabricate host context.Canceled. Resource cancellation is released after Wait.

Without authoritative terminal usage, usage now aggregates distinct formal message IDs. Repeated cumulative deltas and later assistant snapshots for one ID contribute only that message's increase; root and nested stream deltas track their own current message. Two messages with input/output/cache counters (10,5,2) and (20,10,4) therefore yield (30,15,6). A formal terminal usage report of zero still overrides that aggregate. No schema capability, raw-policy activation, checkpoint or T01 input-completion contract changes.

`TestAlignmentClaudePartialWriteDrain` injects one-byte failures over a real child stdin pipe, waits until the child has buffered formal stdout and un-terminated stderr, and checks both public Run and Stream errors, no replay, no checkpoint and no invented cancellation. `TestAlignmentClaudeObservedUsageAndExit` covers real two-message output with repeated deltas/snapshots, exit 23 versus terminal true-zero usage, unchanged prior healthy checkpoint, and public Run/Stream output equivalence. `TestAlignmentClaudeInterleavedMessageUsage` covers root/nested usage attribution. The attempt-2 custom DecisionCapableSink and successful resident-reuse tests remain part of all five required race repetitions.

G02 should add these details to the existing streaming/result/usage and error-cause paragraphs plus CHANGELOG. Local godoc and Claude README are synchronized. The final attempt-3 result lists the append-only commit, complete required commands on that SHA, exact test counts and validator log. This remains macOS fake-process evidence; cross-layer, Linux/Windows and real-provider/live gates remain independent.
