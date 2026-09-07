package mcpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
				probe := layout.path
				requestedMode := mode
				if mode == 0 {
					requestedMode = 0644
					probe = filepath.Join(t.TempDir(), "default-mode")
					if err := os.WriteFile(probe, nil, requestedMode); err != nil {
						t.Fatal(err)
					}
				}
				beforeInfo, err := os.Lstat(probe)
				if err != nil {
					t.Fatal(err)
				}
				want := beforeInfo.Mode().Perm()
				if runtime.GOOS != "windows" && want != requestedMode {
					t.Fatalf("POSIX fixture mode: got %04o want %04o", want, requestedMode)
				}
				for _, endpoint := range []string{"https://example.invalid/one", "https://example.invalid/two"} {
					payload := driver.MCPPayload{Servers: []driver.MCPServerSpec{{Key: "ordinary", Transport: driver.MCPTransportHTTP, URL: endpoint}}}
					before, readErr := os.ReadFile(layout.path)
					if readErr != nil && !os.IsNotExist(readErr) {
						t.Fatal(readErr)
					}
					_, syncErr := SyncResource(context.Background(), provider, dir, ProfileKindHostManaged, payload)
					readonlyRejected := runtime.GOOS == "windows" && want&0200 == 0 && errors.Is(syncErr, fs.ErrPermission)
					if syncErr != nil && !readonlyRejected {
						t.Fatal(syncErr)
					}
					info, err := os.Lstat(layout.path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != want {
						t.Fatalf("existing permission changed: got %04o want %04o", info.Mode().Perm(), want)
					}
					if readonlyRejected {
						after, err := os.ReadFile(layout.path)
						if err != nil || string(after) != string(before) {
							t.Fatal("readonly rejection changed original bytes", err)
						}
						t.Log("Windows refused readonly replacement; permission and content preserved")
						continue
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
