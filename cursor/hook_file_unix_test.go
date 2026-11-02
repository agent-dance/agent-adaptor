//go:build unix

package cursor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"golang.org/x/sys/unix"
)

// Replace .cursor at the final external operation after eligibility checking.
// Even an in-workspace symlink is ineligible: hooks require a plain directory.
func TestCursorHookInstallRejectsReplacedDirectory(t *testing.T) {
	for _, replacement := range []string{"outside symlink", "inside symlink", "file"} {
		t.Run(replacement, func(t *testing.T) {
			p := newMessageTestPort(driver.MessageItem{ID: "pending", Text: "guidance"})
			req := managedHookRequest(t, p)
			outside := t.TempDir()
			if replacement == "inside symlink" {
				outside = filepath.Join(req.Workspace.CWD, "other")
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			hookDir := filepath.Join(req.Workspace.CWD, ".cursor")
			previous := cursorHookListen
			listener := &memoryHookListener{closed: make(chan struct{})}
			cursorHookListen = func(string, string) (net.Listener, error) {
				if replacement == "file" {
					if err := os.WriteFile(hookDir, []byte("user file"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(outside, hookDir); err != nil {
					t.Fatal(err)
				}
				return listener, nil
			}
			t.Cleanup(func() { cursorHookListen = previous })
			sink := &messageTestSink{}
			hooks, err := prepareCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil, sink)
			if hooks != nil {
				t.Cleanup(func() { _ = hooks.close() })
			}
			if err != nil || hooks != nil || len(sink.events) != 1 || sink.events[0].Metadata["code"] != "cursor_hook_unavailable" || !strings.Contains(sink.events[0].Text, "turn end via resume") {
				t.Fatalf("hooks=%v err=%v events=%+v", hooks, err, sink.events)
			}
			if len(p.pending) != 1 || len(p.claims) != 0 || len(p.acks) != 0 || len(p.windows) != 0 {
				t.Fatalf("unavailable relay touched pending messages: %+v", p)
			}
			select {
			case <-listener.closed:
			default:
				t.Fatal("failed installation leaked listener")
			}
			if _, err := os.Lstat(filepath.Join(outside, hookFileName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hook installation escaped .cursor: %v", err)
			}
		})
	}
}

func TestCursorHookInstallPreservesLateFile(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[symlink], func(t *testing.T) {
			req := managedHookRequest(t, newMessageTestPort())
			outside := filepath.Join(t.TempDir(), hookFileName)
			want := []byte("user hooks")
			if err := os.WriteFile(outside, want, 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(req.Workspace.CWD, ".cursor", hookFileName)
			previous := cursorHookListen
			cursorHookListen = func(string, string) (net.Listener, error) {
				if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if err := os.Symlink(outside, path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, want, 0o600); err != nil {
					t.Fatal(err)
				}
				return &memoryHookListener{closed: make(chan struct{})}, nil
			}
			t.Cleanup(func() { cursorHookListen = previous })
			hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
			if hooks != nil {
				t.Cleanup(func() { _ = hooks.close() })
			}
			if err == nil || hooks != nil {
				t.Fatalf("late user hooks accepted: hooks=%v err=%v", hooks, err)
			}
			assertHookContents(t, path, want)
			assertHookContents(t, outside, want)
		})
	}
}

func TestCursorHookCleanupRejectsReplacedDirectory(t *testing.T) {
	useMemoryHookListener(t)
	for _, replacement := range []string{"outside symlink", "inside symlink", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			req := managedHookRequest(t, newMessageTestPort())
			hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hooks.close() })
			dir := filepath.Dir(hooks.path)
			original := filepath.Join(req.Workspace.CWD, "original-cursor")
			if err := os.Rename(dir, original); err != nil {
				t.Fatal(err)
			}
			other := t.TempDir()
			if replacement == "inside symlink" {
				other = filepath.Join(req.Workspace.CWD, "other-cursor")
				if err := os.Mkdir(other, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if replacement == "directory" {
				other = dir
				if err := os.Mkdir(other, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(other, dir); err != nil {
				t.Fatal(err)
			}
			// Identical inode and bytes defeat file-only ownership checks: the
			// parent directory must be pinned and checked as well.
			if err := os.Link(filepath.Join(original, hookFileName), filepath.Join(other, hookFileName)); err != nil {
				t.Fatal(err)
			}
			if err := hooks.close(); err == nil {
				t.Fatal("cleanup accepted replaced .cursor")
			}
			assertHookContents(t, filepath.Join(original, hookFileName), hooks.data)
			assertHookContents(t, filepath.Join(other, hookFileName), hooks.data)
		})
	}
}

func TestCursorHookCleanupPreservesReplacedFile(t *testing.T) {
	useMemoryHookListener(t)
	for _, replacement := range []string{"identical file", "symlink", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			req := managedHookRequest(t, newMessageTestPort())
			hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hooks.close() })
			outside := filepath.Join(t.TempDir(), hookFileName)
			if err := os.WriteFile(outside, hooks.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(hooks.path); err != nil {
				t.Fatal(err)
			}
			switch replacement {
			case "identical file":
				err = os.Link(outside, hooks.path)
			case "symlink":
				err = os.Symlink(outside, hooks.path)
			case "fifo":
				err = unix.Mkfifo(hooks.path, 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := hooks.close(); err == nil {
				t.Fatal("cleanup accepted replaced hooks.json")
			}
			assertHookContents(t, outside, hooks.data)
			if _, err := os.Lstat(hooks.path); err != nil {
				t.Fatalf("replacement was removed: %v", err)
			}
			if replacement != "fifo" {
				assertHookContents(t, hooks.path, hooks.data)
			}
		})
	}
}

func TestCursorHookCleanupPinsDirectoryDuringReplacement(t *testing.T) {
	useMemoryHookListener(t)
	req := managedHookRequest(t, newMessageTestPort())
	hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hooks.close() })
	dir := filepath.Dir(hooks.path)
	original := filepath.Join(req.Workspace.CWD, "original-cursor")
	outside := t.TempDir()
	if err := os.Link(hooks.path, filepath.Join(outside, hookFileName)); err != nil {
		t.Fatal(err)
	}
	previous := cursorHookRename
	cursorHookRename = func(fromfd int, from string, tofd int, to string) error {
		if err := os.Rename(dir, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, dir); err != nil {
			t.Fatal(err)
		}
		return previous(fromfd, from, tofd, to)
	}
	t.Cleanup(func() { cursorHookRename = previous })
	if err := hooks.close(); err != nil {
		t.Fatal(err)
	}
	assertHookContents(t, filepath.Join(outside, hookFileName), hooks.data)
	entries, err := os.ReadDir(original)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pinned directory cleanup: entries=%v err=%v", entries, err)
	}
}

func TestCursorHookCleanupPreservesLateReplacement(t *testing.T) {
	useMemoryHookListener(t)
	for _, replacement := range []string{"identical file", "symlink", "restore conflict"} {
		t.Run(replacement, func(t *testing.T) {
			req := managedHookRequest(t, newMessageTestPort())
			hooks, err := startCursorHooks(context.Background(), req, Config{}, req.Workspace.CWD, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hooks.close() })
			outside := filepath.Join(t.TempDir(), hookFileName)
			if err := os.WriteFile(outside, hooks.data, 0o600); err != nil {
				t.Fatal(err)
			}
			previous := cursorHookRename
			cursorHookRename = func(fromfd int, from string, tofd int, to string) error {
				// Arrive after the ownership/content check, immediately before
				// the syscall that would otherwise unlink a user's own file.
				if err := os.Remove(hooks.path); err != nil {
					t.Fatal(err)
				}
				if replacement == "symlink" {
					err = os.Symlink(outside, hooks.path)
				} else {
					err = os.Link(outside, hooks.path)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := previous(fromfd, from, tofd, to); err != nil {
					return err
				}
				if replacement == "restore conflict" {
					return os.WriteFile(hooks.path, []byte("newer user hooks"), 0o600)
				}
				return nil
			}
			t.Cleanup(func() { cursorHookRename = previous })
			if err := hooks.close(); err == nil {
				t.Fatal("cleanup did not report late replacement")
			}
			assertHookContents(t, outside, hooks.data)
			if replacement == "restore conflict" {
				assertHookContents(t, hooks.path, []byte("newer user hooks"))
				entries, err := filepath.Glob(filepath.Join(filepath.Dir(hooks.path), ".agent-adaptor-hook-cleanup-*", hookFileName))
				if err != nil || len(entries) != 1 {
					t.Fatalf("displaced user hooks not preserved: %v %v", entries, err)
				}
				assertHookContents(t, entries[0], hooks.data)
			} else {
				assertHookContents(t, hooks.path, hooks.data)
				entries, err := os.ReadDir(filepath.Dir(hooks.path))
				if err != nil || len(entries) != 1 {
					t.Fatalf("cleanup directory leaked: %v %v", entries, err)
				}
			}
		})
	}
}

func assertHookContents(t *testing.T, path string, want []byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(want) {
		t.Fatalf("%s changed: data=%q err=%v", path, data, err)
	}
}
