# AG-UI example build dependency security — 2026-09-09

The existing production audit passed, but a separate full audit on the original
lockfile found seven package findings: four high, two moderate and one low.
The high entries were Vite, PostCSS, nanoid and Browserslist; esbuild and
baseline-browser-mapping had moderate entries, and Babel core had a low entry.
The original JSON is retained separately from production scan results.

## Dependency selection

Vite moves from the 5.4 range to **6.4.3**, the first fixed version in the next
major outside the reported range. This is a deliberate build-tool major upgrade;
it does not satisfy the previous `^5.4.0` declaration. React 18.3.1, AG-UI client
0.0.57 and React plugin 4.7.0 remain unchanged. The plugin supports Vite 6, which
supports Node 20 or 22+; CI uses Node 22.

The [official migration guide](https://v6.vite.dev/guide/migration) was checked
against this example's `defineConfig`, `loadEnv`, React plugin, backend URL
injection and loopback server options. It has no custom resolve conditions,
SSR/runtime API, library build, Sass, custom PostCSS configuration, proxy bypass
or affected glob patterns. These changes therefore do not require application
source edits here; actual build and server smoke checks remain necessary.

The [6.4.3 release](https://github.com/vitejs/vite/releases/tag/v6.4.3) retains
Rollup and moves esbuild to its allowed 0.25 family. The change stays within the
example build boundary. An official, documented build tool with security updates
is preferable to a custom bundler. No Go `require` or SDK execution API is added.
PostCSS 8.5.23, nanoid 3.3.18 and Browserslist 4.28.7 satisfy their existing parent
ranges. All resolved URLs use the official npm registry; fresh installation
checks integrity values.

## Verification boundary

A task-private Node 22 installation runs fresh `npm ci`, TypeScript checking and
`vite build`. Development and preview HTTP smoke checks must also verify the
configured entry points without invoking the Go backend or any real provider.
GitHub Actions repeats installation/build on Ubuntu and runs both
`npm audit --omit=dev --audit-level=high` and the newly added full-tree
`npm audit --audit-level=high`. Existing checks and thresholds are retained.

The updated lockfile audit reports no high/critical findings, with one low Babel
core finding still recorded. The coordinator preserves actual commands, source
hashes, exits and raw audit JSON for each date and scope. These checks do not
certify provider conformance, Windows Node binaries or future advisory status.

## September 10 integration revalidation

The deliberate Vite 5-to-6 build-tool migration is retained as the documented
major-version exception; React, AG-UI client and React plugin stay unchanged.
Official registry metadata confirms plugin-react 4.7.0 accepts Vite 6. The
migration guide was rechecked against the example's actual Vite configuration.
Lockfile SHA256:
`8abb42332a1764652997d59a3450e5a30477b23ad438d93d45e921456ce17e56`.

A fresh private installation with **Node 22.23.2 / npm 10.9.8**, TypeScript check
and production build all exit 0. Both Vite dev and preview servers were started
on temporary loopback ports, their HTML and JavaScript entry responses returned
200, and the servers were stopped. No backend or provider was called. This
example has no separate lint script; its existing build includes `tsc -b`.

The production audit reports **zero findings**. The full-tree audit reports
**one low finding, zero moderate/high/critical**: `@babel/core`,
[GHSA-4x5r-pxfx-6jf8](https://github.com/advisories/GHSA-4x5r-pxfx-6jf8).
Both commands keep the high threshold and exit 0. Registry metadata, command
logs, raw audit JSON and smoke results are preserved in the task-private
`agent-adaptor-ci-dependencies-20260910` evidence directory for coordinator
archival. Earlier branch CI is historical; the integrated SHA requires its own
complete CI acceptance.
