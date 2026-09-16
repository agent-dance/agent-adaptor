# R033 Cursor native Windows gate supplement

The Windows collector's full T26 mandatory inventory now includes all eleven
Cursor directory/projection/ACL roots from R033 and the approved T17 follow-up. It requires each exact
package-qualified top-level PASS; missing, skipped and child-only results are
rejected. Existing mandatory entries, full T26 commands, supplemental selectors
and the skip policy remain unchanged.

An independent literal R033/follow-up inventory in the Python tests drives the actual
T26-V01 collector entry point with complete synthetic Go JSON and three negative
shapes per root. Before the inventory change, the regression failed; afterward
all eleven omissions, skips and substitutions are rejected. The test also prevents
any skip exemption for these roots or their descendants.

Only the three authorized CI files and this handoff change. Cursor's native
implementation remains with T17. Names were verified read-only against fixed
T17 commit `49ee11e3ebcfea997fd915581cd05dd4b870dfb5`; no provider code was imported into this worktree.
The original nine-name `6ab9273` audit and failing regression are preserved. A
second regression against the nine-root gate proves the two follow-up roots
(environment-name case folding and delivered-byte snapshot) also need explicit
mandatory entries. No public API, Go source, task metadata or golden changes are
involved.

The script tests prove evidence acceptance, not Windows semantics. Original T23
V01/V02 are rerun at the committed HEAD with paid/live/golden gates closed.
The coordinator must merge the accepted T17 implementation and run the complete
native Windows Actions matrix at final S. ACL protection, no-replace publication,
handle behavior and cleanup remain pending that actual native execution.

The maintained user-facing CI explanation is `.github/scripts/windows-validation.md`.
Central release notes may record stronger mandatory Windows evidence collection;
this supplement does not claim native acceptance or close T23-AC05 by itself.
