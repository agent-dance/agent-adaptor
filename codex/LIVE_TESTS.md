# Codex live tests

Normal `go test` runs do not invoke a model. Live conformance requires
`AGENT_ADAPTOR_LIVE_CONFORMANCE=1` and an installed, authenticated `codex` CLI.
The app-server smoke tests also require `-tags=codex_live`.

Live conformance and the app-server haiku/persistent/structured smoke tests use the model
selected by the CLI's `CODEX_HOME` profile. Set
`AGENT_ADAPTOR_CODEX_LIVE_MODEL` to override it with a model available to that
account. These tests explicitly request reasoning effort `low`. An unset model
override does not force a historical model name. Hermetic conformance keeps a
fixed model and an isolated home.

```sh
AGENT_ADAPTOR_LIVE_CONFORMANCE=1 \
  go test -count=1 -run '^TestCodexDriverConformance$' ./codex

AGENT_ADAPTOR_LIVE_CONFORMANCE=1 \
  go test -tags=codex_live -count=1 \
  -run '^TestAppServer(Haiku|PersistentTwoTurns|StructuredOutput)$' ./codex/appserver
```

The native structured-output conformance probe supplies an output schema and
the native source with `Streaming=false`. The Driver selects the transport
from the resolved request; this flag does not force structured output onto
exec. The temporary workspace is initialized as a Git repository so it also
satisfies preflight for exec coverage. The ordinary streaming probe uses
app-server. Both probes keep their full success assertions and report provider
failures, process outcomes, and the stderr tail before checking returned output.

App-server smoke tests close their Agent with an independent ten-second
cleanup context, including when the request or an assertion fails. Use a
disposable `CODEX_HOME` copy when testing authentication or native config
changes; the tests do not normalize credentials.

`TestAppServerStructuredOutput` sends a public `WithSchema[T]` request through
one Thread three times: Result before consuming Events, Events before Result,
then `WithSpawn`. It checks decoded values and conversation continuity, raw
stdout and terminal payload, transcript, usage, and healthy checkpoints. Ordinary
process events may be dropped while only Result is consumed: the first request
therefore establishes the visible spawn baseline. With prompt Events consumption,
the second request must add no spawn, and `WithSpawn` must add one.

The MCP override parser probe is separate and does not invoke a model, start an
MCP server, or require authentication. Point `AGENT_ADAPTOR_CODEX_CONFIG_CLI`
at a Codex binary to run it against an isolated temporary native config:

```sh
go test -count=1 -run '^TestCodexMCPOverrideCLIParser$' ./codex
```
