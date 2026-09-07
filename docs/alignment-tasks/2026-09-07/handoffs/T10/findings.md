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

Evidence of final repairs is the committed-source test suite and final-SHA V01,
V02 and V03 logs listed in result.json. Built-in provider startup signatures and
Windows native/live execution are outside this core implementation; the delivery
does not claim those later contracts have passed.
