package agentadaptor

// Thin shell preserving the historical root-package archive-source surface.
// The public truth now lives in package skill; this facade is deleted at the
// v1 cutover. The engine consumes sources through a structural projection so
// no root-package initialization or reverse dependency is required.

import (
	"context"
	"io"
	"net/http"

	"github.com/agent-dance/agent-adaptor/skill"
)

// SkillArchiveFormat selects the decompressor used by SDK's built-in
// archive materializer. SkillArchiveAuto triggers magic-byte sniffing.
type SkillArchiveFormat = skill.Format

const (
	// SkillArchiveAuto leaves format detection to the materializer:
	// it peeks the first few bytes of the archive stream and matches
	// against the canonical magic signatures (PK\x03\x04 for zip,
	// 1f 8b for gzip, ustar offset 257 for tar).
	SkillArchiveAuto = skill.FormatAuto

	// SkillArchiveZip is a ZIP archive (PKZIP format).
	SkillArchiveZip = skill.FormatZip

	// SkillArchiveTar is an uncompressed POSIX tar archive.
	SkillArchiveTar = skill.FormatTar

	// SkillArchiveTarGz is a tar archive wrapped in a gzip stream
	// (canonical .tar.gz / .tgz).
	SkillArchiveTarGz = skill.FormatTarGz
)

// SkillFromArchive sources a skill from an archive stream. The archive
// must contain SKILL.md at its root, or under Subpath when the archive
// wraps the skill in an extra directory layer.
//
// Archive is invoked at materialization time and returns a fresh reader
// of the archive bytes. The materializer reads it to completion (subject
// to the size cap configured via WithMaxArchiveSize), so implementations
// MUST be idempotent: a second invocation should be able to produce the
// same content.
//
// Format selects the decompressor; SkillArchiveAuto triggers magic-byte
// sniffing. Setting Format explicitly skips sniffing and surfaces format
// mismatches as decompression errors.
//
// Subpath is the prefix inside the archive where SKILL.md lives.
// Empty means the archive root. The materializer rejects any path that
// resolves outside Subpath even if the archive contains malicious
// "..\n.." components.
//
// Fingerprint is an opaque stable source revision used for declaration
// equivalence and compatibility. It is neither a cache key nor an integrity
// check; the built-in cache remains content-addressed.
type SkillFromArchive = skill.ArchiveSource

// ArchiveFromBytes returns an Archive function that serves the given
// data from memory. The slice is captured by reference; callers MUST
// NOT mutate it after invocation.
func ArchiveFromBytes(data []byte) func(context.Context) (io.ReadCloser, error) {
	return skill.ArchiveBytes(data)
}

// ArchiveFromPath returns an Archive function that opens the given file
// each time the materializer needs to read it. Useful when the host has
// already downloaded the archive to disk.
func ArchiveFromPath(path string) func(context.Context) (io.ReadCloser, error) {
	return skill.ArchiveFile(path)
}

// ArchiveHTTPOption configures the http.Request issued by ArchiveFromURL.
type ArchiveHTTPOption = skill.ArchiveHTTPOption

// WithArchiveHeader adds a header to every request the returned Archive
// function issues. Multiple WithArchiveHeader calls accumulate.
func WithArchiveHeader(key, value string) ArchiveHTTPOption {
	return skill.WithArchiveHeader(key, value)
}

// WithArchiveHTTPClient overrides the http.Client used to issue the
// request. Defaults to http.DefaultClient.
func WithArchiveHTTPClient(client *http.Client) ArchiveHTTPOption {
	return skill.WithArchiveHTTPClient(client)
}

// ArchiveFromURL returns an Archive function that GETs the URL with the
// configured headers and HTTP client. It does not perform integrity checking;
// Fingerprint is only a declared source identity and does not add verification.
// Wrap the opener with host-side verification when authenticity is required.
//
// 4xx / 5xx responses surface as a non-nil error; the response body is
// drained and closed before the error is returned.
//
// # Timeout policy
//
// ArchiveFromURL does NOT impose a default timeout: it uses the
// supplied client (or http.DefaultClient if none is set) and the ctx
// passed in by the materializer at fetch time. http.DefaultClient has
// no Timeout, so a slow / unresponsive server WILL hang the call until
// ctx is cancelled. Callers MUST therefore either:
//
//   - pass a *http.Client with a Timeout via WithArchiveHTTPClient,
//     OR
//   - ensure the ctx propagated through SDK Run() carries a deadline
//     (most production hosts already do this at the HTTP handler edge)
//
// Failing to satisfy at least one of these is the most common
// production hazard with this helper. SDK does not silently inject a
// default timeout because hosts deploying behind very fast / very
// slow stores have very different "reasonable" caps.
func ArchiveFromURL(url string, opts ...ArchiveHTTPOption) func(context.Context) (io.ReadCloser, error) {
	return skill.ArchiveURL(url, opts...)
}
