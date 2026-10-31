package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestSavedPermissionsFailBeforePrompt(t *testing.T) {
	grants, _ := json.Marshal([]map[string]string{{"data": `[{"permission":"*","pattern":"*","action":"allow"}]`}})
	for _, tc := range []struct {
		name, initial, after string
		unsupported          bool
	}{
		{"saved-grant", string(grants), "", true},
		{"changed-during-initialization", "[]", string(grants), true},
		{"invalid-json", "{broken", "", false},
		{"null-query", "null", "", false},
		{"missing-data", `[{}]`, "", false},
		{"null-rules", `[{"data":"null"}]`, "", false},
		{"invalid-action", `[{"data":"[{\"permission\":\"*\",\"pattern\":\"*\",\"action\":\"future\"}]"}]`, "", false},
		{"duplicate-project", `[{"data":"[]"},{"data":"[]"}]`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
			root := filepath.Dir(capture)
			if err := os.WriteFile(filepath.Join(root, "permission-query.json"), []byte(tc.initial), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.after != "" {
				if err := os.WriteFile(filepath.Join(root, "permission-query-after.json"), []byte(tc.after), 0600); err != nil {
					t.Fatal(err)
				}
			}
			d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			req := driver.Request{Prompt: "must not execute"}
			req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
			response, err := d.Run(ctx, req, &testutil.EventRecorder{})
			if err == nil || errors.Is(err, errors.ErrUnsupported) != tc.unsupported || response.Checkpoint != nil {
				t.Fatalf("response=%+v err=%v", response, err)
			}
			if response.RawStreams == nil || !strings.Contains(response.RawStreams.Stdout, tc.initial) {
				t.Fatal("native probe stdout lost from audit")
			}
			if _, err := os.Stat(filepath.Join(root, "prompts.log")); !os.IsNotExist(err) {
				t.Fatal("unsafe prompt was delivered")
			}
		})
	}
}

func TestSavedPermissionCacheVerifiedOncePerProcess(t *testing.T) {
	f := persistentServerFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "permission-query.json"), []byte(`[{"data":"[]"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}})
	f.run(t, policy)
	f.run(t, policy)
	probes, _ := os.ReadFile(filepath.Join(f.dir, "permission-probes.log"))
	if len(strings.Fields(string(probes))) != 2 {
		t.Fatalf("unexpected probes=%s", probes)
	}
	f.run(t, policy, adaptor.WithSpawn())
	probes, _ = os.ReadFile(filepath.Join(f.dir, "permission-probes.log"))
	if len(strings.Fields(string(probes))) != 4 {
		t.Fatalf("new process did not verify grants=%s", probes)
	}
}

func TestNativeDefaultPolicyDoesNotProbeSavedGrants(t *testing.T) {
	f := persistentServerFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "permission-query.json"), []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	if _, err := os.Stat(filepath.Join(f.dir, "permission-probes.log")); !os.IsNotExist(err) {
		t.Fatal("native-default invocation probed or changed saved grants")
	}
}
