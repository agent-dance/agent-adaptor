package skill

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Format selects the decompressor applied to an [Archive] source.
// FormatAuto (the zero value) triggers magic-byte sniffing.
type Format string

// Supported archive formats accepted by the built-in materializer.
const (
	// FormatAuto leaves format detection to the materializer's
	// magic-byte sniffing (zip local-file header, gzip magic, ustar
	// magic at offset 257).
	FormatAuto Format = ""
	// FormatZip is a ZIP archive (PKZIP format).
	FormatZip Format = "zip"
	// FormatTar is an uncompressed POSIX tar archive.
	FormatTar Format = "tar"
	// FormatTarGz is a tar archive wrapped in a gzip stream
	// (canonical .tar.gz / .tgz).
	FormatTarGz Format = "tar.gz"
)

// Opener produces a fresh reader over the archive bytes. It is invoked
// at materialization time and read to completion (subject to the
// configured size cap), so implementations MUST be idempotent: a second
// invocation should produce the same content. Build one with
// [ArchiveBytes], [ArchiveFile], or [ArchiveURL], or supply your own.
type Opener = func(ctx context.Context) (io.ReadCloser, error)

// ArchiveOption configures the archive source built by [Archive].
type ArchiveOption func(*archiveConfig)

type archiveConfig struct {
	format      Format
	subpath     string
	fingerprint string
}

// WithFormat pins the archive format instead of relying on magic-byte
// sniffing. Explicit formats surface mismatches as decompression
// errors, which is preferable when the host already knows what it
// serves.
func WithFormat(f Format) ArchiveOption {
	return func(c *archiveConfig) {
		c.format = f
	}
}

// WithSubpath declares the prefix inside the archive where SKILL.md
// lives. Empty means the archive root. Entries that resolve outside
// the subpath are rejected during extraction.
func WithSubpath(subpath string) ArchiveOption {
	return func(c *archiveConfig) {
		c.subpath = subpath
	}
}

// WithFingerprint supplies an opaque, stable source revision or identity.
// It is used to decide whether independently declared archive sources refer
// to the same logical revision and may also participate in Thread
// compatibility. It is not a cache key and is not an integrity check: the
// built-in materializer always keys its cache from the extracted content.
// Hosts that need authenticity or integrity verification must perform it in
// the Opener before returning the reader.
func WithFingerprint(fingerprint string) ArchiveOption {
	return func(c *archiveConfig) {
		c.fingerprint = fingerprint
	}
}

// ArchiveSource is the public archive-origin value stored in [Skill.Source].
// Independently declared ArchiveSource values without a non-empty Fingerprint
// are intentionally not assumed equal because Go functions have no stable
// content identity.
type ArchiveSource struct {
	// Archive opens the archive from its beginning for each materialization.
	Archive Opener
	// Format selects archive decoding; its zero value enables detection.
	Format Format
	// Subpath locates the skill root within the archive.
	Subpath string
	// Fingerprint is an optional stable source revision or identity.
	Fingerprint string
}

// SkillSource implements [Source].
func (ArchiveSource) SkillSource() {}

// SkillArchive returns the values consumed by a compatible materializer.
func (s ArchiveSource) SkillArchive() (Opener, string, string, string) {
	return s.Archive, string(s.Format), s.Subpath, s.Fingerprint
}

// Archive builds a Skill sourced from an archive stream (zip, tar, or
// tar.gz). The archive must contain SKILL.md at its root, or under the
// prefix declared via [WithSubpath]. Key is required; open supplies
// the archive bytes and is typically built with [ArchiveBytes],
// [ArchiveFile], or [ArchiveURL]:
//
//	skill.Archive("deploy-kit", skill.ArchiveFile("./deploy-kit.tgz"))
//	skill.Archive("deploy-kit",
//	    skill.ArchiveURL("https://store.example.com/kits/deploy.zip",
//	        skill.WithArchiveHeader("Authorization", "Bearer "+token)),
//	    skill.WithFingerprint(knownDigest),
//	)
//
// Format defaults to [FormatAuto] (magic-byte sniffing); use
// [WithFormat] to pin it explicitly.
func Archive(key string, open Opener, opts ...ArchiveOption) Skill {
	var cfg archiveConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return Skill{
		Key: key,
		Source: ArchiveSource{
			Archive:     open,
			Format:      cfg.format,
			Subpath:     cfg.subpath,
			Fingerprint: cfg.fingerprint,
		},
	}
}

// ArchiveBytes returns an [Opener] that serves the given data from
// memory. The slice is captured by reference; callers MUST NOT mutate
// it afterwards.
func ArchiveBytes(data []byte) Opener {
	return func(_ context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
}

// ArchiveFile returns an [Opener] that opens the given file each time
// the materializer needs to read it. Useful when the host has already
// downloaded the archive to disk.
func ArchiveFile(path string) Opener {
	return func(_ context.Context) (io.ReadCloser, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("skill: open archive %q: %w", path, err)
		}
		return file, nil
	}
}

// ArchiveHTTPOption configures the http.Request issued by [ArchiveURL].
type ArchiveHTTPOption func(*archiveHTTPConfig)

type archiveHTTPConfig struct {
	headers map[string]string
	client  *http.Client
}

// ArchiveURL returns an [Opener] that GETs the URL with the configured
// headers and HTTP client. Non-2xx responses surface as errors. It performs
// no integrity checking, and [WithFingerprint] is only a declared source
// identity—it does not add integrity verification. The opener itself must
// verify authenticity when required. ArchiveURL imposes no default timeout:
// either supply an
// http.Client with a Timeout via [WithArchiveHTTPClient] or make sure
// the run context carries a deadline.
func ArchiveURL(url string, opts ...ArchiveHTTPOption) Opener {
	cfg := archiveHTTPConfig{client: http.DefaultClient}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.client == nil {
		cfg.client = http.DefaultClient
	}
	return func(ctx context.Context) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("skill: create archive request %q: %w", url, err)
		}
		for key, value := range cfg.headers {
			req.Header.Set(key, value)
		}
		resp, err := cfg.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("skill: fetch archive %q: %w", url, err)
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("skill: fetch archive %q: HTTP %d", url, resp.StatusCode)
		}
		return resp.Body, nil
	}
}

// WithArchiveHeader adds a header to every request the [ArchiveURL]
// opener issues. Multiple WithArchiveHeader options accumulate.
func WithArchiveHeader(key, value string) ArchiveHTTPOption {
	return func(c *archiveHTTPConfig) {
		if c.headers == nil {
			c.headers = map[string]string{}
		}
		c.headers[key] = value
	}
}

// WithArchiveHTTPClient overrides the http.Client used by the
// [ArchiveURL] opener. Defaults to http.DefaultClient.
func WithArchiveHTTPClient(client *http.Client) ArchiveHTTPOption {
	return func(c *archiveHTTPConfig) { c.client = client }
}
