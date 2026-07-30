package tool_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionPackageDoesNotImportAdaptorImplementationLayers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		content := string(raw)
		for _, forbidden := range []string{
			`github.com/agent-dance/agent-adaptor/driver`,
			`github.com/agent-dance/agent-adaptor/internal`,
			`github.com/agent-dance/agent-adaptor/mcp`,
		} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s imports forbidden implementation vocabulary %q", entry.Name(), forbidden)
			}
		}
	}
}
