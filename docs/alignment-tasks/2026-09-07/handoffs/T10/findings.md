# T10 findings and resolution

- **T10-F01, P1, resolved (W11-R01–R03; W13-R01/R06).** Independent baseline
  fixtures show no Request append channel, no Policy active budget and a parent
  deadline during Ask classified as approval_timeout. The initial baseline log
  is `evidence/baseline-failure.jsonl` (exit 1, three failed top-level tests).
  The final scope tests preserve exact text, reject unsupported requests before
  resource/lease/Driver access, and distinguish parent from approval deadlines.
- **T10-F02, P1, resolved (W13-R02/R06).** R012 identifies the error-only Store
  interface's missing commit acknowledgment. FinishExecution now settles and
  seals before persistence; delayed Finalize return cannot spend sealed budget.
  Tests cover overdue unscheduled timers, late callbacks, commit error, parent
  cancellation before/after commit, missing checkpoint and schema failure.
- **T10-F03, P1, resolved (W13-R05/R06).** The independent memory fixture failed
  both already-canceled and waiting-for-lock cases because Finalize wrote anyway.
  `evidence/memory-baseline-failure.jsonl` records the original exit 1. Lock-front
  and immediately-prewrite context checks now preserve old records and the key
  index; lock barriers establish ordering. Store godoc explains no postcommit
  rollback.
- **T10-F04, P2, resolved (W13-R06).** The original process-outcome test explicitly
  waited for ctx.Done before returning a provider failure yet required provider
  priority. With the exact path authorized in R012, it now requires deadline
  priority while preserving text, raw stdout, official terminal and context
  cause. The reverse provider-registration-before-cancel case uses a channel
  barrier against the terminal mutex and retains cancellation as a secondary
  cause. No global “context always wins” rule was introduced.

- **T10-F05, P1, resolved (R013; existing C03 descriptive snapshot contract).**
  C01's fixed-G02 public fixture and the local pre-repair repetition each failed
  11 tests/subtests: newApprovalRequest shared nested Payload containers, while
  WithEventMeta shared Choices/Details across consumers. Question choice-key
  mutation even changed the original live Answer classification. The constructor
  and event clone now copy only the existing JSON-container domain; the responder
  pointer remains shared. The fixture covers three Kinds, callback/Stream,
  competing responders, mismatch, nil/zero values and cancel-expiry. Evidence:
  `evidence/C01-baseline-snapshot-red.json`,
  `evidence/C01-baseline-snapshot-red.log`,
  `evidence/approval-snapshot-local-red.jsonl`, and final V04.

- **T10-F06, P1, resolved (W13-R05/R06).** C02's independent review found that an
  inherited ActiveExecutionTimeoutError caused the current run to report its own
  active expiry, even though its fake clock never advanced. The root now
  distinguishes its private expiry object from inherited causes, including
  pre-dispatch terminal events and nested members. A terminal-notification lock
  barrier also proves own expiry before parent cancellation remains active even
  if the outer watcher registers first. Parent causes remain secondary evidence.
- **T10-F07, P1, resolved (W13-R02/R04/R06).** A lawful delayed parent AfterFunc
  can leave the budget child uncanceled after the parent's Err is set.
  FinishExecution now checks the direct parent before sealing, preserving prior
  selected child/controller causes. The independent deferred-parent barrier
  fixture is retained in activebudget tests. The two reported defects (plus
  pre-dispatch and nested variants) produced 9 local red test/subtest outcomes
  before repair, recorded in `evidence/budget-review-local-red.jsonl`.
- **T10-F08, P1, resolved (R014; W13-R04/R06).** Independent review of d623b71
  reopened F06's narrower selection-to-propagation window. A timer Stop barrier
  confirms the controller already selected its local expiry before a parent
  cancellation wins the standard child cause. The new read-only SelectedCause
  preserves that selected instance; core registers it even while child.Err is
  nil, and pre-dispatch errors retain it without a fabricated Result. First
  FinishExecution now preserves a selected cause before consulting child.Err.
  Controller binding/read shares the terminal mutex, with explicit binding/cancel
  race coverage. `evidence/selection-gap-local-red.jsonl` records four local
  failing tests/subtests (Driver/preparation and controller), followed by repaired
  race coverage. Historical 69d74f8 and d623b71 evidence remains archived.

Evidence of final repairs is the committed-source test suite and final-SHA V01,
V02, V03 and V04 logs listed in result.json. Built-in provider startup signatures and
Windows native/live execution are outside this core implementation; the delivery
does not claim those later contracts have passed.
