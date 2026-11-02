package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
)

func guardProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := seedProject(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestArtifactAllowlistRejectsTraversalAndInvalidNames(t *testing.T) {
	root := guardProject(t)
	for _, name := range []string{
		"", "../router.go", "../../outside", "./router.go", "sub/../router.go",
		"sub/router.go", "router.go/other", "..\\router.go", "C:\\router.go",
		filepath.Join(root, "router.go"), string([]byte{'r', 255}), "AUTH.json",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := safeRead(root, name); err == nil {
				t.Fatalf("untrusted artifact path %q was accepted", name)
			}
		})
	}
	if data, err := safeRead(root, "router.go"); err != nil || string(data) != fixtureStarter {
		t.Fatalf("ordinary deliverable unreadable: %v", err)
	}
}

func TestProjectManifestRequiresCompleteBoundedUTF8Files(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(string) error
	}{
		{"extra file", func(root string) error { return os.WriteFile(filepath.Join(root, "notes.txt"), []byte("extra"), 0600) }},
		{"extra directory", func(root string) error { return os.Mkdir(filepath.Join(root, "extra"), 0700) }},
		{"missing file", func(root string) error { return os.Remove(filepath.Join(root, "go.mod")) }},
		{"invalid utf8", func(root string) error { return os.WriteFile(filepath.Join(root, "router.go"), []byte{255}, 0600) }},
		{"oversize", func(root string) error {
			return os.WriteFile(filepath.Join(root, "router.go"), bytes.Repeat([]byte{'x'}, (256<<10)+1), 0600)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := guardProject(t)
			if err := tt.mutate(root); err != nil {
				t.Fatal(err)
			}
			if _, err := projectManifest(root); err == nil {
				t.Fatal("invalid project accepted")
			}
		})
	}
}

func TestProjectRejectsSymlinks(t *testing.T) {
	root := guardProject(t)
	external := filepath.Join(t.TempDir(), "external.go")
	if err := os.WriteFile(external, []byte(fixtureStarter), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "router.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "router.go")); err != nil {
		t.Skipf("symlink creation unavailable on this host: %v", err)
	}
	if _, err := safeRead(root, "router.go"); err == nil {
		t.Fatal("symlink target was read as a regular artifact")
	}
	if _, err := projectManifest(root); err == nil {
		t.Fatal("symlink accepted in project manifest")
	}
	linkRoot := filepath.Join(t.TempDir(), "linked-project")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := projectManifest(linkRoot); err == nil {
		t.Fatal("symlink project root accepted")
	}
}

func TestCandidateManifestDetectsDriftAndForgery(t *testing.T) {
	root := guardProject(t)
	manifest, err := projectManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	forged := append([]artifact(nil), manifest...)
	forged[0].SHA256 = strings.Repeat("0", 64)
	if err := verifyManifest(root, forged); err == nil {
		t.Fatal("model-supplied forged hash accepted")
	}
	if err := verifyManifest(root, manifest[:len(manifest)-1]); err == nil {
		t.Fatal("incomplete model manifest accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "router.go"), []byte(fixtureStarter+"\n// changed candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyManifest(root, manifest); err == nil {
		t.Fatal("old candidate manifest accepted after byte drift")
	}
	if err := copyCandidate(root, filepath.Join(t.TempDir(), "snapshot"), manifest); err == nil {
		t.Fatal("drifted source copied under its previous identity")
	}
}

func TestRouterGuardRejectsTestEvasion(t *testing.T) {
	root := guardProject(t)
	if err := validateRouter(root); err != nil {
		t.Fatalf("starter rejected before implementation: %v", err)
	}
	for _, tt := range []struct{ name, source string }{
		{"os import", "package mentionroute\nimport \"os\"\nfunc quit() { os.Exit(0) }\n"},
		{"init", "package mentionroute\nfunc init() {}\n"},
		{"init time call", "package mentionroute\nvar value = build()\nfunc build() int { return 1 }\n"},
		{"nested init time call", "package mentionroute\nvar values = []int{build()}\nfunc build() int { return 1 }\n"},
		{"go directive", "package mentionroute\n//go:linkname hidden runtime.exit\nfunc hidden()\n"},
		{"line directive", "package mentionroute\n//line fake.go:1\nfunc hidden() {}\n"},
		{"test main", "package mentionroute\nfunc TestMain() {}\n"},
		{"wrong package", "package main\nfunc main() {}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "router.go"), []byte(tt.source), 0600); err != nil {
				t.Fatal(err)
			}
			if err := validateRouter(root); err == nil {
				t.Fatal("test-evasion source accepted")
			}
		})
	}
}

// This list is intentionally fixed by the task, independent of requiredTests.
// Removing a mandatory test from the production guard must not make this green.
var guardAcceptanceNames = []string{
	"TestAcceptanceR1LiteralUTF8", "TestAcceptanceR2BoundariesAndLongest",
	"TestAcceptanceR3DeduplicationOrder", "TestAcceptanceR4InlineCode",
	"TestAcceptanceR5FencedCode", "TestAcceptanceR6QuotesEmailEscapes",
	"TestAcceptanceR7HumanRouting", "TestAcceptanceR8AgentAuthority",
	"TestAcceptanceConcurrentIndependentCalls",
}

func guardCheckLog(names []string, includeRuns, packagePass bool) []byte {
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for _, name := range names {
		if includeRuns {
			_ = enc.Encode(map[string]string{"Action": "run", "Package": "example.test/mentionroute", "Test": name})
		}
		_ = enc.Encode(map[string]string{"Action": "pass", "Package": "example.test/mentionroute", "Test": name})
	}
	if packagePass {
		_ = enc.Encode(map[string]string{"Action": "pass", "Package": "example.test/mentionroute"})
	}
	return log.Bytes()
}

func TestAcceptanceEvidenceNeedsEveryActualTestLifecycle(t *testing.T) {
	complete := guardCheckLog(guardAcceptanceNames, true, true)
	if err := acceptanceCompleted(complete); err != nil {
		t.Fatalf("complete real event shape rejected: %v", err)
	}
	for i, missing := range guardAcceptanceNames {
		t.Run("missing "+missing, func(t *testing.T) {
			names := append(append([]string(nil), guardAcceptanceNames[:i]...), guardAcceptanceNames[i+1:]...)
			if err := acceptanceCompleted(guardCheckLog(names, true, true)); err == nil {
				t.Fatalf("acceptance omitted %s but succeeded", missing)
			}
		})
	}
	wrapped, err := json.Marshal(map[string]string{"Action": "output", "Package": "example.test/mentionroute", "Output": string(complete)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		log  []byte
	}{
		{"exit zero with no output", nil},
		{"plain model success", []byte("exit code 0\nPASS\nok example.test/mentionroute\n")},
		{"passes without starts", guardCheckLog(guardAcceptanceNames, false, true)},
		{"no package terminal", guardCheckLog(guardAcceptanceNames, true, false)},
		{"wrong package", bytes.ReplaceAll(complete, []byte("example.test/mentionroute"), []byte("forged.test/mentionroute"))},
		{"forged ordinary output", wrapped},
		{"failure before success", append([]byte("{\"Action\":\"fail\",\"Package\":\"example.test/mentionroute\"}\n"), complete...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := acceptanceCompleted(tt.log); err == nil {
				t.Fatal("unproven acceptance succeeded")
			}
		})
	}
}

func TestEvidenceRedactionPreservesUsageAccounting(t *testing.T) {
	t.Setenv("SDLC_FIXTURE_TOKEN", "sdlc-fixture-private-value")
	path := filepath.Join(t.TempDir(), "evidence.json")
	value := map[string]any{
		"usage":        adaptor.Usage{InputTokens: 123, OutputTokens: 45, CachedInputTokens: 6, EstimatedCostMilli: 7},
		"access_token": "direct-private-value",
		"nested":       []any{map[string]any{"authorization": "private-header", "auth": map[string]string{"key": "private-object"}}},
		"log":          "sdlc-fixture-private-value Bearer fixture.bearer.value and \"refresh_token\":\"nested-private-value\"",
	}
	if err := writeJSON(path, value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sdlc-fixture-private-value", "direct-private-value", "private-header", "private-object", "fixture.bearer.value", "nested-private-value"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatalf("secret %q survived serialization", secret)
		}
	}
	var decoded struct {
		Usage       adaptor.Usage `json:"usage"`
		AccessToken string        `json:"access_token"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	want := adaptor.Usage{InputTokens: 123, OutputTokens: 45, CachedInputTokens: 6, EstimatedCostMilli: 7}
	if !reflect.DeepEqual(decoded.Usage, want) {
		t.Fatalf("accounting damaged by redaction: %+v; want %+v", decoded.Usage, want)
	}
	if decoded.AccessToken != "[REDACTED]" {
		t.Fatalf("sensitive JSON field was not replaced: %q", decoded.AccessToken)
	}
}
