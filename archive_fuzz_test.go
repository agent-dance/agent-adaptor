package agentadaptor

import (
	"strings"
	"testing"
)

// FuzzExtractZip exercises the zip extractor against arbitrary byte
// inputs. The materializer MUST never panic, MUST never escape the
// virtual root, and MUST always either produce a benign Files map or a
// classified error wrapping errArchiveFormat.
func FuzzExtractZip(f *testing.F) {
	// Seed corpus: a few valid-ish zip-shaped buffers and some malicious
	// fixtures from the unit tests.
	f.Add(makeZipFuzz(map[string]string{"SKILL.md": "# ok"}))
	f.Add(makeZipFuzz(map[string]string{"../escape": "evil"}))
	f.Add(makeZipFuzz(map[string]string{"/abs/escape": "evil"}))
	f.Add([]byte{0x50, 0x4b, 0x05, 0x06}) // empty zip EOCD only
	f.Add([]byte("not a zip"))
	f.Add([]byte{}) // empty

	f.Fuzz(func(t *testing.T, data []byte) {
		// extractArchive must never panic, even on adversarial bytes.
		extr, err := extractArchive(data, SkillArchiveZip, "", defaultMaterializerConfig())
		if err != nil {
			// Errors are fine; we only insist they're routed through
			// errArchiveFormat so callers can surface them uniformly.
			if !strings.HasPrefix(err.Error(), "agentadaptor: archive format error") {
				t.Errorf("zip fuzz: error not routed through errArchiveFormat: %q", err.Error())
			}
			return
		}
		// Successful extraction must never produce paths that would
		// escape the staging directory.
		assertNoEscape(t, "zip fuzz", extr.Files)
	})
}

// FuzzExtractTar exercises the tar extractor against arbitrary inputs.
func FuzzExtractTar(f *testing.F) {
	f.Add(makeTarGzFuzz(map[string]string{"SKILL.md": "# ok"}))
	f.Add(makeTarGzFuzz(map[string]string{"../escape": "evil"}))
	f.Add([]byte("not a tarball"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		extr, err := extractArchive(data, SkillArchiveTarGz, "", defaultMaterializerConfig())
		if err != nil {
			if !strings.HasPrefix(err.Error(), "agentadaptor: archive format error") {
				t.Errorf("tar fuzz: error not routed through errArchiveFormat: %q", err.Error())
			}
			return
		}
		assertNoEscape(t, "tar fuzz", extr.Files)
	})
}

// assertNoEscape verifies the extracted file map has no entry whose
// path would resolve outside the staging tree. The check operates on
// path SEGMENTS (split by /) rather than raw substrings so legitimate
// filenames containing ".." as part of a longer name (e.g. "..foo")
// are not flagged.
func assertNoEscape(t *testing.T, label string, files map[string][]byte) {
	t.Helper()
	for name := range files {
		if strings.HasPrefix(name, "/") {
			t.Errorf("%s: absolute path leaked: %q", label, name)
			continue
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == ".." {
				t.Errorf("%s: traversal segment leaked: %q", label, name)
				break
			}
		}
	}
}

// FuzzSniffArchiveFormat exercises the format-sniffing routine. The
// sniffer must always return one of the four enum values and never panic.
func FuzzSniffArchiveFormat(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("hi"))
	f.Add([]byte{0x50, 0x4b, 0x03, 0x04})
	f.Add([]byte{0x1f, 0x8b})

	f.Fuzz(func(t *testing.T, data []byte) {
		got := sniffArchiveFormat(data)
		switch got {
		case SkillArchiveAuto, SkillArchiveZip, SkillArchiveTar, SkillArchiveTarGz:
			// fine
		default:
			t.Errorf("sniffArchiveFormat returned unexpected value: %q", got)
		}
	})
}

// makeZipFuzz / makeTarGzFuzz are panic-on-fail seed builders so fuzz
// corpus construction surfaces shape errors clearly even before t.Run
// scope is established.

func makeZipFuzz(entries map[string]string) []byte {
	t := &panicTB{}
	return makeZip(t, entries)
}

func makeTarGzFuzz(entries map[string]string) []byte {
	t := &panicTB{}
	return makeTarGz(t, entries)
}

// panicTB is a minimal testing.TB stand-in for fuzz seed construction.
// It panics on Fatalf / Errorf so corpus shape bugs surface immediately
// rather than silently producing zero-byte fixtures.
type panicTB struct {
	testing.TB
}

func (p *panicTB) Helper()                                   {}
func (p *panicTB) Fatalf(format string, args ...interface{}) { panic("fuzz seed: " + format) }
func (p *panicTB) Errorf(format string, args ...interface{}) { panic("fuzz seed: " + format) }
func (p *panicTB) Fatal(args ...interface{})                 { panic("fuzz seed: fatal") }
