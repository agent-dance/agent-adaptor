package profileinstructions

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/profilestate"
)

func TestNativeInstructionModeChangeRollsBackOnFilesystemFailure(t *testing.T) {
	for _, step := range []string{"remove", "commit"} {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			names := []string{"APPEND_SYSTEM.md", "SYSTEM.md"}
			original := &NativeFile{Name: names[0], Key: "old", Content: "original"}
			if _, err := SyncNativeFiles(ctx, "pi", dir, names, original); err != nil {
				t.Fatal(err)
			}
			oldPath := filepath.Join(dir, names[0])
			lockedPath := oldPath
			if step == "commit" {
				lockedPath = filepath.Join(dir, profilestate.ManifestName)
			}
			path, err := syscall.UTF16PtrFromString(lockedPath)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer syscall.CloseHandle(handle)
			_, err = SyncNativeFiles(ctx, "pi", dir, names, &NativeFile{Name: names[1], Key: "new", Content: "replacement"})
			if err == nil {
				t.Fatal("sharing violation should fail the mode change on Windows")
			}
			if _, err := os.Stat(filepath.Join(dir, names[1])); !os.IsNotExist(err) {
				t.Fatalf("failed mode switch retained replacement instructions: %v", err)
			}
			if raw, err := os.ReadFile(oldPath); err != nil || string(raw) != "original" {
				t.Fatalf("failed switch lost original: %v", err)
			}
			snapshot, err := ObserveNativeFiles("pi", dir, names, original)
			if err != nil || len(snapshot.Managed) != 1 || snapshot.Managed[0] != "old" {
				t.Fatalf("failed switch changed ownership: %+v %v", snapshot, err)
			}
		})
	}
}
