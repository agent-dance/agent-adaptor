package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveSkill builds the Skill value the tests below feed into the
// merger. The opener is captured once so callers can decide whether two
// Skills share an origin or genuinely differ.
func archiveSkill(key string, open func(context.Context) (io.ReadCloser, error), opts ...func(*SkillFromArchive)) Skill {
	src := SkillFromArchive{Archive: open, Format: SkillArchiveZip}
	for _, opt := range opts {
		opt(&src)
	}
	return Skill{Key: key, Source: src}
}

// TestSkillSourcesEquivalent_Archive pins conservative archive declaration
// equality: only an explicit stable fingerprint can establish equivalence.
// Function identity is deliberately ignored because closures sharing code may
// capture different content.
func TestSkillSourcesEquivalent_Archive(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{"SKILL.md": "# kit"})
	opener := ArchiveFromBytes(data)
	other := ArchiveFromPath(filepath.Join(t.TempDir(), "kit.zip"))

	cases := []struct {
		name string
		a    SkillFromArchive
		b    SkillFromArchive
		want bool
	}{
		{
			name: "same opener",
			a:    SkillFromArchive{Archive: opener, Format: SkillArchiveZip},
			b:    SkillFromArchive{Archive: opener, Format: SkillArchiveZip},
			want: false,
		},
		{
			name: "subpath normalized",
			a:    SkillFromArchive{Archive: opener, Subpath: "./docs"},
			b:    SkillFromArchive{Archive: opener, Subpath: "docs"},
			want: false,
		},
		{
			name: "same fingerprint different opener",
			a:    SkillFromArchive{Archive: opener, Fingerprint: "sha256:abc"},
			b:    SkillFromArchive{Archive: other, Fingerprint: "sha256:abc"},
			want: true,
		},
		{
			name: "different fingerprint",
			a:    SkillFromArchive{Archive: opener, Fingerprint: "sha256:abc"},
			b:    SkillFromArchive{Archive: opener, Fingerprint: "sha256:def"},
			want: false,
		},
		{
			name: "different format",
			a:    SkillFromArchive{Archive: opener, Format: SkillArchiveZip},
			b:    SkillFromArchive{Archive: opener, Format: SkillArchiveTarGz},
			want: false,
		},
		{
			name: "different subpath",
			a:    SkillFromArchive{Archive: opener, Subpath: "docs"},
			b:    SkillFromArchive{Archive: opener, Subpath: "kit"},
			want: false,
		},
		{
			name: "different opener kind",
			a:    SkillFromArchive{Archive: opener},
			b:    SkillFromArchive{Archive: other},
			want: false,
		},
		{
			name: "nil vs opener",
			a:    SkillFromArchive{},
			b:    SkillFromArchive{Archive: opener},
			want: false,
		},
		{
			name: "both nil",
			a:    SkillFromArchive{},
			b:    SkillFromArchive{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := skillSourcesEquivalent(tc.a, tc.b); got != tc.want {
				t.Fatalf("skillSourcesEquivalent = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSkillSourcesEquivalent_ArchiveCrossFamily guards the archive branch
// against matching a differently-shaped source that happens to carry the
// same key.
func TestSkillSourcesEquivalent_ArchiveCrossFamily(t *testing.T) {
	t.Parallel()
	opener := ArchiveFromBytes(makeZip(t, map[string]string{"SKILL.md": "# kit"}))
	archive := SkillFromArchive{Archive: opener}
	if skillSourcesEquivalent(archive, SkillFromInline{SkillMD: "# kit"}) {
		t.Fatal("archive source must not equal an inline source")
	}
	if skillSourcesEquivalent(SkillFromInline{SkillMD: "# kit"}, archive) {
		t.Fatal("inline source must not equal an archive source")
	}
}

func TestSkillSourcesEquivalent_ArchiveClosuresRequireFingerprint(t *testing.T) {
	t.Parallel()
	first := ArchiveFromBytes([]byte("first"))
	second := ArchiveFromBytes([]byte("second"))
	if skillSourcesEquivalent(
		SkillFromArchive{Archive: first},
		SkillFromArchive{Archive: second},
	) {
		t.Fatal("closures from one constructor with different captures must conflict")
	}

	plainA := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("a")), nil
	}
	plainB := func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("b")), nil
	}
	if skillSourcesEquivalent(
		SkillFromArchive{Archive: plainA},
		SkillFromArchive{Archive: plainB},
	) {
		t.Fatal("plain closures without a fingerprint must conflict")
	}
}

// TestResolveSkills_ArchiveNoSelfConflict is the end-to-end regression for
// the same bug: Admin paths register binding defaults as candidates and
// then again as defaults, so one Skill value reaches the merger twice.
// It also doubles as the P5.2 decoupling probe — package engine cannot
// import the root package, so a green run proves the default materializer
// works without the deleted init() injection.
func TestResolveSkills_ArchiveNoSelfConflict(t *testing.T) {
	t.Parallel()
	data := makeZip(t, map[string]string{
		"SKILL.md":      "# deploy kit",
		"refs/note.txt": "hi",
	})
	sk := archiveSkill("deploy-kit", ArchiveFromBytes(data))
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir()))

	payload, selected, _, err := resolveSkillsWith(
		context.Background(), nil, mat, AgentIdentity{},
		[]SkillRef{sk}, nil, []SkillRef{sk},
	)
	if err != nil {
		t.Fatalf("resolveSkillsWith: %v", err)
	}
	if len(selected) != 1 || selected[0] != "deploy-kit" {
		t.Fatalf("selected = %v, want [deploy-kit]", selected)
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(payload.Entries))
	}
	body, err := os.ReadFile(filepath.Join(payload.Entries[0].SourcePath, "SKILL.md"))
	if err != nil {
		t.Fatalf("materialized SKILL.md: %v", err)
	}
	if string(body) != "# deploy kit" {
		t.Fatalf("SKILL.md body = %q", body)
	}
	if _, err := os.Stat(filepath.Join(payload.Entries[0].SourcePath, "refs", "note.txt")); err != nil {
		t.Fatalf("refs/note.txt should exist: %v", err)
	}
}

// TestResolveSkills_ArchiveConflictStillDetected keeps the fix honest:
// two genuinely different archives under one key must still be rejected.
func TestResolveSkills_ArchiveConflictStillDetected(t *testing.T) {
	t.Parallel()
	first := archiveSkill("deploy-kit", ArchiveFromBytes(makeZip(t, map[string]string{"SKILL.md": "# one"})),
		func(s *SkillFromArchive) { s.Fingerprint = "sha256:one" })
	second := archiveSkill("deploy-kit", ArchiveFromBytes(makeZip(t, map[string]string{"SKILL.md": "# two"})),
		func(s *SkillFromArchive) { s.Fingerprint = "sha256:two" })
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir()))

	_, _, _, err := resolveSkillsWith(
		context.Background(), nil, mat, AgentIdentity{},
		[]SkillRef{first}, []SkillRef{second}, nil,
	)
	if !errors.Is(err, ErrSkillKeyConflict) {
		t.Fatalf("err = %v, want ErrSkillKeyConflict", err)
	}
}

func TestArchiveFingerprintDoesNotControlContentCache(t *testing.T) {
	t.Parallel()
	mat := NewDefaultSkillMaterializer(WithSkillCacheRoot(t.TempDir()))
	content := makeZip(t, map[string]string{"SKILL.md": "# same"})

	materialize := func(body []byte, fingerprint string) string {
		t.Helper()
		path, err := mat.Materialize(context.Background(), Skill{
			Key: "kit",
			Source: SkillFromArchive{
				Archive: ArchiveFromBytes(body), Fingerprint: fingerprint,
			},
		})
		if err != nil {
			t.Fatalf("Materialize(%q): %v", fingerprint, err)
		}
		return path
	}

	if first, second := materialize(content, "revision-one"), materialize(content, "revision-two"); first != second {
		t.Fatalf("same content with different fingerprints must share cache: %q != %q", first, second)
	}
	changed := makeZip(t, map[string]string{"SKILL.md": "# changed"})
	if first, second := materialize(content, "same-revision"), materialize(changed, "same-revision"); first == second {
		t.Fatalf("different content with the same fingerprint must not share cache: %q", first)
	}
}
