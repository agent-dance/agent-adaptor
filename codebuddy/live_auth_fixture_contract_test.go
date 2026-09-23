package codebuddy

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func codeBuddyFixtureSeedDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func codeBuddySyntheticSeed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(codeBuddyFixtureSeedDir(t), "snapshot.info")
	if err := os.WriteFile(path, []byte(codeBuddySyntheticNativeSession), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodeBuddyNativeAuthFixturePaths(t *testing.T) {
	for platform, want := range map[string]string{
		"darwin":  "Library/Application Support/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info",
		"windows": "AppData/Local/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info",
		"linux":   ".local/share/CodeBuddyExtension/Data/Public/auth/Tencent-Cloud.coding-copilot.info",
	} {
		got, err := codeBuddyNativeAuthPath(platform)
		if err != nil || filepath.ToSlash(got) != want {
			t.Fatalf("%s path mismatch", platform)
		}
	}
	if _, err := codeBuddyNativeAuthPath("unknown"); err == nil {
		t.Fatal("unknown platform silently accepted")
	}
}

func TestCodeBuddyNativeAuthFixtureBytesIsolationAndCleanup(t *testing.T) {
	seed := codeBuddySyntheticSeed(t)
	for _, name := range []string{"settings.json", "mcp.json", "credentials.json", "other.info"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(seed), name), []byte(`{"business":"must not copy"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var home string
	t.Run("owned lifetime", func(t *testing.T) {
		f := newCodeBuddyLiveHome(t)
		home = f.home
		if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: seed, configDir: filepath.Dir(seed)}); err != nil {
			t.Fatal(err)
		}
		relative, err := codeBuddyNativeAuthPath(runtime.GOOS)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, relative)
		data, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(data, []byte(codeBuddySyntheticNativeSession)) {
			t.Fatal("opaque native bytes changed")
		}
		sourceInfo, _ := os.Stat(seed)
		targetInfo, _ := os.Stat(target)
		if os.SameFile(sourceInfo, targetInfo) {
			t.Fatal("credential copy aliases source")
		}
		if entries, err := os.ReadDir(f.profile); err != nil || len(entries) != 0 {
			t.Fatalf("native profile isolation: read_error=%v entry_count=%d want=0", err, len(entries))
		}
		if entries, err := os.ReadDir(filepath.Dir(target)); err != nil || len(entries) != 1 {
			t.Fatalf("native auth isolation: read_error=%v entry_count=%d want=1", err, len(entries))
		}
		bindings := map[string]string{}
		for _, binding := range f.bindings() {
			bindings[binding.Name] = binding.Value
		}
		if bindings["HOME"] != home || bindings["USERPROFILE"] != home || bindings["CODEBUDDY_CONFIG_DIR"] != f.profile || bindings["CODEBUDDY_API_KEY_DISABLED"] != "1" {
			t.Fatal("wrong native environment")
		}
		for _, name := range []string{"APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "TMPDIR", "TMP", "TEMP"} {
			relative, err := filepath.Rel(home, bindings[name])
			if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
				t.Fatalf("%s escapes private HOME", name)
			}
		}
		// A first Agent may close while a cold successor still needs this same HOME.
		// Only the fixture cleanup registered before both agents removes it.
		if err := verifyCodeBuddyPrivateObject(f.directory, true); err != nil {
			t.Fatal(err)
		}
		for _, directory := range f.directories {
			if err := verifyCodeBuddyPrivateObject(directory, true); err != nil {
				t.Fatal(err)
			}
		}
		opened, err := os.Open(target)
		if err != nil {
			t.Fatal(err)
		}
		err = verifyCodeBuddyPrivateObject(opened, false)
		closeErr := opened.Close()
		if err != nil || closeErr != nil {
			t.Fatal("seed file permissions are not private")
		}
		// A native refresh must be able to replace the seed after initial copying.
		replacement := target + ".refresh"
		if err := os.WriteFile(replacement, []byte(`{"auth":{"refreshed":true}}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatalf("native refresh blocked: %v", err)
		}
	})
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private HOME survived cleanup")
	}
	data, err := os.ReadFile(seed)
	if err != nil || string(data) != codeBuddySyntheticNativeSession {
		t.Fatal("source native session changed")
	}
}

func TestCodeBuddyNativeAuthFixtureModeSelection(t *testing.T) {
	get := func(values map[string]string) func(string) string { return func(k string) string { return values[k] } }
	if _, err := codeBuddyLiveSeedFromEnv(get(nil)); err == nil {
		t.Fatal("missing all explicit auth choices accepted")
	}
	for _, key := range []string{"CODEBUDDY_API_KEY", "CODEBUDDY_AUTH_TOKEN"} {
		seed, err := codeBuddyLiveSeedFromEnv(get(map[string]string{key: "synthetic-token"}))
		if err != nil || !seed.environment || seed.nativeFile != "" {
			t.Fatal("existing environment token mode changed")
		}
		f := newCodeBuddyLiveHome(t)
		if err := f.seed(seed); err != nil {
			t.Fatal(err)
		}
		for _, binding := range f.bindings() {
			if binding.Name == key || binding.Name == "CODEBUDDY_API_KEY_DISABLED" {
				t.Fatal("environment token mode rewritten")
			}
		}
	}
	for _, key := range codeBuddyNativeAuthConflicts {
		values := map[string]string{"CODEBUDDY_NATIVE_AUTH_FILE_SOURCE": "/explicit/synthetic.info", key: "synthetic-conflict"}
		if _, err := codeBuddyLiveSeedFromEnv(get(values)); err == nil || !strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "synthetic-conflict") {
			t.Fatal("native conflicting choice not rejected safely")
		}
		values[key] = ""
		if _, err := codeBuddyLiveSeedFromEnv(get(values)); err != nil {
			t.Fatal("empty environment variable changed native mode")
		}
	}
	for _, value := range []string{"", "1"} {
		if _, err := codeBuddyLiveSeedFromEnv(get(map[string]string{"CODEBUDDY_NATIVE_AUTH_FILE_SOURCE": "/explicit/synthetic.info", "CODEBUDDY_API_KEY_DISABLED": value})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := codeBuddyLiveSeedFromEnv(get(map[string]string{"CODEBUDDY_NATIVE_AUTH_FILE_SOURCE": "/explicit/synthetic.info", "CODEBUDDY_CREDENTIALS_IN_MEMORY": "1"})); err == nil {
		t.Fatal("in-memory mode hid file storage")
	}
}

func TestCodeBuddyNativeAuthFixtureRejectsInvalidSeeds(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"empty", ""}, {"malformed", "{not JSON"}, {"null", "null"}, {"array", "[]"}, {"empty_object", "{}"}, {"oversized", strings.Repeat(" ", 8<<20) + `{"auth":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(codeBuddyFixtureSeedDir(t), "invalid.info")
			if err := os.WriteFile(source, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			f := newCodeBuddyLiveHome(t)
			if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: source, environment: true}); err == nil {
				t.Fatal("invalid explicit native seed fell back to environment")
			}
			relative, _ := codeBuddyNativeAuthPath(runtime.GOOS)
			if _, err := os.Stat(filepath.Join(f.home, relative)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid seed partially published")
			}
		})
	}
	for _, source := range []string{"relative.info", filepath.Join(codeBuddyFixtureSeedDir(t), "absent.info"), codeBuddyFixtureSeedDir(t)} {
		if _, err := readCodeBuddyLiveSeed(source, true); err == nil {
			t.Fatal("unsafe/missing source accepted")
		}
	}
	source := codeBuddySyntheticSeed(t)
	link := filepath.Join(codeBuddyFixtureSeedDir(t), "link.info")
	if err := os.Symlink(source, link); err == nil {
		if _, err := readCodeBuddyLiveSeed(link, true); err == nil {
			t.Fatal("symlink source accepted")
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
}

func TestCodeBuddyNativeAuthFixtureLegacyMissingAndIO(t *testing.T) {
	empty := codeBuddyFixtureSeedDir(t)
	f := newCodeBuddyLiveHome(t)
	if err := f.seed(codeBuddyLiveAuthSeed{configDir: empty}); err == nil {
		t.Fatal("missing legacy files silently succeeded")
	}
	if err := f.seed(codeBuddyLiveAuthSeed{configDir: empty, environment: true}); err != nil {
		t.Fatal("explicit env mode with empty legacy source rejected")
	}
	source := codeBuddyFixtureSeedDir(t)
	if err := os.Mkdir(filepath.Join(source, ".credentials.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := f.seed(codeBuddyLiveAuthSeed{configDir: source, environment: true}); err == nil {
		t.Fatal("non-ENOENT legacy failure swallowed")
	}
	source = codeBuddyFixtureSeedDir(t)
	raw := []byte("{ \"syntheticLogin\":true }\n")
	if err := os.WriteFile(filepath.Join(source, "credentials.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "mcp.json"), []byte(`{"private":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.seed(codeBuddyLiveAuthSeed{configDir: source}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.profile, "credentials.json"))
	if err != nil || !bytes.Equal(data, raw) {
		t.Fatal("legacy seed bytes changed")
	}
	entries, err := os.ReadDir(f.profile)
	if err != nil || len(entries) != 1 {
		t.Fatalf("legacy profile isolation: read_error=%v entry_count=%d want=1", err, len(entries))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal("cleanup not idempotent")
	}
}

func TestCodeBuddyNativeAuthFixtureNoReplace(t *testing.T) {
	f := newCodeBuddyLiveHome(t)
	seed := codeBuddySyntheticSeed(t)
	if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: seed}); err != nil {
		t.Fatal(err)
	}
	if err := f.seed(codeBuddyLiveAuthSeed{nativeFile: seed}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing native target replaced: %v", err)
	}
}

func TestCodeBuddyNativeAuthFixtureDisabledAPIKey(t *testing.T) {
	source := codeBuddySyntheticSeed(t)
	for _, disabled := range []string{"1", "0", "false", " "} {
		values := map[string]string{"CODEBUDDY_API_KEY": "synthetic-inactive-key", "CODEBUDDY_API_KEY_DISABLED": disabled}
		getenv := func(key string) string { return values[key] }
		if _, err := codeBuddyLiveSeedFromEnv(getenv); err == nil {
			t.Fatal("disabled API key counted as authentication")
		}
		values["CODEBUDDY_NATIVE_AUTH_FILE_SOURCE"] = source
		seed, err := codeBuddyLiveSeedFromEnv(getenv)
		if err != nil || seed.environment {
			t.Fatal("inactive key conflicted with native session")
		}
		f := newCodeBuddyLiveHome(t)
		if err := f.seed(seed); err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, binding := range f.bindings() {
			if binding.Name == "CODEBUDDY_API_KEY_DISABLED" {
				seen = binding.Value == disabled
			}
		}
		if !seen {
			t.Fatal("native fixture rewrote nonempty disabled value")
		}
	}
	values := map[string]string{"CODEBUDDY_NATIVE_AUTH_FILE_SOURCE": source, "CODEBUDDY_API_KEY": "synthetic-active-key"}
	if _, err := codeBuddyLiveSeedFromEnv(func(key string) string { return values[key] }); err == nil {
		t.Fatal("active API key accepted with native session")
	}
}

func TestCodeBuddyNativeAuthFixtureRejectsParentLinksAndLogout(t *testing.T) {
	source := codeBuddySyntheticSeed(t)
	parent := codeBuddyFixtureSeedDir(t)
	link := filepath.Join(parent, "linked-parent")
	if err := os.Symlink(filepath.Dir(source), link); err != nil {
		t.Fatal("cannot inject required parent-link counterexample", err)
	}
	if _, err := readCodeBuddyLiveSeed(filepath.Join(link, filepath.Base(source)), true); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("linked parent accepted: %v", err)
	}
	marker := source + ".logged-out"
	for _, kind := range []string{"file", "directory", "dangling-link"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "file":
				if err := os.WriteFile(marker, []byte("synthetic logout"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(marker, 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling-link":
				if err := os.Symlink(filepath.Join(parent, "absent"), marker); err != nil {
					t.Fatal("cannot inject required logout-link counterexample", err)
				}
			}
			defer os.Remove(marker)
			if _, err := readCodeBuddyLiveSeed(source, true); err == nil || !strings.Contains(err.Error(), "logout marker") {
				t.Fatal("logout marker accepted")
			}
		})
	}
}
