package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// SkillArchiveFormat selects the decompressor used by SDK's built-in
// archive materializer. SkillArchiveAuto triggers magic-byte sniffing.
type SkillArchiveFormat string

const (
	// SkillArchiveAuto leaves format detection to the materializer:
	// it peeks the first few bytes of the archive stream and matches
	// against the canonical magic signatures (PK\x03\x04 for zip,
	// 1f 8b for gzip, ustar offset 257 for tar).
	SkillArchiveAuto SkillArchiveFormat = ""

	// SkillArchiveZip is a ZIP archive (PKZIP format).
	SkillArchiveZip SkillArchiveFormat = "zip"

	// SkillArchiveTar is an uncompressed POSIX tar archive.
	SkillArchiveTar SkillArchiveFormat = "tar"

	// SkillArchiveTarGz is a tar archive wrapped in a gzip stream
	// (canonical .tar.gz / .tgz).
	SkillArchiveTarGz SkillArchiveFormat = "tar.gz"
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
// Fingerprint is an opaque, stable source revision or identity used for
// declaration equivalence and compatibility. It is not a cache key and is
// not an integrity check: the materializer keys its cache from extracted
// content. New public code should construct this source through package
// skill; this engine type remains only for the in-flight v1 migration.
type SkillFromArchive struct {
	Archive     func(ctx context.Context) (io.ReadCloser, error)
	Format      SkillArchiveFormat
	Subpath     string
	Fingerprint string
}

// SkillSource implements [SkillSource].
func (SkillFromArchive) SkillSource() {}

// SkillArchive is the structural projection consumed by the materializer.
func (s SkillFromArchive) SkillArchive() (func(context.Context) (io.ReadCloser, error), string, string, string) {
	return s.Archive, string(s.Format), s.Subpath, s.Fingerprint
}

// ArchiveFromBytes returns an Archive function that serves the given
// data from memory. The slice is captured by reference; callers MUST
// NOT mutate it after invocation.
func ArchiveFromBytes(data []byte) func(context.Context) (io.ReadCloser, error) {
	return func(_ context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// ArchiveFromPath returns an Archive function that opens the given file
// each time the materializer needs to read it. Useful when the host has
// already downloaded the archive to disk.
func ArchiveFromPath(path string) func(context.Context) (io.ReadCloser, error) {
	return func(_ context.Context) (io.ReadCloser, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("agentadaptor: ArchiveFromPath(%q): %w", path, err)
		}
		return f, nil
	}
}

// ArchiveHTTPOption configures the http.Request issued by ArchiveFromURL.
type ArchiveHTTPOption func(*archiveHTTPConfig)

type archiveHTTPConfig struct {
	headers map[string]string
	client  *http.Client
}

// WithArchiveHeader adds a header to every request the returned Archive
// function issues. Multiple WithArchiveHeader calls accumulate.
func WithArchiveHeader(key, value string) ArchiveHTTPOption {
	return func(c *archiveHTTPConfig) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[key] = value
	}
}

// WithArchiveHTTPClient overrides the http.Client used to issue the
// request. Defaults to http.DefaultClient.
func WithArchiveHTTPClient(client *http.Client) ArchiveHTTPOption {
	return func(c *archiveHTTPConfig) {
		c.client = client
	}
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
	cfg := &archiveHTTPConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}
	if cfg.client == nil {
		cfg.client = http.DefaultClient
	}
	return func(ctx context.Context) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("agentadaptor: ArchiveFromURL(%q): %w", url, err)
		}
		for k, v := range cfg.headers {
			req.Header.Set(k, v)
		}
		resp, err := cfg.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("agentadaptor: ArchiveFromURL(%q): %w", url, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("agentadaptor: ArchiveFromURL(%q): HTTP %d", url, resp.StatusCode)
		}
		return resp.Body, nil
	}
}

// errArchiveFormat reports a format-detection or decompression failure
// for a SkillFromArchive source. It is wrapped by the materializer so
// callers can match with errors.Is(err, ErrArchiveFormat).
var errArchiveFormat = errors.New("agentadaptor: archive format error")

// sniffArchiveFormat inspects the first bytes of data and returns the
// detected format. SkillArchiveAuto means "could not identify".
//
// The sniffing rules cover the canonical magic numbers:
//
//	zip:    starts with the local-file-header signature 50 4b 03 04 ("PK\x03\x04")
//	gzip:   starts with 1f 8b
//	tar:    has "ustar" at offset 257 (covers both POSIX "ustar\x00" and GNU "ustar  ")
//
// Order: zip / gzip first (they are the common cases and have early
// magic), tar last (the magic is at offset 257 so we need at least
// that many bytes).
func sniffArchiveFormat(data []byte) SkillArchiveFormat {
	switch {
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4b, 0x03, 0x04}):
		return SkillArchiveZip
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4b, 0x05, 0x06}):
		// Empty zip (end-of-central-directory marker as the only thing)
		return SkillArchiveZip
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		// gzip-wrapped; we treat all gzip streams as tar.gz here. The
		// materializer will surface a decompression-then-tar error if
		// the inner stream isn't actually tar.
		return SkillArchiveTarGz
	case len(data) >= 262 && bytes.Equal(data[257:262], []byte("ustar")):
		// 5-byte "ustar" prefix at offset 257 covers both POSIX
		// (ustar\x00) and GNU tar (ustar followed by spaces) magic;
		// no need for a separate full-6-byte check.
		return SkillArchiveTar
	default:
		return SkillArchiveAuto
	}
}
