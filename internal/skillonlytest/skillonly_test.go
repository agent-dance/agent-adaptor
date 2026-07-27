// Package skillonlytest is an external-consumer smoke test. Keep its import
// graph limited to the public skill package and the Go standard library: it
// proves archive materialization does not depend on loading the root package
// or a root init hook.
package skillonlytest

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/skill"
)

func TestArchiveMaterializesThroughPublicSkillPackage(t *testing.T) {
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	entry, err := zw.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("# isolated\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	materializer := skill.NewDefaultSkillMaterializer(
		skill.WithSkillCacheRoot(t.TempDir()),
	)
	path, err := materializer.Materialize(context.Background(), skill.Archive(
		"isolated",
		skill.ArchiveBytes(archive.Bytes()),
		skill.WithFormat(skill.FormatZip),
	))
	if err != nil {
		t.Fatalf("Materialize() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(path, "SKILL.md"))
	if err != nil {
		t.Fatalf("read materialized SKILL.md: %v", err)
	}
	if string(got) != "# isolated\n" {
		t.Fatalf("SKILL.md = %q", got)
	}
}
