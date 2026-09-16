package livetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func routeFixture() map[string]any {
	return map[string]any{"version": 1, "model_provider": "review.provider", "name": "Review \"路由\"", "base_url": "https://route.invalid/v1", "wire_api": "responses", "requires_openai_auth": true}
}
func routeJSON(t *testing.T, fields map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func writeFixture(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAlignmentLiveRouteRoundTrip(t *testing.T) {
	fields := routeFixture()
	args, err := RouteArgs(routeJSON(t, fields))
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 4 || args[0] != "-c" || args[2] != "-c" {
		t.Fatal("unexpected override shape")
	}
	var parsed map[string]any
	if err := toml.Unmarshal([]byte(args[1]+"\n"+args[3]), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model_provider"] != fields["model_provider"] {
		t.Fatal("provider identity changed")
	}
	providers := parsed["model_providers"].(map[string]any)
	want := map[string]any{"name": fields["name"], "base_url": fields["base_url"], "wire_api": "responses", "requires_openai_auth": true}
	if len(providers) != 1 || !reflect.DeepEqual(providers["review.provider"], want) {
		t.Fatal("projection changed selected provider")
	}
	again, err := RouteArgs(routeJSON(t, fields))
	if err != nil || !reflect.DeepEqual(args, again) {
		t.Fatal("nondeterministic routing")
	}
	fields["model_provider"] = "openai"
	fields["name"] = "OpenAI"
	args, err = RouteArgs(routeJSON(t, fields))
	if err != nil || !strings.HasPrefix(args[3], "openai_base_url=") {
		t.Fatal("explicit built-in endpoint missing")
	}
}
func TestAlignmentLiveRouteRejectsAmbiguity(t *testing.T) {
	cases := map[string]func(map[string]any){
		"unknown":          func(m map[string]any) { m["env_key"] = "secret-field" },
		"missing":          func(m map[string]any) { delete(m, "model_provider") },
		"version":          func(m map[string]any) { m["version"] = 2 },
		"null-auth":        func(m map[string]any) { m["requires_openai_auth"] = nil },
		"other-auth":       func(m map[string]any) { m["requires_openai_auth"] = false },
		"wire":             func(m map[string]any) { m["wire_api"] = "chat" },
		"empty":            func(m map[string]any) { m["model_provider"] = "" },
		"control":          func(m map[string]any) { m["name"] = "bad\nname" },
		"space":            func(m map[string]any) { m["model_provider"] = " trailing" },
		"remote-http":      func(m map[string]any) { m["base_url"] = "http://secret.invalid/v1" },
		"userinfo":         func(m map[string]any) { m["base_url"] = "https://secret:password@route.invalid/v1" },
		"query":            func(m map[string]any) { m["base_url"] = "https://route.invalid/v1?key=secret" },
		"fragment":         func(m map[string]any) { m["base_url"] = "https://route.invalid/v1#secret" },
		"type":             func(m map[string]any) { m["name"] = []string{"secret"} },
		"builtin-conflict": func(m map[string]any) { m["model_provider"] = "openai" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			m := routeFixture()
			change(m)
			if _, err := RouteArgs(routeJSON(t, m)); err == nil || err.Error() != "invalid or unsupported codex live route" {
				t.Fatal("invalid route accepted or diagnostic leaked input")
			}
		})
	}
	for name, input := range map[string][]byte{"duplicate": []byte(`{"version":1,"version":1}`), "trailing": append(routeJSON(t, routeFixture()), '0'), "large": []byte(strings.Repeat("x", maxSeedBytes+1)), "null": []byte("null")} {
		t.Run(name, func(t *testing.T) {
			if _, err := RouteArgs(input); err == nil {
				t.Fatal("invalid route accepted")
			}
		})
	}
	for _, endpoint := range []string{"http://127.0.0.1:1234/v1", "http://[::1]:1234/v1"} {
		m := routeFixture()
		m["base_url"] = endpoint
		if _, err := RouteArgs(routeJSON(t, m)); err != nil {
			t.Fatal("loopback fixture rejected")
		}
	}
}
func TestAlignmentLiveProfileMinimalAndFailClosed(t *testing.T) {
	seed := t.TempDir()
	route := filepath.Join(t.TempDir(), "route.json")
	writeFixture(t, route, routeJSON(t, routeFixture()))
	writeFixture(t, filepath.Join(seed, "auth.json"), []byte(`{"OPENAI_API_KEY":"dummy-key"}`))
	writeFixture(t, filepath.Join(seed, "config.toml"), []byte("must never be copied"))
	p, err := Prepare(seed, route, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, ".git")); err != nil {
		t.Fatal("isolated workspace is not a repository")
	}
	files, err := os.ReadDir(p.Directory)
	if err != nil || len(files) != 1 || files[0].Name() != "auth.json" {
		t.Fatal("copied unrelated profile state")
	}
	auth, err := os.ReadFile(filepath.Join(p.Directory, "auth.json"))
	if err != nil || string(auth) != `{"OPENAI_API_KEY":"dummy-key"}` {
		t.Fatal("auth projection changed")
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{p.Directory: 0700, p.Workspace: 0700, filepath.Join(p.Directory, "auth.json"): 0600} {
			f, err := os.Stat(path)
			if err != nil || f.Mode().Perm() != mode {
				t.Fatal("private permissions lost")
			}
		}
	}
	for _, auth := range []string{`{"OPENAI_API_KEY":"dummy-key","tokens":{}}`, `{"OPENAI_API_KEY":"a","OPENAI_API_KEY":"b"}`, `{"OPENAI_API_KEY":null}`, `{}`} {
		writeFixture(t, filepath.Join(seed, "auth.json"), []byte(auth))
		if _, err := Prepare(seed, route, t.TempDir()); err == nil || strings.Contains(err.Error(), "dummy-key") {
			t.Fatal("invalid auth accepted or leaked")
		}
	}
	home := t.TempDir()
	if _, err := Prepare(filepath.Join(seed, "absent"), "", home); err == nil || err.Error() != "codex live route file unavailable" {
		t.Fatal("routing did not fail before auth")
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatal("invalid input materialized resources")
	}
	if _, err := Prepare("", route, t.TempDir()); err == nil {
		t.Fatal("implicit auth source accepted")
	}
}

func TestAlignmentLiveProfileGitIsolation(t *testing.T) {
	seed := t.TempDir()
	route := filepath.Join(t.TempDir(), "route.json")
	writeFixture(t, route, routeJSON(t, routeFixture()))
	writeFixture(t, filepath.Join(seed, "auth.json"), []byte(`{"OPENAI_API_KEY":"dummy"}`))
	outside := t.TempDir()
	writeFixture(t, filepath.Join(outside, "sentinel"), []byte("unchanged"))
	for name, value := range map[string]string{"GIT_DIR": filepath.Join(outside, "repo"), "GIT_WORK_TREE": outside, "GIT_TEMPLATE_DIR": outside, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "init.templateDir", "GIT_CONFIG_VALUE_0": outside} {
		t.Setenv(name, value)
	}
	p, err := Prepare(seed, route, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 1 || files[0].Name() != "sentinel" {
		t.Fatal("Git wrote outside the private workspace")
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, ".git", "sentinel")); !os.IsNotExist(err) {
		t.Fatal("Git inherited a template")
	}
	if _, err := os.Stat(filepath.Join(p.Workspace, ".git", "HEAD")); err != nil {
		t.Fatal("Git repository was not initialized privately")
	}
}
