# Go standard-library security patch — 2026-09-08

The minimum Go version is raised from **1.26.5 to 1.26.8**. This keeps the
existing Go 1.26 language series and updates its standard library and toolchain.
The original September 8 patch changed no `require` version or `go.sum` entry.
The September 10 integration below additionally updates `golang.org/x/sys`.

## Trigger and selected patch

The `Go` workflow's `validate` job in Actions run `34212287457` executed:

```sh
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

It exited 1 on Go 1.26.5 with five reachable standard-library findings. Each
report identifies Go 1.26.6 as the fix within the 1.26 series:

| Official finding | Reported package |
|---|---|
| [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) | `net/url` |
| [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) | `crypto/tls` |
| [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) | `net/http` |
| [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) | `encoding/asn1` |
| [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) | `net/http` |

The original log is preserved in the coordinator's execution evidence at
`docs/alignment-execution/2026-09-07/windows-actions/go-34212287457-failed.log`,
SHA256 `c3233a108c4c936bd811b4b5b255cddd098d7c81ab0969062b0c9fb07519b4c5`.
The directory name does not make this a Windows scan: `validate` runs on Ubuntu.
The separate frontend failure in that log is outside this Go patch.

[Go's release history](https://go.dev/doc/devel/release) lists 1.26.6 security
fixes on August 13, 1.26.7 HTTP fixes on August 19, and 1.26.8 on September 1.
Version 1.26.8 is the latest stable 1.26 patch in the official download listing
checked on September 8. It includes the earlier security and HTTP fixes plus
later compiler/runtime and platform fixes. The consumer minimum stays on Go 1.26;
the later CI selection described below is independent of that minimum.

## Compiler requirement and CI selection

The [`go` directive](https://go.dev/doc/toolchain#go-mod-file) is a minimum
toolchain requirement, including when this SDK is used as a dependency. SDK
consumers using an older toolchain must upgrade to at least 1.26.8. With
`GOTOOLCHAIN=local`, an older Go command refuses the module instead of downloading
a replacement. With automatic toolchain selection enabled, Go may obtain a
suitable toolchain. The Go language version remains 1.26.

The `minimum-go` job uses `actions/setup-go@v5` with `go-version-file: go.mod`
and runs the complete test suite and vet on the declared consumer minimum.
Other Go jobs and the Review contracts workflow use `.github/go-version`;
Windows/Linux platform acceptance reads `source/.github/go-version` from its
exact source checkout. The CI version is pinned to official Go 1.27.1 for the
fuzz shutdown fix below. `GOTOOLCHAIN=local` and closed paid-test gates remain
intact; no dependency or public API requires Go 1.27.

## Reproduction and evidence boundary

The local check uses the official macOS ARM64 archive in a task-private temporary
directory, without changing the user's globally installed Go:

- [Archive](https://go.dev/dl/go1.26.8.darwin-arm64.tar.gz):
  `go1.26.8.darwin-arm64.tar.gz`, 64,626,620 bytes.
- SHA256 from the [official download listing](https://go.dev/dl/):
  `a012b25b571bd0138a03dcd25375ceba866fe5ca822f426d2c66a4de56fd3f4b`.
- The downloaded bytes must match that checksum before extraction or execution.

After committing this patch, run the original pinned scanner and local compile
checks on that exact commit:

```sh
go version
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go build -v ./...
go vet ./...
```

The execution uses explicit `GOROOT`/`PATH`, `GOTOOLCHAIN=local`, private
`HOME`/`USERPROFILE`/`XDG_CONFIG_HOME` and Go caches, removes provider configuration
root overrides, and sets `AGENT_ADAPTOR_LIVE_CONFORMANCE`, `AGENT_ADAPTOR_E2E` and
`AGENT_ADAPTOR_UPDATE_API_GOLDEN` to `0`. It does not run provider CLIs or paid tests.

The separate `security-go-patch-report.json` records the resulting commit, native
OS, exact commands, exits, download verification and complete log hashes. A clean
scan describes the code and vulnerability database observed at that time; it is
not a guarantee against future reports. Local macOS build/vet and scanning do not
replace the coordinator's full Linux/Windows CI validation. Previous canonical
task and acceptance records retain their original Go version and tested SHA.

## September 10 integration and Windows dependency repair

The dependency line was revalidated using an isolated snapshot of
`fc811120b6d4fbf3f30aebf26c24921d935442d7` with the integrated `go.mod`/`go.sum`.
The native macOS scan with Go 1.26.8 and pinned govulncheck 1.6.0 initially
reported no reachable symbols, but that did not cover Windows-only code.
A separate `GOOS=windows GOARCH=amd64` source scan exited 3 and identified
`internal/systemprompt/file_windows.go:72` calling `windows.NewNTUnicodeString`:
[GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024).

`golang.org/x/sys` is therefore updated from **v0.41.0 to v0.44.0**, the first
fixed version identified by the official advisory. `go get` and `go mod tidy`
update only this existing requirement and its two checksum entries. Its Go 1.25
minimum is compatible with the project's Go 1.26.8 minimum.

Dependency selection: the maintained Go project package supplies the Windows
NT and security-descriptor APIs needed for atomic private file creation;
updating it removes a defect in the called symbol without replacing those APIs
with handwritten syscall code. Its use remains within existing platform
implementation boundaries and adds no public SDK type or dependency family.
Current private object names are SDK-generated short values, so a reachable-symbol
report is not proof that an attacker can supply an overflowing string on this
path. The dependency defect is nevertheless removed rather than suppressed.

After the update, native macOS and Windows-target source scans both exit 0 with
**zero reachable-symbol and zero imported-package findings**. Three module-only
advisories remain: [GO-2026-6180](https://pkg.go.dev/vuln/GO-2026-6180) and
[GO-2026-6179](https://pkg.go.dev/vuln/GO-2026-6179) in `x/mod v0.33.0`, and
[GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970) in `x/text v0.33.0`.
The scanner reports no calls into those vulnerable packages. These observations
are platform- and source-specific, not a declaration that all required modules
have zero advisories.

The host-built scanner was executed with Windows source selection; this is not
native Windows runtime evidence. The integrated commit still requires the
coordinator's complete CI, including its native Windows scanner and runtime tests.
Before/after logs, exact commands, tool versions and SHA256 manifests are retained
in the task-private `agent-adaptor-ci-dependencies-20260910` evidence directory
for coordinator archival. The failed pre-upgrade Windows scan is retained along
with the passing scans; historical acceptance records are not rewritten.

## September 10 fuzz coordinator shutdown repair

Candidate `75a84c8b7a62e4bcdcca2fe9e2e31c8670d0fcb9` passed the ordinary Go
workflow and native T26, but the independent T25-V09 command failed after
30 seconds and 847,052 fuzz executions with only `context deadline exceeded`.
T25-V01 through V08 passed. The failure produced no crash input and did not
change source bytes; the failed run remains part of the evidence.

[Go issue 75804](https://github.com/golang/go/issues/75804) describes the
coordinator race matching that output. On Go 1.26.8, the parent context can
publish its deadline before canceling its child. The coordinator's `stop`
function recognizes the child error but can retain the parent's deadline error
as a failure during that interval. The
[official Go 1.27.1 implementation](https://github.com/golang/go/blob/go1.27.1/src/internal/fuzz/fuzz.go)
also recognizes `ctx.Err()` before stopping workers. This is a framework
termination path, not an archive parser error. The original CI log does not
record the internal scheduling; the mechanism and upstream fix are independent
evidence, not a retrospectively captured trace of that run.

CI and both platform collectors now use the official Go 1.27.1 distribution,
released September 1 in the [Go release history](https://go.dev/doc/devel/release).
The minimum supported Go version remains 1.26.8 and receives separate full tests
and vet. No local toolchain patch, deadline-error filter, automatic retry,
execution-count substitute or shorter fuzz budget is used in acceptance. Every
original T25 command, including all 30-second fuzz targets, must pass on the new
exact SHA.
