# T17 — R038 T30-F05 synthetic marker wording

Prepared base: `6b6f101f3b42ba40e0589e729b73adc0827013df`.
Scope is only `cursor/alignment_live_isolation_test.go` and this handoff.

The failed Native positive control on `8db5dbf` logged a nil Go error and one
host tool callback, but not which combined predicate failed. Its original cause
and missing field remain unknown. A separately authorized single Native run
using the unchanged prompt/model/resources read both resources and returned both
nonces. Its formal assistant initially described the test tokens as sensitive
credentials. That new observation supports clarifying the fixture's meaning;
the later pass does not replace the original failure or establish its cause.

Native and selected skill/tool descriptions and the two prompts now describe the
values as public synthetic conformance markers, safe to echo verbatim and with
no authentication or authorization purpose. This is true of the random values
created solely for the test. Their actual values remain only in skill content
and tool results, never in a prompt. Tool/resource identities, JSON result shape,
model, profile selection, policy, timeout and lifecycle remain unchanged.

The two combined failure conditions remain identical. Their messages now name
safe booleans for result presence, each exact marker check and, on the selected
path, the expected subagent reply, alongside the error type and callback count.
They do not print marker values, Raw, Transcript or credential-bearing errors.
All exact Text, real tool callback, native HTTP count, source-file equality,
no-native-marker-disclosure and typed completed MCP/Subagent assertions remain.
No production resolver/parser, public API, skip policy or central document changes.

External evidence: `docs/alignment-execution/2026-09-16/live-repair/repairs/R038-T17`.
Validation is a closed-gate live-tag compile and an explicit before/after audit
of every existing assertion and protected fixture setting. No mirror wording
tests, model calls or credential reads are added. Native and selected resource
behavior still require the original full live fixture on the integrated new S,
plus all original same-S gates; this local change does not close those gates.
