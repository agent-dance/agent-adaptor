# Managed Profile Resources

This showcase demonstrates a host-managed agent environment: binding-level
profile resources establish the normal operating policy, while one run replaces
selected resources for an incident workflow. Both runs use an isolated cloned
profile and a temporary workspace.

## Architecture

```text
host
  -> AgentBinding defaults (identity + cloned profile + profile resources)
  -> Admin.ProfileSnapshot / SyncProfile
  -> sdk.Run with binding defaults
  -> sdk.Run with per-call ProfileResources override
  -> selected local agent CLI
```

The SDK materializes skills, agents, hooks, instructions, and allowlisted config
patches into the selected provider's real profile shape. The adapter remains
responsible for provider-specific translation.

## Prerequisites

- Go toolchain
- An installed and authenticated Codex, Claude Code, or Cursor Agent CLI
- Permission to create temporary directories

## Provider Support

Codex, Claude Code, and Cursor Agent all support this example. The exact files
and supported config patches differ by provider; inspect the emitted profile
snapshot instead of assuming byte-for-byte parity.

## Setup And Run

From the repository root:

```bash
go run ./examples/showcases/managed-profile -agent=codex
go run ./examples/showcases/managed-profile -agent=claude
go run ./examples/showcases/managed-profile -agent=cursor
```

Use `-command`, `-model`, or `-timeout` when the environment needs an override.
Add `-keep-workspace` to retain the generated workspace and profile for manual
inspection.

## Expected Evidence

The command prints JSON containing:

- the selected agent and isolated profile paths;
- profile snapshots before and after synchronization;
- binding-default and per-run resource summaries;
- two distinct `RunID` values and their assistant output.

A transport error exits non-zero. A completed run with `RunResult.Failure` or a
non-zero provider exit code also exits non-zero.

## Cleanup

Temporary files are removed automatically. When `-keep-workspace` is set, remove
the printed workspace directory after inspection.

## Security Notes

The clone links supported native authentication files rather than copying OAuth
refresh tokens. Treat the clone as credential-bearing state, do not publish it,
and do not use untrusted skill, hook, instruction, or config input. The example's
hooks are disabled and never execute.

## Known Limitations

- It uses an in-process skill set and does not demonstrate a remote catalog.
- It demonstrates replacement semantics, not a general merge UI.
- Provider profile formats and supported patches are intentionally different.
- The temporary in-memory host state is not a durable production store.

See the [usage guide](../../../docs/usage-guide.md) and
[profile resource matrix](../../../docs/profile-resource-provider-matrix.md).
