# Full Profile Integration

This showcase materializes the broad profile surface in one controlled
environment: skills, MCP, hooks, instructions, sub-agent definitions, and
allowlisted provider config. It collects filesystem and local probe evidence;
a real model call is optional.

## Architecture

```text
fixture builders (MCP server + hook command)
  -> ProfileResources
  -> dedicated or native provider profile
  -> Admin.SyncProfile
  -> filesystem / provider / MCP / hook probes
  -> optional sdk.Run
```

## Prerequisites

- Go toolchain
- An installed Codex, Claude Code, or Cursor Agent CLI
- Existing CLI authentication only when using `-run=true`

## Provider Support

All three built-in providers can materialize the common resource model. Their
native layouts and probe surfaces differ, so the JSON report records which
evidence was available instead of treating missing provider probes as success.

## Setup And Run

The default `dedicated` mode avoids modifying the native provider profile:

```bash
go run ./examples/showcases/full-profile -agent=codex -run=false
go run ./examples/showcases/full-profile -agent=claude -run=false
go run ./examples/showcases/full-profile -agent=cursor -run=false
```

To include a real invocation:

```bash
go run ./examples/showcases/full-profile -agent=codex -run=true
```

Use `-profile` and `-workspace` for explicit locations. `-probe=false` skips
local provider probes. `-profile-mode=native` is intentionally opt-in because it
may update the current user's provider profile.

## Expected Evidence

The JSON report identifies the effective profile, synchronized resources,
redacted authentication evidence, generated MCP and hook binaries, provider
probe results, and optional `RunResult` evidence. Probe failures remain visible
as failures in their individual records.

## Cleanup

Automatically created profile and workspace directories are retained so their
contents can be inspected. Their paths are printed in the report; remove them
after inspection. Explicit paths are always owned and cleaned up by the caller.

## Security Notes

- Prefer `dedicated`; `native` can alter real CLI settings.
- Authentication evidence is redacted, but the generated profile can still
  contain credential links or sensitive settings.
- Hooks and MCP servers execute local programs. Only materialize trusted input.
- Do not expose the generated MCP endpoint outside a trusted local environment.

## Known Limitations

- Provider CLIs expose different inventory and prompt-debug commands.
- Local probes prove materialization and visibility, not model compliance.
- The showcase is deliberately broad and is not a copy-paste starter recipe.
- Production profile ownership, locking, policy review, and secret rotation are
  host responsibilities.

See the [profile resource matrix](../../../docs/profile-resource-provider-matrix.md)
and [API reference](../../../docs/api-reference.md).
