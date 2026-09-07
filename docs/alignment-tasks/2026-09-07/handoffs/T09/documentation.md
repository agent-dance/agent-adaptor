# T09 documentation fragment for G02

## Public behavior and docs/a2a.md insertion

Insert after the delegation artifact/result projection description:

`DelegationRequest.IncludeRemoteArtifacts` now also opts into ordered `DelegationArtifact.Parts` on each `DelegationArtifactCreated` event. Each event contains exactly that update's parts: `DelegationEvent.Append=false` replaces the artifact content; `Append=true` appends the supplied parts for that ArtifactID. `LastChunk=true` completes the artifact update sequence, not the delegation. Empty final chunks are retained. Text, Data, URL/file references, inline bytes, filename, media type and part metadata retain their formal A2A representation; delegation never interprets provider or host DataPart schemas to invent artifact semantics.

For example, an update carrying `[Text("A")]`, followed by `Append=true` with `[Text("B")]`, delivers those two individual payloads and yields one final remote artifact with `[Text("A"), Text("B")]`. A later `Append=false` update replaces that content. Historical Task replay only restores artifacts not already updated live and does not replay answered questions. Explicit GetTask recovery continues to retain complete extensions, reject lagging prefixes, and report incomparable content with the existing safe conflict diagnostic.

The final `DelegationResult.Artifacts` projection remains compact even when full content is requested; full final parts remain in `RemoteArtifacts`. Both final and live full projections follow `IncludeRemoteArtifacts`. With its default false value, artifact events contain no Parts or original artifact protocol Raw; when content was omitted, Event.Raw contains the explicit safe marker `{"parts_omitted":"remote_artifacts_not_requested"}`. Existing compact ID, name, description, URI, media type and artifact metadata remain unchanged. `RemotePart.Raw` means inline file bytes; artifact protocol Raw remains a separate opt-in field and is never merged into Parts or metadata.

Local full-content opt-in cannot enable the remote bridge's ExposurePolicy. Content withheld there remains absent, and enabled diagnostics arrive with the remote bridge's existing redaction. The receiver preserves explicit Data/file/metadata values rather than guessing which schema fields are safe to expose. The default does not acquire formerly hidden part text, data, inline bytes, part metadata, or an original protocol payload through the new fields.

`DelegationPolicy.MaxArtifactBytes` is applied before artifact event publication as well as at final projection. The count covers cumulative part text/URI/media type/filename strings, inline bytes, JSON Data, part/artifact metadata, artifact protocol Raw, and extension strings. URL contents are not fetched or estimated. Repeated metadata on appended updates is counted once in the cumulative view. Zero keeps the existing absence of a byte ceiling. Non-JSON-encodable Data or metadata fails closed even without a ceiling. Oversized/invalid updates produce `DelegationStreamDropped` with safe `reason=artifact_too_large` (and `max_bytes`) or `reason=artifact_invalid`, without content. Appends after a rejected update remain incomplete until a replacement or authoritative snapshot; they cannot silently resume a partial artifact. An oversized/invalid final artifact preserves the existing failure path (`artifact_too_large`) or returns `artifact_invalid`. Interrupted results retain other permitted partial artifacts while withholding rejected ones.

`MaxArtifacts` still limits only compact final Artifacts, including explicit zero. Live updates and opt-in full RemoteArtifacts remain complete. A truncation now emits a safe `DelegationStreamDropped` with `reason=artifact_result_limit`, `omitted_count`, and `max_artifacts`.

Mapped events, terminal buffering, EventBus replay and each subscription, Service observers/hooks, final results, and all Service result accessors own independent nested copies. JSON-shaped maps/slices/bytes and typed/named containers, arrays, pointers and exported struct fields are copied without a JSON round trip. A failed JSON encoding never falls back to sharing mutable source data. Callers must not mutate an input concurrently with Publish; subsequent mutation is isolated. Backpressure diagnostics clear Parts, status parts, text, errors and other semantic payloads.

## CHANGELOG entry

- Added opt-in real-time A2A artifact Parts and Append/LastChunk, preserving actual delta updates and complete final recovery without duplicate aggregation.
- Fixed nested mutable data sharing in delegation mapping, EventBus subscriptions/replay, terminal buffering and Service/result projections.
- Kept default compact results and remote ExposurePolicy boundaries; default artifact event Raw now reports omitted content instead of forwarding the original protocol payload.
- Applied cumulative artifact byte limits before live publication, including metadata/protocol Raw; invalid unencodable artifacts are rejected with safe diagnostics. Compact final count truncation is now observable.

The Raw default and expanded byte accounting are deliberate tightening of exposure/size semantics and must be called out in the release notes; they are not an unconditional content expansion.

## Public declarations and local godoc

Added only leaf DTO fields in `hosttools/a2adelegation/types.go`:

- `DelegationArtifact.Parts []RemotePart` with JSON tag `parts,omitempty`.
- `DelegationEvent.Append bool` and `DelegationEvent.LastChunk bool`.

Updated godoc for these DTOs, `RemotePart`, `IncludeRemoteArtifacts`, `MaxArtifacts`, `MaxArtifactBytes`, and EventBus.Publish isolation. No root or Driver declarations changed, so neither root nor Driver AST golden needs updating. No new top-level dependency was added: the standard library reflect copier handles decoded containers without protocol parsing or a runtime framework.

## Fixed source comparison

Read internal commit `3253009054681a499fd693329486d0fe3279d843` with git show only. Adopted the additive Parts concept. Rejected its shallow Data sharing, omission of Append/LastChunk, unconditional live content exposure, and terminal-only byte policy. The existing T03 historical/live/query recovery contract remains the baseline and is exercised rather than replaced. No T18 publisher, capability relay, active budget, or same-batch T06 symbol is consumed.

## Verification boundary

Initial baseline fixtures failed for missing Parts/update flags, cloneRemoteParts.Data aliasing, and EventBus publisher/subscriber/replay aliasing; the original failure log is retained outside the source commit. Implemented fixtures cover mixed Text/Data/URL/inline-byte parts, same-ID replacement and Append, empty LastChunk, old-question replay, complete recovery regressions, byte/count limits, unencodable data, all mutable event payloads, terminal buffering, concurrent Service accessors, and a real local bridge/client loopback across remote metadata exposure × local full-artifact opt-in.

Final task-required commands and exact counts/hashes are recorded in result.json after the source commit is fixed. Local verification is macOS arm64 using fake/local providers and loopback only, with all live/E2E/API-golden update gates disabled. This worker does not claim independent Linux/Windows, paid live conformance, G02 acceptance, or W08 final closure. G02 must merge this fragment into docs/a2a.md and CHANGELOG before accepting the batch.
