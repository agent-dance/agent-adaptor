# R038 / R039: managed skill clone and copied-source safety

`SyncProfile` can create SDK-managed skill symlinks in a Native profile. A later
`WithTools` invocation now safely clones these into ordinary directories instead
of rejecting the SDK's own materialization. IncludeSkills remains enabled; the
source profile is never modified by this clone.

The exception is confined to direct `skills/<runtimeName>` links proved by the
formal reconciler manifest, source hash, and fingerprint. The fingerprint proves
the source path, not its contents. The clone holds directory roots, checks object
identity and type, reuses `hostedprofile.ReadResourceFile`, rejects nested links
and special nodes, and compares two bounded snapshots before writing. Duplicate
physical ownership, changed proof, reserved marker conflicts, and conflicting
destinations fail explicitly. This is not an atomic snapshot against arbitrary
hostile concurrent filesystem mutation; retained handles and identity checks
bound normal replacement races. Native Windows reparse/ACL behavior still needs
the separate platform gate; local macOS race checks do not prove it.

A fresh copied tree preserves ordinary file bytes and observed permission bits,
including unknown regular attachments. The source marker is published after the
copy and identity checks. The source manifest is not copied: its absolute paths
would refer to a staging directory after persistent seed publication. The normal
reconciler recognizes the existing source marker and writes the execution
profile's manifest. Same-source repeat clones preserve actual destination bytes,
modes, added files, and removed files; they do not refresh from the cache. Actual
copied resources continue to enter the full compatibility fingerprint.

R039 closes a separately reproduced unsafe overwrite: when a copied tree's
marker names source A and a later same-name declaration requests source B, being
inside ManagedRoots does not prove the old tree can be deleted. Compatibility
projection and formal reconciliation now return `profile.ErrUnsafe` before
replacement. Same-source reconciliation and proved symlink replacement remain
supported. Existing conservative copied-tree prune rejection remains in force.
This behavior also applies to the SDK's prior copied fallback materializations.

Skill clone reads are bounded to 64 MiB of regular bytes and 20,000 entries per
skills tree (and a 4 MiB profile manifest), including all copied attachments.
These are explicit failures, not truncated or silently omitted resources. A
failed copy does not delete unknown destination contents or adopt an unmarked
partial tree on retry. Non-skill resource/auth copy behavior is unchanged.

Core keeps ProfileReporter Go errors wrapped with their original identity, and
preserves an embedded AgentProfile.Error message for reporters using that view,
in both temporary claims and persistent seed initialization. CodeBuddy's original
Go error restoration is the separate T15 change; this implementation requires no
new peer API and adds no public declarations or API-golden changes.

Merge these details into tools/profile documentation, public error semantics,
and CHANGELOG under R038/R039. Zero-CLI regression coverage includes real
configured CodeBuddy profile/skill methods with only Run replaced, Native and
Dedicated, Run and Stream, Dedicated cold ResumeOnly, source integrity, one
materialization/injection per call, runtime environment, rebased manifest paths,
copied content/mode/attachment drift, real ManagedRoots rejection, original
errors, and safety limits. This local delivery does not close T28 live, G05/B06,
Windows/Linux native gates, or merge/release readiness.
