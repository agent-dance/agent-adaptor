# R040: managed-clone ancestor aliases

A formal skill manifest can retain the spelling used by `SyncProfile`, while
Dedicated `WithTools` obtains the canonical spelling of the same source profile.
The clone previously applied a lexical direct-child check to those different
spellings and rejected the SDK-managed skill before Driver dispatch. The public
zero-CLI counterexample records a manifest through an ancestor alias and then
clones the canonical source. Canonical Dedicated and aliased Native controls
already passed; aliased Dedicated failed before this repair.

Clone now holds both the source profile and its direct `skills` directory. For
every skill manifest entry it proves that the recorded profile parent and its
direct `skills` parent are respectively the same two filesystem objects. Both
final directory entries must be ordinary directories, not symlinks. Only after
that proof does a private copied manifest map use the held source spelling and
the original direct-child basename. No source manifest bytes, declared source,
key, source hash, fingerprint, canonical ownership rule, or Thread fingerprint
are rewritten. Wrong profiles with the same leaf target, matching skills targets
under a foreign parent, duplicate entries after relocation, wrong hierarchy,
and unproved relative-link source hashes remain errors.

The original compatibility proof and actual readlink check still apply to the
private view. Each subsequent source snapshot checks the held skills identity
before and after its tree walk, so replacing the skills child cannot inherit the
old proof. A retargeted ancestor alias is never used for subsequent content
reads. Existing no-follow checks, copied-source safeguards, original bytes and
observed modes, two independent bounded source snapshots, and the separate
shared retained-forest budget are unchanged. This remains a checked copy, not an
atomic snapshot against arbitrary hostile concurrent filesystem mutation.

Permanent regressions cover a formally generated alias manifest without source
writes, the two distinct parent-identity failures, duplicate aliases, hierarchy
and hash failures, and deterministic post-proof replacement. Public tests retain
real configured profile/skill methods and replace only Driver execution: Native
and Dedicated, Run and Stream, one materialization/injection/dispatch, source
manifest bytes unchanged, and Dedicated Close followed by cold ResumeOnly.
The original failing public diagnostic is also replayed byte-for-byte by an
external Go overlay; no Windows fixture was canonicalized or skipped.

The original Windows job observed Dedicated/managed and drift setup failures
with `invalid managed skill path`, plus an 8.3 temporary ancestor in its process
environment. It did not log the two failing per-test paths. The exact 8.3
expansion in that run is therefore an inference; the same-directory alias
mechanism is independently reproduced. Local macOS tests do not certify native
Windows reparse/ACL behavior or the Windows suite. The coordinator must run the
original complete Windows gate on the new integrated source. No provider or
authentication access is needed for this repair.

Central tools/profile documentation and CHANGELOG are owned by the coordinator.
This scoped delivery does not close final G05/B06/G06, live-provider validation,
or merge/release readiness.
