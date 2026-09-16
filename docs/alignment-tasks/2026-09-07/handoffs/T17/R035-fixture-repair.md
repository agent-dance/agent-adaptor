# T17 — R035 G05-F01 fixture environment isolation

Prepared base: `4290cb9237a89d0a3cbe2ce8dc0c4793dcc6d54b`.

The three legacy HOME fallback fixtures set a private HOME but inherited
`CURSOR_CONFIG_DIR`, `CURSOR_DATA_DIR`, and `XDG_CONFIG_HOME`. The G05 collector
correctly supplies a private XDG configuration root, which takes precedence over
HOME under the official Cursor resolver. Consequently the fixtures tested a
different directory from the one containing their test data.

`TestDetectModelFallsBackToCursorConfigFile`,
`TestGetProfileCloneCanShareNativeCursorAuth`, and
`TestCheckEnvironmentReportsConfigFileState` now reuse
`cursorPathTestEnvironment`: private HOME/USERPROFILE plus explicit clearing of
the legacy and official Cursor overrides and XDG_CONFIG_HOME. The existing
model/source, copied settings, AuthLink file identity, and environment-report
assertions remain unchanged. The official root precedence/selection matrix,
Native split clone tests, and Windows case-insensitive environment tests are
unchanged. No production file, public API, capability, collector environment,
assertion, or skip policy changes.

The exact common base reproduced all three failures with the unchanged G05
collector environment builder and all paid/golden gates set to zero. Targeted
checks after the fixture change pass under that same environment shape. Final
commit evidence includes the original full `go test -count=1 ./cursor`, the
entire Cursor suite and race with distinct private HOME/XDG/CURSOR roots and
sentinel models, closed-live-tag compilation/execution, and `go vet ./cursor`.
Only fake provider processes run. A separate external process watchdog bounds
each check; source snapshots, actual commands/environment, exit codes, named
test terminals, raw logs and SHA-256 hashes are retained in the external report.

Evidence: `docs/alignment-execution/2026-09-16/live-repair/handoffs/T17/g05-fixture-repair`.
The prior failed `final-f7aa53c/G05` and R030 reports remain unchanged. This repair
requires independent review and does not close the complete G05, native
platform, live-provider, or G06 gates. No centralized documentation or CHANGELOG
change is needed because public behavior is unchanged.
