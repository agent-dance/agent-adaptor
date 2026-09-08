# Go standard-library security patch — 2026-09-08

The minimum Go version is raised from **1.26.5 to 1.26.8**. This keeps the
existing Go 1.26 language series and updates its standard library and toolchain.
No `require` version or `go.sum` entry changes.

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
later compiler/runtime and platform fixes. This change does not move to Go 1.27.

## Compiler requirement and CI selection

The [`go` directive](https://go.dev/doc/toolchain#go-mod-file) is a minimum
toolchain requirement, including when this SDK is used as a dependency. SDK
consumers using an older toolchain must upgrade to at least 1.26.8. With
`GOTOOLCHAIN=local`, an older Go command refuses the module instead of downloading
a replacement. With automatic toolchain selection enabled, Go may obtain a
suitable toolchain. The Go language version remains 1.26.

The existing `.github/workflows/go.yml` jobs use `actions/setup-go@v5` with
`go-version-file: go.mod`; `windows-validation.yml` uses `source/go.mod` for its
checked-out source. They therefore select the updated version without a workflow
edit. Their existing `GOTOOLCHAIN=local` and closed paid-test gates remain intact.

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
