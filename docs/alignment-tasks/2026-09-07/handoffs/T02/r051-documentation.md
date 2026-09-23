# R051: MCP Tool wire compatibility

Public `Definition.Descriptor` and `Definition.Invoke` retain their original
schema/value bytes and local validation. The private hosted MCP catalog projects
outputs without a literal root `"type":"object"` into a required `result` object
property; successful `structuredContent` contains the entire original value in
`{"result": value}`. This includes scalar, array, null, boolean-schema and union
outputs. The single text block remains the original validated JSON value.

Explicit object outputs retain their original value and text. At either input
or output wire schema root, direct boolean `properties` values use equivalent
`{"allOf":[true|false]}` objects for older clients. Deeper schemas and literal data
are untouched. Otherwise compatible object schema bytes and fingerprints stay
unchanged. Changed input/output wire schemas enter the existing fingerprint;
handler `Revision` semantics remain unchanged.

Wrapped schemas retain their original resource boundary: absolute root `$id`
is preserved; absent/ordinary relative IDs resolve against a deterministic
private HTTPS identity with the complete schema SHA256 split over two host
labels, so `../` and `/` cannot erase the namespace. An explicit `//authority`
uses that author's authority under HTTPS. Fragment references, nested IDs,
anchors, dynamic references, defaults/examples/const data and numeric precision
are not rewritten. External references remain forbidden by local validation.
The existing maximum response size applies to original validated output bytes;
the structured envelope adds exactly 11 bytes. Errors and cancellation produce
no structured success. No public API, dependency or execution pipeline is added.

Regression coverage includes original-versus-projected validation with a
network-denying compiler, resource independence, local/HTTP byte preservation,
all JSON output types, direct boolean input/output properties, original schema
immutability, fingerprints, bounds and private failure paths. Actual loopback
HTTP negotiates MCP 2025-06-18 and emits full list/call evidence for the pinned
official CodeBuddy 2.157 schema parser, loaded offline as ten pure modules.

Owner validation uses Go 1.27.1: complete `./tool ./internal/toolruntime` tests,
the same packages with race, complete root tests, and scoped vet. Live,
E2E and API-golden-update environment gates remain zero. This is local protocol
compatibility evidence, not provider/live, native Windows/Linux or final G06
acceptance. Final committed-SHA results and original red evidence are external
under `live-repair/repairs/R051-T02-MCP-output/owner`; root owns central usage
documentation and CHANGELOG integration.
