# T04 / W02 hosted profile lifecycle

Source-Internal-Commits: ca571fbed8454e482560b481a3aa6853e9d174df.
Implements W02-R01/R02/R03/R04/R05/R07/R08; R003 assigns W02-R06's resolved-resource compatibility closure to T06. This delivery does not close W02 or claim native Windows / paid provider conformance.

## Merge into docs/tools.md: profile lifetime and cleanup

When non-empty `WithTools` is combined with explicit `WithProfile(profile.Dedicated(source))`, supported built-in Drivers execute in a persistent sibling clone. The source must already exist and is read without calling a provider initializer. Its canonical source path, Driver type, and all four identity fields select a private namespace:

`<canonical-source>.hosted-tools/v1/<sha256-framed-key>/profile`

Thread keys never become directory names. Source path aliases resolve to the same ownership partition. Empty identity is a real shared partition; another Agent using it gets `profile.ErrInUse` immediately, including within the same OS process. Different ID, tenant, profile or name fields select distinct partitions.

The first use copies settings, MCP and skills once and shares only declared provider auth files through the existing AuthLink policy. A normal close and a new Agent with the same configuration retain provider session files, and renew the hosted gateway URL, bearer token and environment carrier. Later source configuration changes do not overwrite the existing clone. New desired resources continue through the Driver's existing materialization pipeline.

Native, Default, CloneFrom and CloneNative continue using per-Agent temporary hosted clones; Close removes those clones. Merely setting a provider environment variable does not opt into persistence. Explicit SyncProfile still targets the host-selected source and remains independent of WithTools execution cloning. Inspect/ProfileState acquire no hosted claim or gateway.

Example: create Agent A with Dedicated+Tools+ThreadStore; run `a.Thread(key).Run`; close A successfully; create Agent B with the same Driver/source/identity/Tool revision/store; `b.Thread(key).Run` can read the retained local provider session. The regression fixture writes an unpredictable nonce in `projects/<provider-session-id>.jsonl`; resumption succeeds only after reading that file and matching its checkpoint nonce. Both Run and Stream cover this path.

## Merge into docs/tools.md and docs/public-errors.md: crash and filesystem boundary

The SDK uses a persistent 0600 owner.lock with an exclusive OS lock, strict versioned ownership records and private directory permissions. It never unlinks/truncates/replaces owner.lock. Supported filesystem classes are local ext4/XFS/tmpfs, APFS/HFS+, NTFS/ReFS; unsupported/remote/unknown filesystems fail closed with `profile.ErrUnsupportedFilesystem`. Unix checks owner/mode/link identity. Windows uses protected current-user/System DACLs, verifies file IDs/reparse points, and holds a no-delete-sharing CreateFile handle with LockFileEx. Native Windows verification belongs to T26.

`profile.ErrUnsafe` reports unverified ownership, paths, permissions or control records. `profile.ErrRecoveryRequired` means a previous active generation did not finish a clean shutdown. Kernel lock release on process exit alone does not prove its provider children exited. A new Agent refuses the dirty generation without changing state, sending a prompt or modifying the Thread store. There is no TTL-based takeover.

Offline recovery: stop all hosts using the namespace; prove and wait for every provider process tree to exit through host process-management records; back up the complete namespace, including transcripts; acquire the same exclusive lock and verify markers/permissions; remove only proven-owned MCP projections using provider path, owner and rendered fingerprint; verify no old gateway remains usable; atomically change active to ready and unlock. If any proof is unavailable, retain active and the backup. Alternatively, once all users have stopped, move the entire namespace to an isolated backup and start a new namespace; this loses local session continuity and uses the existing resume-rejection policy. Never delete only owner.lock. Permanent historical-data deletion belongs to host maintenance after the same stop/backup/exclusive-ownership checks; Agent.Close is not a history deletion API.

## Merge into API reference / Agent.Close documentation

Close first closes admission and cancels runs. It preserves the first provider close to unblock cancellation, drains admitted runs, closes late-started writers, removes proven-owned hosted MCP projections, closes the gateway, then deletes temporary clones or releases persistent claims. Any failed phase remains retryable with a fresh context. A cleanup error before OS unlock retains exclusive ownership. After a successful OS unlock, a later handle-close failure retains only the pending cleanup phase: retry never touches the successor Agent's state. User-modified hosted entries are preserved under the existing rendered-fingerprint rule; their ownership records are dropped and gateway shutdown revokes the original token.

Already deleted historical transcripts cannot be reconstructed from resume IDs. Continue-or-start retains its existing single safe resume-rejection fallback; ResumeOnly rejects and does not replace the healthy stored record. The opaque host key is unchanged.

## Compatibility handoff to T06 under R003

T04's error-returning claim validates the currently observable private profile on every use. The materialized snapshot preserves all known config roots, exact manifest-referenced resources, actual file bytes/modes, strict JSON numbers and unknown configuration fields. Only the precise hosted MCP entry with matching owner/provider/path/rendered proof is removed from its pure baseline view. New ordinary MCP entries now record rendered_fingerprint for later projection proofs. Authentication content, session files and volatile ownership controls never enter compatibility hashes. Unknown config materialization can conservatively reject ResumeOnly, and unproven skill symlinks reject before execution.

The existing claim hook runs before `resolveRun` resolves/injects skills; it cannot safely observe newly resolved dynamic source contents. No second skill-resolution path was added. T06 must move the final immutable per-run snapshot after the sole resource resolution/injection and before Thread fingerprint/Driver dispatch, allow IO errors to propagate, and implement verified ordinary-MCP/skill projection. It must not treat the current shared selection cache as the final per-run compatibility snapshot. Available private hooks are `Claim.Validate`, `Claim.SeedPaths`, `Claim.Dir`, `mcpruntime.HostedCompatibilityBaseline`, `mcpruntime.ReadHostedManifest`, and the existing error-returning materialized fingerprint helper. Preserve current full fingerprint dimensions and real process URLs/env payloads.

Suggested T06 fixture: a dynamically resolved skill source changes after one run while its declaration/path fingerprint stays stable; both first materialization and subsequent managed symlink states must compare the actual resolved source contents, while unknown external files still affect compatibility. No dynamic skill declaration is needed for T04's main session-file cold-resumption guarantee.

## CHANGELOG fragment

- Retain built-in provider session files for explicit Dedicated profiles with hosted Tools in an identity-partitioned, exclusively owned sibling clone; temporary selections keep their previous cleanup lifecycle.
- Reject concurrent or unsafe profile adoption and unclean active generations with profile-owned errors. Keep failed Close phases retryable and never overwrite a successor generation after unlock.
- Preserve private source profiles and renew hosted credentials across clean Agent reconstruction. Do not claim recovery of transcripts already removed by older versions.

Run godoc also follows frozen C02: after Driver.Run entry, failures return nil and RunError carrying the available Result/cause; startup errors remain ordinary errors.Is/As-compatible wrappers. Its implementation belongs to same-batch T05 and was not changed here.

Public declarations added only in `profile/errors.go`: ErrInUse, ErrUnsafe, ErrRecoveryRequired, ErrUnsupportedFilesystem. No root/SPI API declarations or golden changes. Dedicated/profile package and Agent.Close godoc are updated locally.

## Dependency decision

Existing golang.org/x/sys v0.41.0 becomes direct, without version changes. It provides official OS locks, identities and Windows ACL calls needed for validated-handle ownership. The Go project maintains its version/security/issue lifecycle. Imports remain confined to private hostedprofile platform files. A new flock package adds a path opener that would still require a separate verified-handle boundary, so no new dependency is added. No shared engine lock is repurposed as an Agent ownership lease.

## Rejected source behavior and validation boundary

Do not import internal's bare sibling directory, SDK/SessionKey abstractions, or ID-only fake resume proof. Do not expand the temporary directory deletion whitelist. Do not adopt user-controlled markers by chmod, steal a stale lock, or replay a possibly delivered prompt.

Actual local macOS package/race runs and Windows/Linux cross-compilation are recorded in result.json and evidence after the production commit. Fixtures use only the Go test binary and local loopback; all live/E2E/API-golden environment gates remain zero. No paid CLI, native Windows/Linux runtime, or official provider session conformance is claimed here. G01 must integrate these public documentation/CHANGELOG paragraphs before accepting the batch; T20/T26–T30 retain their independent verification responsibilities.

## R008 integration fixture repair (attempt 2)

The hermetic `e2e` package now explicitly creates its isolated Cursor source and runs the existing real Go provider subprocess through both Dedicated and CloneFrom selections. Each retains MCP discovery/list/call, unauthenticated rejection, normal Result/Transcript output, two same-Agent turns and a third checkpoint continuation after Agent reconstruction. Dedicated reuses the same execution namespace and reads the first two turns from its retained session artifact; temporary CloneFrom deletes the old hosted clone and starts the replacement in a new directory. This synthetic Cursor protocol fixture does not establish native provider recovery; the root unpredictable-session-nonce Run/Stream fixtures remain unchanged.

Both selections verify an unchanged source, removal of the owned MCP projection, closure of the old endpoint, endpoint/token/environment-carrier rotation, and rejection of the old token by the replacement gateway. The first token exists only in a private file under the test's isolated temporary root for that rejection request; observations/logs include only its hash. No production profile/runtime behavior changed in this repair. The failed original source-missing fixture and committed-head full `go test -count=1 ./e2e` results are included in the attempt 2 evidence, alongside the unchanged required package and five-repeat race commands.
