package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/skillmaterializer"
	"github.com/agent-dance/agent-adaptor/skill"
)

func FuzzExtractZip(f *testing.F) {
	f.Add(makeZipFuzz(map[string]string{"SKILL.md": "# ok"}))
	f.Add(makeZipFuzz(map[string]string{"../escape": "evil"}))
	f.Add(makeZipFuzz(map[string]string{"/abs/escape": "evil"}))
	f.Add([]byte{0x50, 0x4b, 0x05, 0x06})
	f.Add([]byte("not a zip"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		extracted, err := skillmaterializer.ExtractArchive(data, skillmaterializer.FormatZip, "", archiveFuzzConfig())
		if err != nil {
			if !strings.HasPrefix(err.Error(), "agentadaptor: archive format error") {
				t.Errorf("zip fuzz: unclassified error: %q", err.Error())
			}
			return
		}
		assertNoEscape(t, "zip fuzz", extracted.Files)
	})
}

func FuzzExtractTar(f *testing.F) {
	f.Add(makeTarGzFuzz(map[string]string{"SKILL.md": "# ok"}))
	f.Add(makeTarGzFuzz(map[string]string{"../escape": "evil"}))
	f.Add([]byte("not a tarball"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		extracted, err := skillmaterializer.ExtractArchive(data, skillmaterializer.FormatTarGz, "", archiveFuzzConfig())
		if err != nil {
			if !strings.HasPrefix(err.Error(), "agentadaptor: archive format error") {
				t.Errorf("tar fuzz: unclassified error: %q", err.Error())
			}
			return
		}
		assertNoEscape(t, "tar fuzz", extracted.Files)
	})
}

func FuzzSniffArchiveFormat(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("hi"))
	f.Add([]byte{0x50, 0x4b, 0x03, 0x04})
	f.Add([]byte{0x1f, 0x8b})

	f.Fuzz(func(t *testing.T, data []byte) {
		switch got := skillmaterializer.SniffArchiveFormat(data); got {
		case skillmaterializer.FormatAuto, skillmaterializer.FormatZip, skillmaterializer.FormatTar, skillmaterializer.FormatTarGz:
		default:
			t.Errorf("SniffArchiveFormat returned unexpected value: %q", got)
		}
	})
}

func archiveFuzzConfig() skillmaterializer.Config {
	return skillmaterializer.Config{
		MaxArchiveBytes:   256 << 20,
		MaxFileBytes:      64 << 20,
		MaxArchiveEntries: 10000,
		SourceMissing:     skill.ErrSkillSourceMissing,
	}
}

func assertNoEscape(t *testing.T, label string, files map[string][]byte) {
	t.Helper()
	for name := range files {
		if strings.HasPrefix(name, "/") {
			t.Errorf("%s: absolute path leaked: %q", label, name)
			continue
		}
		for _, segment := range strings.Split(name, "/") {
			if segment == ".." {
				t.Errorf("%s: traversal segment leaked: %q", label, name)
				break
			}
		}
	}
}

func makeZipFuzz(entries map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		entry, err := w.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func makeTarGzFuzz(entries map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			panic(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			panic(err)
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
