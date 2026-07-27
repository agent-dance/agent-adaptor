package skill_test

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
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/skill"
)

// Compile-time proof that every constructor result is a Ref in both
// vocabularies (alias identity, no conversion involved).
var (
	_ skill.Ref               = skill.Skill{}
	_ agentadaptor.SkillRef   = skill.Skill{}
	_ skill.Materializer      = agentadaptor.SkillMaterializer(nil)
	_ skill.Provider          = skill.Set{}
	_ skill.Catalog           = skill.Set{}
	_ skill.Source            = skill.ArchiveSource{}
	_ skill.Opener            = agentadaptor.ArchiveFromBytes(nil)
	_ skill.ArchiveHTTPOption = agentadaptor.WithArchiveHeader("k", "v")
)

func TestDirMatchesLocalSkill(t *testing.T) {
	path := filepath.Join("skills", "write-proof")

	got := skill.Dir(path)
	if got.Key != "write-proof" {
		t.Fatalf("skill.Dir(%q).Key = %q", path, got.Key)
	}

	if got.Key != "write-proof" {
		t.Fatalf("Key = %q, want %q", got.Key, "write-proof")
	}
	src, ok := got.Source.(skill.PathSource)
	if !ok {
		t.Fatalf("Source type = %T, want SkillFromPath", got.Source)
	}
	if src.Path != path {
		t.Fatalf("SkillFromPath.Path = %q, want %q", src.Path, path)
	}
}

func TestFSMatchesFSSkill(t *testing.T) {
	fsys := fstest.MapFS{
		"bundle/code-review/SKILL.md": &fstest.MapFile{Data: []byte("# code review")},
	}

	got := skill.FS(fsys, "bundle/code-review")
	if got.Key != "code-review" {
		t.Fatalf("skill.FS.Key = %q", got.Key)
	}

	if got.Key != "code-review" {
		t.Fatalf("Key = %q, want %q", got.Key, "code-review")
	}
	src, ok := got.Source.(skill.FSSource)
	if !ok {
		t.Fatalf("Source type = %T, want SkillFromFS", got.Source)
	}
	if src.Root != "bundle/code-review" {
		t.Fatalf("SkillFromFS.Root = %q, want %q", src.Root, "bundle/code-review")
	}
	if !reflect.DeepEqual(src.FS, fsys) {
		t.Fatalf("SkillFromFS.FS not passed through")
	}

	// Empty / "." root falls back to the default key.
	if got := skill.FS(fsys, ""); got.Key != "skill" {
		t.Fatalf("default key for empty root = %q, want %q", got.Key, "skill")
	}
}

func TestInlineMatchesInlineSkill(t *testing.T) {
	got := skill.Inline("greet", "# Greeting\nSay hi.")
	if got.Key != "greet" {
		t.Fatalf("skill.Inline.Key = %q", got.Key)
	}

	if got.Key != "greet" {
		t.Fatalf("Key = %q, want %q", got.Key, "greet")
	}
	src, ok := got.Source.(skill.InlineSource)
	if !ok {
		t.Fatalf("Source type = %T, want SkillFromInline", got.Source)
	}
	if src.SkillMD != "# Greeting\nSay hi." {
		t.Fatalf("SkillFromInline.SkillMD = %q", src.SkillMD)
	}
}

func TestKeyMatchesRootKey(t *testing.T) {
	got := skill.Key("deploy-checklist")
	want := agentadaptor.Key("deploy-checklist")
	if got != want {
		t.Fatalf("skill.Key = %#v, want %#v", got, want)
	}
	sk, ok := got.(agentadaptor.SkillKey)
	if !ok {
		t.Fatalf("dynamic type = %T, want SkillKey", got)
	}
	if string(sk) != "deploy-checklist" {
		t.Fatalf("SkillKey = %q, want %q", sk, "deploy-checklist")
	}
}

func TestRequireMatchesRoot(t *testing.T) {
	base := skill.Dir(filepath.Join("skills", "write-proof"))

	got := skill.Require(base, "compliance mandate")
	want := agentadaptor.Require(base, "compliance mandate")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("skill.Require = %#v, want %#v", got, want)
	}
	if !got.Required || got.Reason != "compliance mandate" {
		t.Fatalf("Required/Reason not set: %#v", got)
	}
	if base.Required {
		t.Fatalf("Require mutated its input")
	}
}

func TestMetadataConstantsMatchRoot(t *testing.T) {
	if skill.MetadataRuntimeName != agentadaptor.SkillMetadataRuntimeName {
		t.Fatalf("MetadataRuntimeName = %q, want %q",
			skill.MetadataRuntimeName, agentadaptor.SkillMetadataRuntimeName)
	}
	if skill.MetadataDisplayName != agentadaptor.SkillMetadataDisplayName {
		t.Fatalf("MetadataDisplayName = %q, want %q",
			skill.MetadataDisplayName, agentadaptor.SkillMetadataDisplayName)
	}
}

// TestRuntimeNameOverride verifies the naming-override capability is
// reachable through this package: MetadataRuntimeName controls the
// directory name the default materializer writes.
func TestRuntimeNameOverride(t *testing.T) {
	s := skill.Inline("team/retention", "# Retention playbook")
	s.Metadata = map[string]string{skill.MetadataRuntimeName: "retention-playbook"}

	var m skill.Materializer = agentadaptor.NewDefaultSkillMaterializer(
		agentadaptor.WithSkillCacheRoot(t.TempDir()))
	dir, err := m.Materialize(context.Background(), s)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if base := filepath.Base(dir); !strings.HasPrefix(base, "retention-playbook--") {
		t.Fatalf("materialized dir %q does not use runtime-name override", base)
	}
}

func TestArchiveFieldPassthrough(t *testing.T) {
	payload := []byte("archive-bytes")
	open := skill.ArchiveBytes(payload)

	got := skill.Archive("kit", open,
		skill.WithFormat(skill.FormatZip),
		skill.WithSubpath("inner"),
		skill.WithFingerprint("sha256:abc"),
	)
	if got.Key != "kit" {
		t.Fatalf("Key = %q, want %q", got.Key, "kit")
	}
	src, ok := got.Source.(skill.ArchiveSource)
	if !ok {
		t.Fatalf("Source type = %T, want SkillFromArchive", got.Source)
	}
	if src.Format != skill.FormatZip {
		t.Fatalf("Format = %q, want %q", src.Format, agentadaptor.SkillArchiveZip)
	}
	if src.Subpath != "inner" {
		t.Fatalf("Subpath = %q, want %q", src.Subpath, "inner")
	}
	if src.Fingerprint != "sha256:abc" {
		t.Fatalf("Fingerprint = %q, want %q", src.Fingerprint, "sha256:abc")
	}
	rc, err := src.Archive(context.Background())
	if err != nil {
		t.Fatalf("Archive open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Archive read: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("Archive bytes = %q, want %q", data, payload)
	}

	// Defaults: no options → auto format, empty subpath / fingerprint.
	def := skill.Archive("kit2", open)
	src = def.Source.(skill.ArchiveSource)
	if src.Format != skill.FormatAuto || src.Subpath != "" || src.Fingerprint != "" {
		t.Fatalf("defaults not zero: %#v", src)
	}
}

// TestArchiveMaterializesAllFormats proves skill.Archive feeds the
// built-in materializer for the three supported formats, both via
// magic-byte sniffing (FormatAuto) and with an explicit WithFormat.
func TestArchiveMaterializesAllFormats(t *testing.T) {
	files := map[string]string{
		"SKILL.md":       "# Deploy kit",
		"refs/notes.txt": "runbook notes",
	}
	cases := []struct {
		name string
		key  string
		data []byte
		opts []skill.ArchiveOption
	}{
		{name: "zip-auto", key: "kit-zip", data: zipBytes(t, files)},
		{name: "tar-auto", key: "kit-tar", data: tarBytes(t, files)},
		{name: "tgz-auto", key: "kit-tgz", data: tgzBytes(t, files)},
		{name: "zip-explicit", key: "kit-zip-explicit", data: zipBytes(t, files),
			opts: []skill.ArchiveOption{skill.WithFormat(skill.FormatZip)}},
		{name: "tar-explicit", key: "kit-tar-explicit", data: tarBytes(t, files),
			opts: []skill.ArchiveOption{skill.WithFormat(skill.FormatTar)}},
		{name: "tgz-explicit", key: "kit-tgz-explicit", data: tgzBytes(t, files),
			opts: []skill.ArchiveOption{skill.WithFormat(skill.FormatTarGz)}},
	}

	m := agentadaptor.NewDefaultSkillMaterializer(agentadaptor.WithSkillCacheRoot(t.TempDir()))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := skill.Archive(tc.key, skill.ArchiveBytes(tc.data), tc.opts...)
			dir, err := m.Materialize(context.Background(), s)
			if err != nil {
				t.Fatalf("Materialize: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
			if err != nil {
				t.Fatalf("read SKILL.md: %v", err)
			}
			if string(content) != files["SKILL.md"] {
				t.Fatalf("SKILL.md = %q, want %q", content, files["SKILL.md"])
			}
			ref, err := os.ReadFile(filepath.Join(dir, "refs", "notes.txt"))
			if err != nil {
				t.Fatalf("read refs/notes.txt: %v", err)
			}
			if string(ref) != files["refs/notes.txt"] {
				t.Fatalf("refs/notes.txt = %q, want %q", ref, files["refs/notes.txt"])
			}
		})
	}
}

func TestArchiveSubpath(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"bundle/SKILL.md":  "# Bundled skill",
		"bundle/extra.txt": "extra",
		"unrelated.txt":    "ignored",
	})
	m := agentadaptor.NewDefaultSkillMaterializer(agentadaptor.WithSkillCacheRoot(t.TempDir()))

	s := skill.Archive("kit-sub", skill.ArchiveBytes(data), skill.WithSubpath("bundle"))
	dir, err := m.Materialize(context.Background(), s)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if string(content) != "# Bundled skill" {
		t.Fatalf("SKILL.md = %q", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); !os.IsNotExist(err) {
		t.Fatalf("entry outside subpath leaked into %s", dir)
	}
}

// TestArchiveFormatMismatch proves WithFormat really reaches the
// extractor: pinning zip on a tar stream must fail instead of falling
// back to sniffing.
func TestArchiveFormatMismatch(t *testing.T) {
	data := tarBytes(t, map[string]string{"SKILL.md": "# tar"})
	m := agentadaptor.NewDefaultSkillMaterializer(agentadaptor.WithSkillCacheRoot(t.TempDir()))

	s := skill.Archive("kit-mismatch", skill.ArchiveBytes(data), skill.WithFormat(skill.FormatZip))
	if _, err := m.Materialize(context.Background(), s); err == nil {
		t.Fatalf("Materialize accepted tar bytes pinned as zip")
	}
}

func TestArchiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kit.zip")
	payload := zipBytes(t, map[string]string{"SKILL.md": "# from file"})
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	rc, err := skill.ArchiveFile(path)(context.Background())
	if err != nil {
		t.Fatalf("ArchiveFile open: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ArchiveFile read: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("ArchiveFile bytes differ from written archive")
	}

	if _, err := skill.ArchiveFile(filepath.Join(t.TempDir(), "missing.zip"))(context.Background()); err == nil {
		t.Fatalf("ArchiveFile of missing path succeeded")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestArchiveURL(t *testing.T) {
	payload := []byte("remote-archive")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/kit.tgz" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Header option passthrough.
	rc, err := skill.ArchiveURL(srv.URL+"/kit.tgz",
		skill.WithArchiveHeader("X-Token", "secret"))(context.Background())
	if err != nil {
		t.Fatalf("ArchiveURL: %v", err)
	}
	data, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ArchiveURL read: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("ArchiveURL bytes = %q, want %q", data, payload)
	}

	// Missing header → 401 → error.
	if _, err := skill.ArchiveURL(srv.URL + "/kit.tgz")(context.Background()); err == nil {
		t.Fatalf("ArchiveURL without header succeeded")
	}

	// Custom client passthrough.
	used := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	rc, err = skill.ArchiveURL(srv.URL+"/kit.tgz",
		skill.WithArchiveHeader("X-Token", "secret"),
		skill.WithArchiveHTTPClient(client))(context.Background())
	if err != nil {
		t.Fatalf("ArchiveURL with client: %v", err)
	}
	_ = rc.Close()
	if !used {
		t.Fatalf("WithArchiveHTTPClient not honoured")
	}
}

// TestProviderCatalogAliases exercises the re-exported interfaces via
// the built-in Set implementation to prove alias identity is
// behavioural, not just nominal.
func TestProviderCatalogAliases(t *testing.T) {
	set := skill.Set{
		"code-review": skill.Inline("code-review", "# Review"),
		"mandatory":   skill.Require(skill.Inline("mandatory", "# Always"), "tenant policy"),
	}

	var p skill.Provider = set
	var rootP agentadaptor.SkillProvider = p // alias identity
	got, err := rootP.GetSkills(context.Background(), []string{"code-review"})
	if err != nil {
		t.Fatalf("GetSkills: %v", err)
	}
	if _, ok := got["code-review"]; !ok {
		t.Fatalf("GetSkills missing requested key: %v", got)
	}
	if _, ok := got["mandatory"]; !ok {
		t.Fatalf("GetSkills dropped Required skill: %v", got)
	}

	var c skill.Catalog = set
	all, err := c.Catalogue(context.Background())
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Catalogue len = %d, want 2", len(all))
	}
}

// TestConsumableByRootOptions proves the constructors plug into the
// existing root-package option surface without conversion — the
// forward-compatibility contract for v1 WithSkills(refs ...skill.Ref).
func TestConsumableByRootOptions(t *testing.T) {
	refs := []skill.Ref{
		skill.Dir(filepath.Join("skills", "write-proof")),
		skill.Key("code-review"),
	}
	var rootRefs []agentadaptor.SkillRef = refs // alias identity, no copy

	var _ agentadaptor.AgentOption = agentadaptor.WithDefaultSkills(rootRefs...)
	var _ agentadaptor.RunOption = agentadaptor.WithSkills(
		skill.Inline("greet", "# hi"),
		skill.Archive("kit", skill.ArchiveBytes([]byte("x"))),
	)
	var _ agentadaptor.Option = agentadaptor.WithSkillProvider(skill.Set{})
	var _ agentadaptor.Option = agentadaptor.WithSkillMaterializer(
		agentadaptor.NewDefaultSkillMaterializer())
}

// --- archive builders -------------------------------------------------------

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %q: %v", name, err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func tarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatalf("tar header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

func tgzBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(tarBytes(t, files)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
