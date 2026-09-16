# R033 Cursor native Windows gate supplement

The Windows collector's full T26 mandatory inventory now includes all nine
Cursor directory/projection/ACL roots named by R033. It requires each exact
package-qualified top-level PASS; missing, skipped and child-only results are
rejected. Existing mandatory entries, full T26 commands, supplemental selectors
and the skip policy remain unchanged.

An independent literal R033 inventory in the Python tests drives the actual
T26-V01 collector entry point with complete synthetic Go JSON and three negative
shapes per root. Before the inventory change, the regression failed; afterward
all nine omissions, skips and substitutions are rejected. The test also prevents
any skip exemption for these roots or their descendants.

Only the three authorized CI files and this handoff change. Cursor's native
implementation remains with T17. Names were verified read-only against fixed
T17 commit `6ab9273`; no provider code was imported into this worktree.
No public API, Go source, task metadata or golden changes are involved.

The script tests prove evidence acceptance, not Windows semantics. Original T23
V01/V02 are rerun at the committed HEAD with paid/live/golden gates closed.
The coordinator must merge the accepted T17 implementation and run the complete
native Windows Actions matrix at final S. ACL protection, no-replace publication,
handle behavior and cleanup remain pending that actual native execution.

The maintained user-facing CI explanation is `.github/scripts/windows-validation.md`.
Central release notes may record stronger mandatory Windows evidence collection;
this supplement does not claim native acceptance or close T23-AC05 by itself.
