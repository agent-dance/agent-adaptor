package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/agent-dance/agent-adaptor/tool"
)

func TestAlignmentProfileSkillAssetsWithTools(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"object.json", `{"examples":[1,2,3]}`},
		{"array.json", `[1,2,3]`},
		{"malformed.json", `{deliberately invalid example`},
		{"malformed.toml", "example = ["},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := alignmentSource(t)
			assets := filepath.Join(source, "skills", "example", "assets")
			if err := os.MkdirAll(assets, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "skills", "example", "SKILL.md"), []byte("---\nname: example\ndescription: Read the example data\n---\nUse the assets as example input data."), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(assets, tc.name), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			d := newAlignmentProfileDriver(source)
			a := adaptor.New(d, adaptor.WithProfile(profile.Dedicated(source)), adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("v1"))))
			t.Cleanup(func() {
				if err := a.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			if _, err := a.Run(context.Background(), "read skill"); err != nil {
				t.Fatalf("skill asset rejected before execution: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(d.request(t, 0).Profile.Dir, "skills", "example", "assets", tc.name))
			if err != nil || string(raw) != tc.data {
				t.Fatalf("provider-visible asset changed: data=%q err=%v", raw, err)
			}
		})
	}
}
