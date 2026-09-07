package systemprompt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/pelletier/go-toml/v2"
)

func requireReason(t *testing.T, err error, reason string) {
	t.Helper()
	var typed *driver.SystemPromptUnsupportedError
	if !errors.Is(err, driver.ErrSystemPromptUnsupported) || !errors.As(err, &typed) || typed.Reason != reason {
		t.Fatalf("error=%v want %s", err, reason)
	}
}
func TestValidationAndEncoding(t *testing.T) {
	for _, value := range []string{"", " \n", "甲\n\"乙\"", "'quote' \\ tab\t CR\r", "\b\f\x01\x7f", "replacement:�", strings.Repeat("字", 12000)} {
		if err := Validate("test", value); err != nil {
			t.Fatal(err)
		}
		encoded, err := TOMLString(value)
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Value string }
		if err := toml.Unmarshal([]byte("Value="+encoded), &got); err != nil || got.Value != value {
			t.Fatalf("round trip=%q, %v", got.Value, err)
		}
	}
	requireReason(t, Validate("fake", string([]byte{0xff, 0})), "invalid_utf8")
	requireReason(t, Validate("fake", "a\x00b"), "nul_byte")
	if _, err := TOMLString(string([]byte{0xff})); err == nil {
		t.Fatal("TOML repaired invalid UTF-8")
	}
	if err := ValidateInline("fake", strings.Repeat("x", MaxInlineBytes)); err != nil {
		t.Fatal(err)
	}
	requireReason(t, ValidateInline("fake", strings.Repeat("x", MaxInlineBytes+1)), "inline_limit")
	if Fingerprint("") != "" || Fingerprint("a") != "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" || Fingerprint(" a") == Fingerprint("a") {
		t.Fatal("content fingerprint changed")
	}
}
func TestWindowsCommandLineMapping(t *testing.T) {
	for _, command := range []string{`C:\Program Files\agent.exe`, `C:\Windows\powershell.exe`} {
		if err := ValidateCommandLine("fake", command, []string{"-File", "agent.ps1", "甲\n\"乙\"", `trailing \\`}, "windows"); err != nil {
			t.Fatal(err)
		}
	}
	// Exact native bound includes executable, separator and NUL: x + space + 32764 + NUL.
	if err := ValidateCommandLine("fake", "x", []string{strings.Repeat("a", 32764)}, "windows"); err != nil {
		t.Fatal(err)
	}
	requireReason(t, ValidateCommandLine("fake", "x", []string{strings.Repeat("a", 32765)}, "windows"), "command_line_limit")
	requireReason(t, ValidateCommandLine("fake", "x", []string{strings.Repeat("😀", 16383)}, "windows"), "command_line_limit")
	requireReason(t, ValidateCommandLine("fake", "x", []string{strings.Repeat("\\", 16382) + `"`}, "windows"), "command_line_limit")
	for _, c := range "\r\n\"%!^&|<>()" {
		requireReason(t, ValidateCommandLine("fake", "cmd.exe", []string{"/d", "/s", "/c", "call", "agent.cmd", string(c)}, "windows"), "unsafe_shell_argument")
	}
	requireReason(t, ValidateCommandLine("fake", "cmd.exe", []string{"/d", "/s", "/c", "call", "agent.cmd", strings.Repeat("a", 8191)}, "windows"), "command_line_limit")
	if err := ValidateCommandLine("fake", "cmd.exe", []string{"/d", "/s", "/c", "call", "agent.cmd", "safe text"}, "windows"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommandLine("fake", "cmd.exe", []string{"quoted\"\n"}, "linux"); err != nil {
		t.Fatal(err)
	}
}
func TestFileOwnershipAndIntegrity(t *testing.T) {
	ctx := context.Background()
	f, err := Materialize(ctx, "甲\n\"乙\"")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if filepath.Dir(filepath.Dir(f.Path())) != filepath.Clean(os.TempDir()) {
		t.Fatal("file not directly under OS temporary root")
	}
	info, _ := os.Stat(f.Path())
	dirInfo, _ := os.Stat(filepath.Dir(f.Path()))
	if runtime.GOOS != "windows" && (info.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700) {
		t.Fatalf("modes=%o/%o", info.Mode().Perm(), dirInfo.Mode().Perm())
	}
	if err := f.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path(), []byte("甲\n\"丙\""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(ctx); !errors.Is(err, os.ErrPermission) {
		t.Fatal("same-length tamper accepted", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(f.Path())); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned directory leaked", err)
	}
}
func TestFileReplacementAndCloseRetry(t *testing.T) {
	ctx := context.Background()
	f, err := Materialize(ctx, "original")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	moved := filepath.Join(filepath.Dir(f.Path()), "original-moved")
	if err := os.Rename(f.Path(), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.Path(), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(ctx); !errors.Is(err, os.ErrPermission) {
		t.Fatal("replacement accepted", err)
	}
	if err := f.Close(); !errors.Is(err, os.ErrPermission) {
		t.Fatal("replacement adopted by Close", err)
	}
	if data, err := os.ReadFile(f.Path()); err != nil || string(data) != "original" {
		t.Fatal("Close deleted replacement", err)
	}
	if err := os.Remove(f.Path()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, f.Path()); err != nil {
		t.Fatal(err)
	}
	// An unknown sibling causes an observable, retryable close failure.
	other := filepath.Join(filepath.Dir(f.Path()), "unowned")
	os.WriteFile(other, []byte("keep"), 0o600)
	if err := f.Close(); err == nil {
		t.Fatal("recursively removed unknown file")
	}
	if data, _ := os.ReadFile(other); string(data) != "keep" {
		t.Fatal("unowned file changed")
	}
	os.Remove(other)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestFileLinksAndDirectoryReplacement(t *testing.T) {
	ctx := context.Background()
	t.Run("file symlink", func(t *testing.T) {
		f, err := Materialize(ctx, "text")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		moved := filepath.Join(filepath.Dir(f.Path()), "moved")
		if err := os.Rename(f.Path(), moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, f.Path()); err != nil {
			t.Fatal(err)
		}
		if err := f.Verify(ctx); err == nil {
			t.Fatal("symlink accepted")
		}
		if err := f.Close(); err == nil {
			t.Fatal("symlink removed/adopted")
		}
		os.Remove(f.Path())
		os.Rename(moved, f.Path())
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("directory replacement", func(t *testing.T) {
		f, err := Materialize(ctx, "text")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		dir := filepath.Dir(f.Path())
		moved := dir + "-moved"
		if err := os.Rename(dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := f.Verify(ctx); err == nil {
			t.Fatal("replacement directory accepted")
		}
		if err := f.Close(); err == nil {
			t.Fatal("replacement directory deleted")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		}
		os.Remove(dir)
		os.Rename(moved, dir)
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
func TestFileModesAndCancellation(t *testing.T) {
	ctx := context.Background()
	f, err := Materialize(ctx, "text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Chmod(f.Path(), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(ctx); err == nil {
		t.Fatal("mode change accepted")
	}
	if err := os.Chmod(f.Path(), 0o600); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := f.Verify(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Materialize(canceled, "text"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var nilFile *File
	if nilFile.Path() != "" || nilFile.Fingerprint() != "" || nilFile.Verify(ctx) != nil || nilFile.Close() != nil {
		t.Fatal("nil handle is not empty")
	}
	if f, err := Materialize(ctx, ""); f != nil || err != nil {
		t.Fatal(f, err)
	}
}

type cancelAtCheck struct {
	context.Context
	cancel    context.CancelFunc
	at, count int
}

func (c *cancelAtCheck) Err() error {
	c.count++
	if c.count == c.at {
		c.cancel()
	}
	return c.Context.Err()
}
func TestFileMaterializationCancellationStages(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	for at := 1; at <= 4; at++ {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checks := &cancelAtCheck{Context: ctx, cancel: cancel, at: at}
			f, err := Materialize(checks, "private text")
			if f != nil {
				defer f.Close()
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel at stage %d = %v", at, err)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil || len(entries) != 0 {
				t.Fatalf("canceled materialization leaked %v: %v", entries, err)
			}
		})
	}
}
func TestFileDirectorySymlinkAndMode(t *testing.T) {
	ctx := context.Background()
	f, err := Materialize(ctx, "text")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dir := filepath.Dir(f.Path())
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, dir); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(ctx); err == nil {
		t.Fatal("directory symlink accepted")
	}
	if err := f.Close(); err == nil {
		t.Fatal("directory symlink adopted")
	}
	os.Remove(dir)
	os.Rename(moved, dir)
	// POSIX directory execute/read restrictions are observable, whereas Windows
	// Chmod exposes only read-only; the file test covers that observable change.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := f.Verify(ctx); err == nil {
			t.Fatal("directory mode drift accepted")
		}
		os.Chmod(dir, 0o700)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
