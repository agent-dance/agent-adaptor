package agentmarkdown

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMarkdown(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent.md")
	content := "\ufeff---\r\nname: reviewer\r\ndescription: >-\r\n  Reviews changes\r\n  carefully\r\npermission:\r\n  'z*': deny\r\n  '*': allow\r\n---\r\n\r\nPrompt {file:literal}\r\n"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Body != "Prompt {file:literal}" || string(doc.Fields["permission"]) != `{"z*":"deny","*":"allow"}` || string(doc.Fields["description"]) != `"Reviews changes carefully"` {
		t.Fatalf("wrong document: %+v", doc)
	}
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(content, "allow", "ask")), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := Read(file)
	if err != nil || changed.Fingerprint == doc.Fingerprint {
		t.Fatal("source content not fingerprinted")
	}
	if _, err := Read(filepath.Dir(file)); err == nil {
		t.Fatal("directory accepted")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(file); err == nil {
		t.Fatal("missing file accepted")
	}
	if doc.Body != "Prompt {file:literal}" {
		t.Fatal("capture depends on source lifetime")
	}
}

func TestMalformedMarkdown(t *testing.T) {
	for _, raw := range [][]byte{
		nil, {0xff}, {'x', 0}, bytes.Repeat([]byte("x"), maxSize+1),
		[]byte("---\na: 1\n"), []byte("---\n[a, b]\n---\nbody"),
		[]byte("---\na: 1\na: 2\n---"), []byte("---\n1: a\n---"),
		[]byte("---\na: &x [*x]\n---"), []byte("---\na: &x {read: allow}\nb: {<<: *x}\n---"),
		[]byte("---\na: .inf\n---"), []byte("---\na: !!binary eA==\n---"),
		[]byte("---\na: !!timestamp 2026-09-21\n---"), []byte("---\na: !custom x\n---"),
		[]byte("---\na: " + strings.Repeat("[", 66) + "x" + strings.Repeat("]", 66) + "\n---"),
	} {
		if _, err := parse(raw); err == nil {
			t.Fatalf("accepted malformed source of length %d", len(raw))
		}
	}
}

func FuzzMarkdown(f *testing.F) {
	for _, seed := range []string{"body", "---\nname: example\n---\nbody", "---\npermission:\n  read: allow\n---", "---\na: &x [*x]\n---"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) { _, _ = parse(raw) })
}
