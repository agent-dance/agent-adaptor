package clihelper

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareCommandWrapsBatchShimOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific shim behavior")
	}

	tempDir := t.TempDir()
	shimPath := filepath.Join(tempDir, "mockshim.cmd")
	if err := os.WriteFile(shimPath, []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatalf("write test shim: %v", err)
	}

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+originalPath)

	command, args, err := prepareCommand("mockshim", []string{"arg-one", "arg-two"})
	if err != nil {
		t.Fatalf("prepareCommand returned error: %v", err)
	}
	if command != "cmd.exe" {
		t.Fatalf("expected command cmd.exe, got %q", command)
	}
	if len(args) < 4 {
		t.Fatalf("expected wrapped cmd args, got %#v", args)
	}
	if args[0] != "/d" || args[1] != "/s" || args[2] != "/c" {
		t.Fatalf("expected cmd wrapper flags, got %#v", args[:3])
	}
	if !filepath.IsAbs(args[3]) || filepath.Ext(args[3]) != ".cmd" {
		t.Fatalf("expected absolute .cmd path, got %q", args[3])
	}
	if args[4] != "arg-one" || args[5] != "arg-two" {
		t.Fatalf("expected original command args to be preserved, got %#v", args[4:])
	}
}

func TestPrepareCommandWrapsPowerShellScriptOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific shim behavior")
	}

	tempDir := t.TempDir()
	scriptPath := filepath.Join(tempDir, "mockshim.ps1")
	if err := os.WriteFile(scriptPath, []byte("Write-Output 'ok'\n"), 0o644); err != nil {
		t.Fatalf("write test script: %v", err)
	}

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+originalPath)

	command, args, err := prepareCommand(scriptPath, []string{"arg-one"})
	if err != nil {
		t.Fatalf("prepareCommand returned error: %v", err)
	}
	if filepath.Base(strings.ToLower(command)) != "pwsh.exe" && filepath.Base(strings.ToLower(command)) != "powershell.exe" &&
		filepath.Base(strings.ToLower(command)) != "pwsh" && filepath.Base(strings.ToLower(command)) != "powershell" {
		t.Fatalf("expected powershell wrapper, got %q", command)
	}
	if len(args) < 5 {
		t.Fatalf("expected powershell wrapper args, got %#v", args)
	}
	if args[0] != "-NoLogo" || args[1] != "-NoProfile" || args[2] != "-File" {
		t.Fatalf("expected powershell wrapper flags, got %#v", args[:3])
	}
	if !filepath.IsAbs(args[3]) || filepath.Ext(args[3]) != ".ps1" {
		t.Fatalf("expected absolute .ps1 path, got %q", args[3])
	}
	if args[4] != "arg-one" {
		t.Fatalf("expected original script args to be preserved, got %#v", args[4:])
	}
}

func TestMergeEnvSynthesizesWindowsRuntimeVariables(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific environment behavior")
	}

	t.Setenv("SystemRoot", "")
	t.Setenv("SYSTEMROOT", "")
	t.Setenv("windir", "")
	t.Setenv("WINDIR", "")
	t.Setenv("ComSpec", "")
	t.Setenv("COMSPEC", "")

	env := mergeEnv(nil)
	got := map[string]string{}
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		got[parts[0]] = parts[1]
	}

	if value := lookupEnvValue(got, "SystemRoot"); value != `C:\Windows` {
		t.Fatalf("expected SystemRoot to be synthesized, got %q", value)
	}
	if value := lookupEnvValue(got, "windir"); value != `C:\Windows` {
		t.Fatalf("expected windir to be synthesized, got %q", value)
	}
	if value := lookupEnvValue(got, "ComSpec"); !strings.EqualFold(value, `C:\Windows\System32\cmd.exe`) {
		t.Fatalf("expected ComSpec to point to cmd.exe, got %q", value)
	}
}

func lookupEnvValue(env map[string]string, name string) string {
	for key, value := range env {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
