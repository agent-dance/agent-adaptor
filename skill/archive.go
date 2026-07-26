package skill

import (
	"context"
	"io"
	"net/http"

	agentadaptor "github.com/agent-dance/agent-adaptor"
)

// Format selects the decompressor applied to an [Archive] source.
// FormatAuto (the zero value) triggers magic-byte sniffing.
type Format = agentadaptor.SkillArchiveFormat

// Supported archive formats. They alias the root-package
// SkillArchiveFormat constants, so format values flow unchanged into
// the built-in materializer.
const (
	// FormatAuto leaves format detection to the materializer's
	// magic-byte sniffing (zip local-file header, gzip magic, ustar
	// magic at offset 257).
	FormatAuto = agentadaptor.SkillArchiveAuto
	// FormatZip is a ZIP archive (PKZIP format).
	FormatZip = agentadaptor.SkillArchiveZip
	// FormatTar is an uncompressed POSIX tar archive.
	FormatTar = agentadaptor.SkillArchiveTar
	// FormatTarGz is a tar archive wrapped in a gzip stream
	// (canonical .tar.gz / .tgz).
	FormatTarGz = agentadaptor.SkillArchiveTarGz
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

// WithFingerprint supplies an opaque cache key for the archive
// contents (for example a sha256 the host already knows, or a
// store-id+version pair). When empty, the SDK derives the cache key
// from the materialised content itself.
func WithFingerprint(fingerprint string) ArchiveOption {
	return func(c *archiveConfig) {
		c.fingerprint = fingerprint
	}
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
		Source: agentadaptor.SkillFromArchive{
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
	return agentadaptor.ArchiveFromBytes(data)
}

// ArchiveFile returns an [Opener] that opens the given file each time
// the materializer needs to read it. Useful when the host has already
// downloaded the archive to disk.
func ArchiveFile(path string) Opener {
	return agentadaptor.ArchiveFromPath(path)
}

// ArchiveHTTPOption configures the http.Request issued by [ArchiveURL].
type ArchiveHTTPOption = agentadaptor.ArchiveHTTPOption

// ArchiveURL returns an [Opener] that GETs the URL with the configured
// headers and HTTP client. Non-2xx responses surface as errors. It
// performs no integrity checking — pass [WithFingerprint] to [Archive]
// for that — and it imposes no default timeout: either supply an
// http.Client with a Timeout via [WithArchiveHTTPClient] or make sure
// the run context carries a deadline.
func ArchiveURL(url string, opts ...ArchiveHTTPOption) Opener {
	return agentadaptor.ArchiveFromURL(url, opts...)
}

// WithArchiveHeader adds a header to every request the [ArchiveURL]
// opener issues. Multiple WithArchiveHeader options accumulate.
func WithArchiveHeader(key, value string) ArchiveHTTPOption {
	return agentadaptor.WithArchiveHeader(key, value)
}

// WithArchiveHTTPClient overrides the http.Client used by the
// [ArchiveURL] opener. Defaults to http.DefaultClient.
func WithArchiveHTTPClient(client *http.Client) ArchiveHTTPOption {
	return agentadaptor.WithArchiveHTTPClient(client)
}
