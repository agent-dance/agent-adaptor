package mcpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
	toml "github.com/pelletier/go-toml/v2"
)

func TestResourceWriterPreservesModeAndUnknownConfiguration(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "cursor", "codebuddy"} {
		for _, mode := range []fs.FileMode{0, 0400, 0600, 0644} {
			t.Run(fmt.Sprintf("%s/%04o", provider, mode), func(t *testing.T) {
				dir := t.TempDir()
				layout, err := layoutFor(provider, dir)
				if err != nil {
					t.Fatal(err)
				}
				unknown := map[string]any{"unknown_setting": map[string]any{"label": "keep", "enabled": true}}
				if mode != 0 {
					var raw []byte
					if layout.format == "toml" {
						raw, err = toml.Marshal(unknown)
					} else {
						raw, err = json.Marshal(unknown)
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(layout.path, raw, mode); err != nil {
						t.Fatal(err)
					}
				}
				for _, endpoint := range []string{"https://example.invalid/one", "https://example.invalid/two"} {
					payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "ordinary", Transport: driver.MCPTransportHTTP, URL: endpoint}}}
					if _, err := SyncResource(context.Background(), provider, dir, ProfileKindHostManaged, payload); err != nil {
						t.Fatal(err)
					}
					info, err := os.Lstat(layout.path)
					if err != nil {
						t.Fatal(err)
					}
					want := mode
					if want == 0 {
						want = 0644
					}
					if info.Mode().Perm() != want {
						t.Fatalf("existing permission changed: got %04o want %04o", info.Mode().Perm(), want)
					}
					root, err := readStructuredRoot(layout)
					if err != nil {
						t.Fatal(err)
					}
					if mode != 0 {
						setting := mapFromAny(root["unknown_setting"])
						if setting["label"] != "keep" || setting["enabled"] != true {
							t.Fatalf("unknown configuration overwritten: %#v", setting)
						}
					}
					section, err := sectionMap(root, layout.field)
					if err != nil || mapFromAny(section["ordinary"])["url"] != endpoint {
						t.Fatal("MCP update was not written", err)
					}
				}
			})
		}
	}
}

func TestResourceWriterRejectsNonregularAndIO(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, kind := range []string{"symlink", "directory", "parent-io"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				layout, err := layoutFor(provider, dir)
				if err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(t.TempDir(), "outside")
				original := []byte("outside content")
				if err := os.WriteFile(outside, original, 0600); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "symlink":
					if err := os.Symlink(outside, layout.path); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(layout.path, 0700); err != nil {
						t.Fatal(err)
					}
				default:
					layout.path = filepath.Join(outside, "config")
				}
				err = writeStructuredRoot(layout, map[string]any{"configured": true})
				if kind == "parent-io" {
					var pathErr *os.PathError
					if !errors.As(err, &pathErr) {
						t.Fatal("IO error lost", err)
					}
				} else if !errors.Is(err, profile.ErrUnsafe) {
					t.Fatal("nonregular config accepted", err)
				}
				raw, err := os.ReadFile(outside)
				if err != nil || string(raw) != string(original) {
					t.Fatal("outside file modified", err)
				}
				if kind == "symlink" {
					if _, err := os.Readlink(layout.path); err != nil {
						t.Fatal("symlink replaced", err)
					}
				}
			})
		}
	}
}
