package exampleutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func Fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}

func Must(err error, format string, args ...any) {
	if err == nil {
		return
	}
	message := format
	if strings.TrimSpace(message) == "" {
		message = "unexpected error"
	}
	allArgs := append([]any{}, args...)
	allArgs = append(allArgs, err)
	Fatalf(message+": %v", allArgs...)
}

func Check(condition bool, format string, args ...any) {
	if condition {
		return
	}
	Fatalf(format, args...)
}

func PrintJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		Fatalf("encode JSON output: %v", err)
	}
}

func EnsureWindowsProcessEnv(base []string) []string {
	if runtime.GOOS != "windows" {
		return base
	}

	env := map[string]string{}
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		env[parts[0]] = parts[1]
	}

	systemRoot := firstEnvValue(env, "SystemRoot", "SYSTEMROOT", "windir", "WINDIR")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	systemRoot = filepath.Clean(systemRoot)

	setEnvValue(env, "SystemRoot", systemRoot)
	setEnvValue(env, "windir", systemRoot)

	comSpec := firstEnvValue(env, "ComSpec", "COMSPEC")
	if comSpec == "" {
		comSpec = filepath.Join(systemRoot, "System32", "cmd.exe")
	}
	setEnvValue(env, "ComSpec", filepath.Clean(comSpec))

	if localAppData := firstEnvValue(env, "LocalAppData", "LOCALAPPDATA"); localAppData == "" {
		if userProfile := firstEnvValue(env, "USERPROFILE"); userProfile != "" {
			setEnvValue(env, "LocalAppData", filepath.Join(userProfile, "AppData", "Local"))
		}
	}
	if appData := firstEnvValue(env, "APPDATA"); appData == "" {
		if userProfile := firstEnvValue(env, "USERPROFILE"); userProfile != "" {
			setEnvValue(env, "APPDATA", filepath.Join(userProfile, "AppData", "Roaming"))
		}
	}
	if tempDir := firstEnvValue(env, "TEMP", "TMP"); tempDir == "" {
		if localAppData := firstEnvValue(env, "LocalAppData", "LOCALAPPDATA"); localAppData != "" {
			tempPath := filepath.Join(localAppData, "Temp")
			setEnvValue(env, "TEMP", tempPath)
			setEnvValue(env, "TMP", tempPath)
		}
	}

	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func WrapCommandForPlatform(command string, args []string) (string, []string) {
	if runtime.GOOS != "windows" {
		return command, args
	}

	switch strings.ToLower(filepath.Ext(command)) {
	case ".ps1":
		powerShellPath, err := exec.LookPath("pwsh.exe")
		if err != nil {
			powerShellPath, err = exec.LookPath("powershell.exe")
			Must(err, "resolve PowerShell for %q", command)
		}
		return powerShellPath, append([]string{"-NoLogo", "-NoProfile", "-File", command}, args...)
	case ".cmd", ".bat":
		return "cmd.exe", append([]string{"/d", "/s", "/c", command}, args...)
	default:
		return command, args
	}
}

func dedupePaths(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(trimmed))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func firstEnvValue(env map[string]string, names ...string) string {
	for _, name := range names {
		for key, value := range env {
			if strings.EqualFold(key, name) && strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}

func setEnvValue(env map[string]string, name, value string) {
	for key := range env {
		if strings.EqualFold(key, name) {
			env[key] = value
			return
		}
	}
	env[name] = value
}
