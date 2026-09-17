# Codex 0.153.4 builtin skill bootstrap and cold resume

The Luna T29 diagnostic on ca315b33583125d67b16e54e372b6a1a985c9b55
observed a healthy first turn, then `run fingerprint changed` before the cold
ResumeOnly Driver started. It did not record per-file changes. A separate
zero-CLI counterexample uses the real configured Codex profile/session contracts
and replaces only Driver.Run: installing the official builtin skill bundle in
that first run causes the same rejection. Removing only that bundle restores
the previous materialized fingerprint. The no-bootstrap control resumes. This
proves a matching mechanism, not a retrospective trace of the live filesystem.

The official source is openai/codex rust-v0.153.4, commit
3d2ee51ca2d5db578f328aa75e20aa22c0197c9a. `codex-rs/skills/src/lib.rs`
installs embedded samples under `skills/.system`; `HostSkillsService::new` in
`codex-rs/ext/skills/src/host_service.rs` invokes installation when enabled.
There are 59 asset files (385,979 bytes), 26 descendant directories, and a
separate marker: 87 snapshot entries including the `.system` root. The marker
is `91663ef126b94ab1`, independently calculated with the official sorted v1
Rust hash. A matching marker alone is insufficient: the installer skips refresh
when it matches, so modified files could survive with that marker.

The repair reuses the existing bounded resource snapshot, which has already
checked ordinary file types and read actual bytes and modes. One pure function
recognizes only the complete ordered path/type/content/mode digest of that
bundle. It removes those proved entries from the compatibility view. Source
files, declarations, the parent `skills` directory, and everything outside the
exact `.system` subtree are unchanged. A managed `.system` source projection is
not eligible. Unknown versions, missing or added entries, modified content or
marker, and permission drift retain their original fingerprint. No new tree
scanner, resolver, initialization process, provider call, public API, or second
compatibility hash is added. Original limits and error propagation still apply
before normalization.

Mode proof is deliberately strict. POSIX bundle directories must be 0755 and
files 0644; Windows must expose directories as 0777 and writable regular files
as 0666. Only after that check are modes encoded identically for the proof
digest. A nonstandard Unix umask is not guessed: differing modes remain hashed
and may conservatively reject the initial cold continuation. Readonly/mode
changes are never normalized away. `skills` parent presence and permissions
remain independent fingerprint facts; this repair does not manufacture or
ignore a missing parent. Local platform-parameter tests are not native Windows
validation.

The source snapshot is still closed before Thread coordination and Driver.Run.
Preparing the bundle by invoking the CLI earlier would require an extra process
and new preparation semantics; the official CLI exposes no dedicated builtin
skills initialization command. This repair does not introduce that behavior.

Compatibility migration is narrow: an old record whose fingerprint already
included this complete bundle may be rejected by ResumeOnly after the change.
Its healthy state and native session remain intact. Empty/unaffected hashes are
unchanged. Existing continue-or-start behavior remains the sole atomic route to
replace an incompatible record after a new healthy checkpoint; there is no
legacy-hash fallback or automatic replay added here.

Permanent tests use a compact official manifest of hashes, not vendored asset
bodies, and exercise complete proof, all-platform mode representations, unknown
resources, parent preservation and the affected old-record boundary. Public
Run/Stream tests use real configured Codex profile methods with fake execution
to verify unchanged cold continuation and rejection of actual configuration,
unknown bundle, skill, marker, permission and parent-removal drift without
checkpoint mutation. The original public counterexample is replayed unchanged
against the full external official assets. No native credentials or provider
requests were used. Central AGENTS, contracts and CHANGELOG remain coordinator
owned; new integrated native/live gates are still required.
