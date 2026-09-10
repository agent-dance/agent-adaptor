# CopilotKit dependency security patch — 2026-09-08

The `Go` workflow at commit `ec329a6` failed its CopilotKit production dependency
scan in [run 34212287457](https://github.com/agent-dance/agent-adaptor/actions/runs/34212287457).
The original failure log is retained in the execution evidence. Scans from other
dates or with different development/production scopes are separate observations.

## Dependency decision

The example retains CopilotKit 1.63.2, AG-UI client 0.0.57, Next
16.3.0-preview.8 and React 18.3.1. No application or SDK API changes are needed.
Existing override slots receive these updates:

| Dependency | Previous | Selected |
| --- | --- | --- |
| DOMPurify | 3.4.12 | 3.4.15 |
| fast-uri | 3.1.5 | 3.1.7 |
| Hono | 4.12.32 | 4.13.7 |
| Mermaid | 11.16.0 | 11.16.1 |
| qs | 6.15.3 | 6.16.0 |

Additional selectors constrain patches to the existing dependency major:
`nanoid@^3.0.0` → 3.3.18, `@hono/node-server@^1.0.0` → 1.19.17,
`body-parser@^1.0.0` → 1.20.8, `body-parser@^2.0.0` → 2.3.0,
`uuid@^11.0.0` → 11.1.1, and Phoenix → 1.8.13. Runtime's uuid 10 dependency
is not forced to uuid 11. The follow-up native CI scan also identified
[the sharp/libheif advisory](https://github.com/advisories/GHSA-rgj7-g3m4-5g8c).
`sharp@^0.35.0` is fixed to 0.35.4, within Next's declared `^0.35.3` range;
its matching native image packages update with it. The framework version stays
unchanged. Development dependency patches use `brace-expansion@^1.0.0` → 1.1.18,
`brace-expansion@^5.0.0` → 5.0.9 and `js-yaml@^4.0.0` → 4.3.2.
These new selectors satisfy the affected parent ranges.
The existing global qs override is an explicit exception: both the old override
and 6.16.0 exceed Express's declared `~6.14.0` range. Actual installation, lint
and production build remain required; semver alone cannot establish compatibility.

All resolved package URLs remain on `registry.npmjs.org`; integrity entries are
retained and checked by fresh installation. Package metadata and advisory fixes
are checked against the official registry and [GitHub Advisory Database](https://github.com/advisories).
No new Go `require` dependency, provider SDK boundary or core API is introduced.

## Validation and residual findings

Use the Node 22 series used by CI:

```sh
npm ci
npm run lint
COPILOTKIT_TELEMETRY_DISABLED=true npm run build
npm audit --omit=dev --audit-level=high
npm audit --audit-level=high
```

The last command adds a separate full dependency-tree CI audit, including build
and lint dependencies, after the existing production gate. Neither command
suppresses advisories or lowers the existing high severity threshold.
The coordinator records command exits, package-lock SHA256, tool versions and
raw audit JSON in `docs/alignment-execution/2026-09-07/security-validation/`.
Acceptance requires new full CI and native Windows/Linux evidence on the
integrated source commit. Earlier platform acceptance remains historical evidence.

Old AI SDK provider-utils and Runtime's uuid 10 retain reported low/moderate
findings. The example configures `HttpAgent` and `ExperimentalEmptyAdapter`, not
those older AI provider adapters; this is an observed configuration boundary,
not a whole-program reachability proof. Development dependencies can have separate
findings and must not be described as a clean production tree. A passing production
gate means no high/critical findings in that scan's production scope, not zero
vulnerabilities or a future guarantee. Further parent-library migration requires
its own compatibility checks; incompatible global major overrides are not used.

## September 10 integration revalidation

The selected manifests and lockfile were installed again from scratch in a
private directory using **Node 22.23.2 / npm 10.9.8**. Official npm registry
metadata was read again for every selected override; tarball URLs in the whole
lockfile remain on `registry.npmjs.org`. CopilotKit, Next and React versions are
unchanged. Lockfile SHA256:
`6ac4400159258da30597cc187b2c7069f5a1e1a370c233a7a7ea6970cb7fe598`.

`npm ci`, `npm run lint`, the production build, and both production/full-tree
`npm audit --audit-level=high` variants exit 0. Telemetry is disabled, and the
checks call no Go backend or provider. Both audit scopes report **9 low and
2 moderate package findings, zero high/critical**:

- The nine low entries are `@ai-sdk/provider-utils` and its propagated effects
  in `@ai-sdk/anthropic`, `gateway`, `google`, `google-vertex`, `mcp`, `openai`,
  `openai-compatible`, and `ai`, from
  [GHSA-866g-f22w-33x8](https://github.com/advisories/GHSA-866g-f22w-33x8).
- The two moderate entries are `uuid` and its propagated effect in
  `@copilotkit/runtime`, from
  [GHSA-w5hq-g745-h8pq](https://github.com/advisories/GHSA-w5hq-g745-h8pq).

These are npm package-entry counts, not eleven distinct advisories. The existing
high threshold remains unchanged; no incompatible uuid major override or
`npm audit fix --force` is used. Exact commands and raw audit JSON are preserved
in the task-private `agent-adaptor-ci-dependencies-20260910` evidence directory
for coordinator archival. This validation belongs to the current manifests and
application snapshot; final acceptance requires new CI on the integrated SHA.
