# T16 R035 — MERGE-F03

The child-role table's 128-identity limit previously returned before checking an already-known child. At 127 entries, contradictory formal role metadata marked that child conflicted and left its pending spawn Interrupted; at 128, the same evidence was ignored and the old role incorrectly produced Completed.

The capacity guard now rejects only new identities. Existing identities still use the unchanged role/conflict merge: matching replay remains idempotent, and a conflict cannot be cleared by later matching metadata. A 129th identity remains untracked with the existing safe unavailable notice. Attribution still needs both the formally related child and the current collab receiver, plus an unambiguous resolved catalog key. No public API, protocol schema, generated code, dependency or checkpoint behavior changes.

Provider godoc now states this pending-spawn boundary. The coordinator should include the corresponding bug fix in CHANGELOG and retain the AGENTS observation-truthfulness rule; this is restoration of that contract, not a new capability. Root/shared files are outside this worker's writes.

The regression sends formal notifications through runState at both 127 and 128 entries. It covers matching replay and repeated terminal, conflicting primary/source roles with sticky conflict after matching replay, admission of the 128th identity versus rejection of the 129th, exact Started/Completed or Started/Interrupted facts, canonical attribution, and no-collab/no-admitted-child negative controls. The independent review fixture remains unchanged outside the source tree; author re-execution is not independent acceptance.

The preserved pre-fix run has 14 named tests/subtests: 9 PASS, 5 FAIL (three failing leaves plus two parents), zero SKIP. Its JSONL SHA-256 is 876bcd8921bbfce89e348086455c29a8620b3bb7d7b8403a8f2ac7b5314a53a1. The original independent report and 127-pass/128-fail log remain immutable. Exact final-SHA original T16 checks, targeted race and evidence hashes are in the external capacity-repair/result.json. This repair does not perform or close native/live, final platform or combined-S gates.
