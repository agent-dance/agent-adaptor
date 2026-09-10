package adaptor

import (
	"os"
	"path/filepath"
	"testing"
)

// Provider configuration is structured, but a skill may intentionally include
// malformed examples or JSON values that are not configuration objects.
func TestHostedProfileSkillAssetsKeepExactBytes(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "codebuddy", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			for _, asset := range []struct{ name, before, after string }{
				{"array.json", `[1,2,3]`, `[1, 2, 3]`},
				{"scalar.json", `"example"`, ` "example"`},
				{"object.json", `{"a":1}`, `{ "a": 1 }`},
				{"invalid.json", `{invalid`, `{different invalid`},
				{"example.toml", "value = 1\n", "value = 1 # example\n"},
				{"invalid.toml", "key = [", "key = {"},
			} {
				t.Run(asset.name, func(t *testing.T) {
					dir := t.TempDir()
					path := filepath.Join(dir, "skills", "example", "assets", asset.name)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					write := func(text string) string {
						t.Helper()
						if err := os.WriteFile(path, []byte(text), 0600); err != nil {
							t.Fatal(err)
						}
						fp, err := hostedToolMaterializedProfileFingerprint(provider, dir)
						if err != nil {
							t.Fatalf("opaque asset rejected: %v", err)
						}
						return fp
					}
					before := write(asset.before)
					if again := write(asset.before); again != before {
						t.Fatal("unchanged asset has an unstable fingerprint")
					}
					if after := write(asset.after); after == before {
						t.Fatal("changed resource bytes did not change compatibility")
					}
				})
			}
		})
	}
}

func TestHostedProfileConfigurationRemainsStructured(t *testing.T) {
	for _, tc := range []struct{ provider, path, before, equivalent, invalid string }{
		{"claude", "settings.json", `{"a":1,"b":2}`, `{"b":2, "a":1}`, `[1,2,3]`},
		{"codex", "config.toml", "a = 1\nb = 2\n", "b = 2\na = 1 # config\n", "a = ["},
		{"codebuddy", "settings.json", `{"a":1,"b":2}`, `{"b":2, "a":1}`, `{"a":1,"a":2}`},
		{"cursor", "settings.json", `{"a":1,"b":2}`, `{"b":2, "a":1}`, `{"a":`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			dir := t.TempDir()
			write := func(text string) (string, error) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, tc.path), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				return hostedToolMaterializedProfileFingerprint(tc.provider, dir)
			}
			before, err := write(tc.before)
			if err != nil {
				t.Fatal(err)
			}
			if after, err := write(tc.equivalent); err != nil || before != after {
				t.Fatalf("configuration normalization changed: %v", err)
			}
			if _, err := write(tc.invalid); err == nil {
				t.Fatal("invalid provider configuration was accepted")
			}
		})
	}
}
