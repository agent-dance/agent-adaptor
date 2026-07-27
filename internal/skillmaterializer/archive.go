package skillmaterializer

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Default safety limits for the built-in archive materializer. Hosts
// can override them via WithMaxArchiveSize / WithMaxFileSize /
// WithMaxArchiveEntries. The defaults are deliberately conservative;
// production hosts that need bigger skill bundles should raise them
// explicitly so the change shows up in code review.
const (
	defaultMaxArchiveBytes   int64 = 256 << 20 // 256 MiB compressed total
	defaultMaxFileBytes      int64 = 64 << 20  // 64 MiB per uncompressed entry
	defaultMaxArchiveEntries int   = 10000
)

// Option configures the private materializer returned by New.
type Option func(*Config)

type Format string

const (
	FormatAuto  Format = ""
	FormatZip   Format = "zip"
	FormatTar   Format = "tar"
	FormatTarGz Format = "tar.gz"
)

var ErrArchiveFormat = errors.New("agentadaptor: archive format error")

type Config struct {
	CacheRoot         string
	MaxArchiveBytes   int64
	MaxFileBytes      int64
	MaxArchiveEntries int
	SourceMissing     error
}

func DefaultConfig(sourceMissing error) Config {
	return Config{
		CacheRoot:         managedSkillCacheRoot(""),
		MaxArchiveBytes:   defaultMaxArchiveBytes,
		MaxFileBytes:      defaultMaxFileBytes,
		MaxArchiveEntries: defaultMaxArchiveEntries,
		SourceMissing:     sourceMissing,
	}
}

// WithSkillCacheRoot overrides the cache root the default materializer
// writes to. Empty input falls back to the AGENT_ADAPTOR_SKILL_CACHE_ROOT
// env var, then os.UserCacheDir(), then os.TempDir() — see
// managedSkillCacheRoot for the resolution order.
func WithSkillCacheRoot(path string) Option {
	return func(c *Config) {
		c.CacheRoot = managedSkillCacheRoot(path)
	}
}

// WithMaxArchiveSize caps the total compressed bytes the materializer
// will read from a SkillFromArchive stream. Streams that exceed the cap
// surface as an error before any extraction begins. Default 256 MiB.
// Values <= 0 are treated as the default.
func WithMaxArchiveSize(bytes int64) Option {
	return func(c *Config) {
		if bytes > 0 {
			c.MaxArchiveBytes = bytes
		}
	}
}

// WithMaxFileSize caps the uncompressed bytes a single archive entry is
// allowed to occupy. Decompression bombs that inflate one entry past
// the cap surface as an error mid-extraction; partially-extracted
// staging directories are cleaned up. Default 64 MiB. Values <= 0 are
// treated as the default.
func WithMaxFileSize(bytes int64) Option {
	return func(c *Config) {
		if bytes > 0 {
			c.MaxFileBytes = bytes
		}
	}
}

// WithMaxArchiveEntries caps the number of entries (files + dirs) in a
// single archive. Archives with more entries than the cap surface as
// an error before any file is written. Default 10000. Values <= 0 are
// treated as the default.
func WithMaxArchiveEntries(n int) Option {
	return func(c *Config) {
		if n > 0 {
			c.MaxArchiveEntries = n
		}
	}
}

// New returns the shared private implementation. sourceMissing is injected
// explicitly so this package remains below the public skill vocabulary while
// every caller still returns skill's one canonical sentinel.
func New(sourceMissing error, opts ...Option) Materializer {
	cfg := DefaultConfig(sourceMissing)
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return &defaultSkillMaterializer{cfg: cfg}
}

// Extraction is the result of safely walking an archive.
//
// Files maps forward-slashed entry paths (relative to Subpath) to their
// uncompressed content. The map is suitable for direct hand-off to
// writeFiles.
type Extraction struct {
	Files map[string][]byte
}

// SniffArchiveFormat detects the supported archive formats by their canonical
// magic bytes. An unknown payload returns FormatAuto and is rejected by
// ExtractArchive.
func SniffArchiveFormat(data []byte) Format {
	switch {
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4b, 0x03, 0x04}):
		return FormatZip
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4b, 0x05, 0x06}):
		return FormatZip
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		return FormatTarGz
	case len(data) >= 262 && bytes.Equal(data[257:262], []byte("ustar")):
		return FormatTar
	default:
		return FormatAuto
	}
}

// ExtractArchive decompresses and extracts an archive byte slice into
// memory subject to the configured safety limits.
//
// The implementation rejects:
//
//   - any entry whose cleaned path escapes the archive root (zip slip)
//   - any entry whose cleaned path escapes Subpath (after normalisation)
//   - any non-regular entry: symbolic links, hardlinks, devices, fifos
//     are silently dropped (they don't make sense in a SKILL.md tree)
//   - any single uncompressed entry larger than cfg.MaxFileBytes
//   - more than cfg.MaxArchiveEntries entries (file + dir count)
//
// Subpath is the prefix inside the archive where SKILL.md is expected
// to live. Empty means the archive root. Subpath is treated as a
// forward-slashed posix path; "./" / "../" / leading "/" are
// normalised away before matching.
func ExtractArchive(raw []byte, format Format, subpath string, cfg Config) (Extraction, error) {
	if format == FormatAuto {
		format = SniffArchiveFormat(raw)
	}
	switch format {
	case FormatZip:
		return extractZip(raw, subpath, cfg)
	case FormatTar:
		return extractTar(raw, subpath, cfg)
	case FormatTarGz:
		return extractTarGz(raw, subpath, cfg)
	default:
		return Extraction{}, fmt.Errorf("%w: unable to identify archive format", ErrArchiveFormat)
	}
}

// normalizeSubpath turns a host-supplied subpath into the canonical
// forward-slashed prefix used for entry-path matching. An empty result
// means "match against archive root".
func normalizeSubpath(subpath string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(subpath))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

// archiveEntryPath returns the path the entry should be written to,
// relative to subpath. The returned path is forward-slashed.
//
// Returns ok=false when the entry should be skipped:
//   - empty / dot-only / absolute / parent-traversing paths
//   - paths outside the requested subpath
//   - paths that would resolve to "" after stripping subpath
//
// errMsg is only set when the entry is malicious (zip slip / absolute
// path) so callers can surface it as a hard error; benign skips
// (subpath mismatch) keep errMsg empty.
func archiveEntryPath(name, subpath string) (rel string, ok bool, errMsg string) {
	cleanedName := strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if cleanedName == "" {
		return "", false, ""
	}
	// Reject any absolute path. tar / zip on POSIX may legitimately
	// have leading-slash names; the materializer treats them as
	// adversarial regardless of platform.
	if strings.HasPrefix(cleanedName, "/") {
		return "", false, fmt.Sprintf("absolute path not allowed: %q", name)
	}
	clean := path.Clean(cleanedName)
	if clean == "." {
		return "", false, ""
	}
	// path.Clean cannot create a leading ".." for relative inputs that
	// stay within the tree, so any ".." prefix means traversal.
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false, fmt.Sprintf("path traversal not allowed: %q", name)
	}

	if subpath == "" {
		return clean, true, ""
	}
	switch {
	case clean == subpath:
		return "", false, ""
	case strings.HasPrefix(clean, subpath+"/"):
		return strings.TrimPrefix(clean, subpath+"/"), true, ""
	default:
		return "", false, ""
	}
}

// extractZip walks a zip byte slice and applies the four documented
// safety nets: per-entry uncompressed cap (maxFileBytes), entry count
// cap (maxArchiveEntries), path-traversal rejection, and non-regular
// entry filtering (symlinks etc. are silently dropped). The total
// compressed-size cap is enforced earlier by ReadArchiveBytes, so
// extraction does not re-check it.
func extractZip(raw []byte, subpath string, cfg Config) (Extraction, error) {
	if len(raw) == 0 {
		return Extraction{}, fmt.Errorf("%w: empty archive", ErrArchiveFormat)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: zip open: %v", ErrArchiveFormat, err)
	}
	if len(zr.File) > cfg.MaxArchiveEntries {
		return Extraction{}, fmt.Errorf("%w: archive has %d entries, limit %d", ErrArchiveFormat, len(zr.File), cfg.MaxArchiveEntries)
	}
	sub := normalizeSubpath(subpath)
	files := map[string][]byte{}
	for _, entry := range zr.File {
		// Reject any non-regular entry. zip lacks a tar-style typeflag
		// but Mode reports symlinks via fs.ModeSymlink; treating the
		// "not regular and not a directory" path as a silent skip
		// matches tar semantics.
		if entry.Mode().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() {
			continue
		}
		rel, ok, errMsg := archiveEntryPath(entry.Name, sub)
		if errMsg != "" {
			return Extraction{}, fmt.Errorf("%w: %s", ErrArchiveFormat, errMsg)
		}
		if !ok {
			continue
		}
		if int64(entry.UncompressedSize64) > cfg.MaxFileBytes {
			return Extraction{}, fmt.Errorf("%w: entry %q is %d bytes, limit %d", ErrArchiveFormat, entry.Name, entry.UncompressedSize64, cfg.MaxFileBytes)
		}
		rc, err := entry.Open()
		if err != nil {
			return Extraction{}, fmt.Errorf("%w: zip entry %q: %v", ErrArchiveFormat, entry.Name, err)
		}
		// Cap the read at maxFileBytes+1 so a lying header can't make
		// us OOM. The +1 lets us detect overruns past the declared
		// size without preallocating the full buffer when the header
		// is truthful.
		buf, readErr := io.ReadAll(io.LimitReader(rc, cfg.MaxFileBytes+1))
		_ = rc.Close()
		if readErr != nil {
			return Extraction{}, fmt.Errorf("%w: zip entry %q read: %v", ErrArchiveFormat, entry.Name, readErr)
		}
		if int64(len(buf)) > cfg.MaxFileBytes {
			return Extraction{}, fmt.Errorf("%w: entry %q exceeds per-file limit %d", ErrArchiveFormat, entry.Name, cfg.MaxFileBytes)
		}
		files[rel] = buf
	}
	return Extraction{Files: files}, nil
}

func extractTar(raw []byte, subpath string, cfg Config) (Extraction, error) {
	return extractTarReader(bytes.NewReader(raw), subpath, cfg)
}

func extractTarGz(raw []byte, subpath string, cfg Config) (Extraction, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: gzip open: %v", ErrArchiveFormat, err)
	}
	defer func() { _ = gz.Close() }()
	return extractTarReader(gz, subpath, cfg)
}

func extractTarReader(r io.Reader, subpath string, cfg Config) (Extraction, error) {
	tr := tar.NewReader(r)
	sub := normalizeSubpath(subpath)
	files := map[string][]byte{}
	entries := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Extraction{}, fmt.Errorf("%w: tar header: %v", ErrArchiveFormat, err)
		}
		entries++
		if entries > cfg.MaxArchiveEntries {
			return Extraction{}, fmt.Errorf("%w: archive has more than %d entries", ErrArchiveFormat, cfg.MaxArchiveEntries)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		// Reject anything that isn't a plain file. SkillMD trees never
		// need symlinks / fifos / devices. tar.TypeReg covers both
		// the POSIX regular file and the historical "type A" alias
		// (the latter is a deprecated stdlib constant collapsed into
		// TypeReg since Go 1.11).
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		rel, ok, errMsg := archiveEntryPath(hdr.Name, sub)
		if errMsg != "" {
			return Extraction{}, fmt.Errorf("%w: %s", ErrArchiveFormat, errMsg)
		}
		if !ok {
			continue
		}
		if hdr.Size > cfg.MaxFileBytes {
			return Extraction{}, fmt.Errorf("%w: entry %q is %d bytes, limit %d", ErrArchiveFormat, hdr.Name, hdr.Size, cfg.MaxFileBytes)
		}
		buf, readErr := io.ReadAll(io.LimitReader(tr, cfg.MaxFileBytes+1))
		if readErr != nil {
			return Extraction{}, fmt.Errorf("%w: tar entry %q read: %v", ErrArchiveFormat, hdr.Name, readErr)
		}
		if int64(len(buf)) > cfg.MaxFileBytes {
			return Extraction{}, fmt.Errorf("%w: entry %q exceeds per-file limit %d", ErrArchiveFormat, hdr.Name, cfg.MaxFileBytes)
		}
		files[rel] = buf
	}
	return Extraction{Files: files}, nil
}

// --- size-capped read helpers --------------------------------------------

// ReadArchiveBytes reads from rc up to limit+1 bytes; if more is
// available it returns errArchiveTooLarge so the caller can refuse the
// archive without OOMing on a multi-gigabyte stream.
func ReadArchiveBytes(ctx context.Context, rc io.ReadCloser, limit int64) ([]byte, error) {
	defer func() { _ = rc.Close() }()
	if rc == nil {
		return nil, fmt.Errorf("%w: nil archive reader", ErrArchiveFormat)
	}
	if limit <= 0 {
		limit = defaultMaxArchiveBytes
	}
	// Honour ctx cancellation by wrapping rc in a cancellable reader.
	cr := &ctxReader{ctx: ctx, r: rc}
	buf, err := io.ReadAll(io.LimitReader(cr, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: archive read: %v", ErrArchiveFormat, err)
	}
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("%w: archive exceeds %d-byte limit", ErrArchiveFormat, limit)
	}
	return buf, nil
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
