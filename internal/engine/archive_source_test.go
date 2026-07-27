package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- archive helpers (test fixtures) -------------------------------------

func makeZip(t testing.TB, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip Create %q: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("zip Write %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip Close: %v", err)
	}
	return buf.Bytes()
}

func makeTarGz(t testing.TB, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, content := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

// --- ArchiveFrom* helpers ------------------------------------------------

func TestArchiveFromBytes(t *testing.T) {
	t.Parallel()
	data := []byte("hello world")
	rc, err := ArchiveFromBytes(data)(context.Background())
	if err != nil {
		t.Fatalf("ArchiveFromBytes: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("ArchiveFromBytes round-trip: got %q, want %q", got, data)
	}
}

func TestArchiveFromPath(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "skill.zip")
	want := []byte{0x50, 0x4b, 0x05, 0x06}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	rc, err := ArchiveFromPath(path)(context.Background())
	if err != nil {
		t.Fatalf("ArchiveFromPath: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ArchiveFromPath round-trip: got %x, want %x", got, want)
	}
}

func TestArchiveFromPath_NotFound(t *testing.T) {
	t.Parallel()
	_, err := ArchiveFromPath("/non/existent/path.zip")(context.Background())
	if err == nil {
		t.Fatal("ArchiveFromPath: want error for missing file, got nil")
	}
}

func TestArchiveFromURL(t *testing.T) {
	t.Parallel()
	want := []byte("ZIPDATA")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer T" {
			t.Errorf("Authorization header: got %q, want %q", got, "Bearer T")
		}
		_, _ = w.Write(want)
	}))
	defer srv.Close()
	rc, err := ArchiveFromURL(srv.URL, WithArchiveHeader("Authorization", "Bearer T"))(context.Background())
	if err != nil {
		t.Fatalf("ArchiveFromURL: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("ArchiveFromURL body: got %q, want %q", got, want)
	}
}

func TestArchiveFromURL_HTTPError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := ArchiveFromURL(srv.URL)(context.Background())
	if err == nil {
		t.Fatal("ArchiveFromURL: want non-nil error on HTTP 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error message should mention 403; got %q", err.Error())
	}
}

func TestArchiveFromURL_CtxCancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0})
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	_, err := ArchiveFromURL(srv.URL)(ctx)
	if err == nil {
		t.Fatal("ArchiveFromURL with cancelled ctx: want error, got nil")
	}
}

// --- sniffArchiveFormat --------------------------------------------------

func TestSniffArchiveFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		want SkillArchiveFormat
	}{
		{"empty", nil, SkillArchiveAuto},
		{"random text", []byte("hello world"), SkillArchiveAuto},
		{"zip-local-header", []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x00}, SkillArchiveZip},
		{"zip-eocd-only (empty zip)", []byte{0x50, 0x4b, 0x05, 0x06, 0x00, 0x00}, SkillArchiveZip},
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00}, SkillArchiveTarGz},
		{"tar (ustar magic at offset 257)", makeTarMagic(), SkillArchiveTar},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sniffArchiveFormat(tc.data); got != tc.want {
				t.Errorf("sniffArchiveFormat(%q): got %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// makeTarMagic returns 263 bytes whose offset 257 contains "ustar\x00",
// which matches the POSIX tar magic. Used for sniffing tests.
func makeTarMagic() []byte {
	b := make([]byte, 263)
	copy(b[257:], []byte("ustar\x00"))
	return b
}

// --- extractArchive end-to-end (zip / tar / tar.gz) ---------------------

func TestExtractZip_Happy(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{
		"SKILL.md":         "# hi",
		"references/r.txt": "ref",
	})
	got, err := extractArchive(data, SkillArchiveAuto, "", defaultMaterializerConfig())
	if err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if got.Files["SKILL.md"] == nil {
		t.Errorf("SKILL.md missing")
	}
	if string(got.Files["references/r.txt"]) != "ref" {
		t.Errorf("references/r.txt: got %q", got.Files["references/r.txt"])
	}
}

func TestExtractTarGz_Happy(t *testing.T) {
	t.Parallel()
	data := makeTarGz(t, map[string]string{
		"SKILL.md":  "# tarball",
		"r/sub.txt": "x",
	})
	got, err := extractArchive(data, SkillArchiveAuto, "", defaultMaterializerConfig())
	if err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if string(got.Files["SKILL.md"]) != "# tarball" {
		t.Errorf("SKILL.md: got %q", got.Files["SKILL.md"])
	}
	if string(got.Files["r/sub.txt"]) != "x" {
		t.Errorf("r/sub.txt: got %q", got.Files["r/sub.txt"])
	}
}

func TestExtractZip_Subpath(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{
		"my-skill-v1/SKILL.md":  "# inner",
		"my-skill-v1/refs/a.md": "A",
		"unrelated/b.md":        "B",
	})
	got, err := extractArchive(data, SkillArchiveZip, "my-skill-v1", defaultMaterializerConfig())
	if err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if string(got.Files["SKILL.md"]) != "# inner" {
		t.Errorf("subpath SKILL.md: got %q", got.Files["SKILL.md"])
	}
	if _, has := got.Files["unrelated/b.md"]; has {
		t.Errorf("subpath leaked unrelated/b.md")
	}
	if string(got.Files["refs/a.md"]) != "A" {
		t.Errorf("subpath refs/a.md: got %q", got.Files["refs/a.md"])
	}
}

// --- security: zip slip / path traversal --------------------------------

func TestExtractZip_RejectsAbsolutePath(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{
		"/etc/passwd": "evil",
		"SKILL.md":    "# legit",
	})
	_, err := extractArchive(data, SkillArchiveZip, "", defaultMaterializerConfig())
	if err == nil {
		t.Fatal("want error for absolute path entry, got nil")
	}
	if !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("error should mention absolute path; got %q", err.Error())
	}
}

func TestExtractZip_RejectsParentTraversal(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{
		"../../etc/passwd": "evil",
	})
	_, err := extractArchive(data, SkillArchiveZip, "", defaultMaterializerConfig())
	if err == nil {
		t.Fatal("want error for ../ path entry, got nil")
	}
	if !strings.Contains(err.Error(), "traversal") {
		t.Errorf("error should mention traversal; got %q", err.Error())
	}
}

func TestExtractTarGz_RejectsParentTraversal(t *testing.T) {
	t.Parallel()
	data := makeTarGz(t, map[string]string{
		"../../etc/passwd": "evil",
	})
	_, err := extractArchive(data, SkillArchiveTarGz, "", defaultMaterializerConfig())
	if err == nil {
		t.Fatal("want error for ../ path entry, got nil")
	}
}

func TestExtractZip_BackslashTraversal(t *testing.T) {
	t.Parallel()
	// Some attackers craft paths with backslash on Unix; the materializer
	// normalises \ -> / before checking.
	data := makeZip(t, map[string]string{
		"..\\..\\evil": "evil",
	})
	_, err := extractArchive(data, SkillArchiveZip, "", defaultMaterializerConfig())
	if err == nil {
		t.Fatal("want error for backslash traversal, got nil")
	}
}

// --- security: limits ---------------------------------------------------

func TestExtract_PerFileSizeLimit(t *testing.T) {
	t.Parallel()
	cfg := defaultMaterializerConfig()
	cfg.maxFileBytes = 16 // tiny
	data := makeZip(t, map[string]string{
		"SKILL.md": strings.Repeat("A", 64), // 64 bytes > 16
	})
	_, err := extractArchive(data, SkillArchiveZip, "", cfg)
	if err == nil {
		t.Fatal("want per-file size error, got nil")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error should mention limit; got %q", err.Error())
	}
}

func TestExtract_TooManyEntries(t *testing.T) {
	t.Parallel()
	cfg := defaultMaterializerConfig()
	cfg.maxArchiveEntries = 2
	data := makeZip(t, map[string]string{
		"a": "1",
		"b": "2",
		"c": "3",
	})
	_, err := extractArchive(data, SkillArchiveZip, "", cfg)
	if err == nil {
		t.Fatal("want too-many-entries error, got nil")
	}
}

// --- security: non-regular entries (symlinks etc.) -----------------------

func TestExtractTar_DropsSymlinks(t *testing.T) {
	t.Parallel()
	// Build a tar with a symlink entry alongside a regular file.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "SKILL.md",
		Mode:     0o644,
		Size:     5,
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("write SKILL.md header: %v", err)
	}
	if _, err := tw.Write([]byte("hello")); err != nil {
		t.Fatalf("write SKILL.md body: %v", err)
	}
	// Symlink pointing outside the tree.
	if err := tw.WriteHeader(&tar.Header{
		Name:     "evil-link",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
		Mode:     0o777,
	}); err != nil {
		t.Fatalf("write symlink header: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	got, err := extractArchive(buf.Bytes(), SkillArchiveTar, "", defaultMaterializerConfig())
	if err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if string(got.Files["SKILL.md"]) != "hello" {
		t.Errorf("SKILL.md content: got %q", got.Files["SKILL.md"])
	}
	if _, has := got.Files["evil-link"]; has {
		t.Errorf("symlink should have been silently dropped, but it was extracted")
	}
}

// --- end-to-end Materialize ---------------------------------------------

func TestMaterialize_SkillFromArchive_Zip(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(tmp))

	data := makeZip(t, map[string]string{
		"SKILL.md":      "# end-to-end",
		"refs/note.txt": "hi",
	})
	skill := Skill{
		Key: "e2e",
		Source: SkillFromArchive{
			Archive: ArchiveFromBytes(data),
			Format:  SkillArchiveZip,
		},
	}

	got, err := mat.Materialize(context.Background(), skill)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	skillFile := filepath.Join(got, "SKILL.md")
	body, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatalf("read materialized SKILL.md: %v", err)
	}
	if string(body) != "# end-to-end" {
		t.Errorf("SKILL.md body: got %q", body)
	}
	notes := filepath.Join(got, "refs", "note.txt")
	if _, err := os.Stat(notes); err != nil {
		t.Errorf("refs/note.txt should exist; got: %v", err)
	}
}

func TestMaterialize_SkillFromArchive_TarGzWithSubpath(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(tmp))

	data := makeTarGz(t, map[string]string{
		"my-skill/SKILL.md": "# inside subpath",
	})
	skill := Skill{
		Key: "subpath",
		Source: SkillFromArchive{
			Archive: ArchiveFromBytes(data),
			Format:  SkillArchiveTarGz,
			Subpath: "my-skill",
		},
	}
	got, err := mat.Materialize(context.Background(), skill)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(got, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(body) != "# inside subpath" {
		t.Errorf("got %q", body)
	}
}

func TestMaterialize_SkillFromArchive_MissingSKILLmd(t *testing.T) {
	t.Parallel()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir()))
	data := makeZip(t, map[string]string{
		"refs/r.md": "ref-only",
	})
	skill := Skill{
		Key: "no-skillmd",
		Source: SkillFromArchive{
			Archive: ArchiveFromBytes(data),
			Format:  SkillArchiveZip,
		},
	}
	_, err := mat.Materialize(context.Background(), skill)
	if err == nil {
		t.Fatal("want error for archive without SKILL.md, got nil")
	}
	if !strings.Contains(err.Error(), "SKILL.md") {
		t.Errorf("error should mention SKILL.md; got %q", err.Error())
	}
}

func TestMaterialize_SkillFromArchive_NilArchive(t *testing.T) {
	t.Parallel()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir()))
	skill := Skill{
		Key:    "nil-archive",
		Source: SkillFromArchive{Archive: nil},
	}
	_, err := mat.Materialize(context.Background(), skill)
	if err == nil {
		t.Fatal("want error for nil Archive, got nil")
	}
	if !strings.Contains(err.Error(), "Archive is nil") {
		t.Errorf("error should mention Archive is nil; got %q", err.Error())
	}
}

func TestMaterialize_SkillFromArchive_CacheHit(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(tmp))

	calls := 0
	archive := func(_ context.Context) (io.ReadCloser, error) {
		calls++
		return ArchiveFromBytes(makeZip(t, map[string]string{"SKILL.md": "# cached"}))(context.Background())
	}
	skill := Skill{
		Key: "cached",
		Source: SkillFromArchive{
			Archive: archive,
			Format:  SkillArchiveZip,
		},
	}
	first, err := mat.Materialize(context.Background(), skill)
	if err != nil {
		t.Fatalf("first Materialize: %v", err)
	}
	second, err := mat.Materialize(context.Background(), skill)
	if err != nil {
		t.Fatalf("second Materialize: %v", err)
	}
	if first != second {
		t.Errorf("cache hit: want same path, got %q vs %q", first, second)
	}
	// Archive function is invoked once per Materialize call (no
	// short-circuit cache); the cache hit happens at the writeFiles
	// layer. This is documented in skill_helpers.go writeFromArchive.
	if calls != 2 {
		t.Errorf("Archive() invocations: got %d, want 2 (cache is at writeFiles, not source)", calls)
	}
}

// --- archive size cap ----------------------------------------------------

func TestMaterialize_SkillFromArchive_SizeLimit(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	mat := NewDefaultSkillMaterializer(
		WithSkillCacheRoot(tmp),
		WithMaxArchiveSize(64), // 64 bytes total
	)
	data := makeZip(t, map[string]string{
		"SKILL.md": strings.Repeat("A", 4096),
	})
	skill := Skill{
		Key:    "too-big",
		Source: SkillFromArchive{Archive: ArchiveFromBytes(data), Format: SkillArchiveZip},
	}
	_, err := mat.Materialize(context.Background(), skill)
	if err == nil {
		t.Fatal("want size-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention size limit; got %q", err.Error())
	}
}
