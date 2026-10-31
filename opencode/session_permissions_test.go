package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestSessionPermissionAppendAndRestore(t *testing.T) {
	for _, withBaseline := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty-baseline", true: "native-baseline"}[withBaseline], func(t *testing.T) {
			agent := []permissionRule{{"*", "*", "allow"}, {"read", "*.env", "deny"}, {"read", "public.env", "allow"}, {"bash", "blocked*", "deny"}}
			var baseline []permissionRule
			if withBaseline {
				baseline = []permissionRule{{"read", "session.env", "allow"}, {"bash", "custom*", "deny"}, {"read", "review*", "ask"}}
			}
			current := append([]permissionRule(nil), baseline...)
			patches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					_, _ = w.Write([]byte(`{"default_agent":"custom"}`))
				case "/agent":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "custom", "mode": "primary", "permission": agent}})
				case "/session/session":
					if r.Method != "PATCH" {
						t.Errorf("unexpected method=%s", r.Method)
					}
					var input struct {
						Permission []permissionRule `json:"permission"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					// The actual API appends, including the earlier SDK segments.
					current = append(current, input.Permission...)
					patches++
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "session", "permission": current})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			for step, gated := range []bool{false, true, true, false, false, true, false} {
				before := patches
				if err := client.configureSessionPermissions(context.Background(), "", "session", baseline, append([]permissionRule(nil), current...), gated); err != nil {
					t.Fatal(err)
				}
				if (step == 2 || step == 4) && before != patches {
					t.Fatal("unchanged policy grew the native session rules")
				}
				wanted := append(append([]permissionRule(nil), agent...), baseline...)
				if gated {
					for i := range wanted {
						if wanted[i].Action == "allow" {
							wanted[i].Action = "ask"
						}
					}
				}
				for _, sample := range []struct{ permission, pattern string }{{"read", "marker.txt"}, {"read", "secret.env"}, {"read", "public.env"}, {"read", "session.env"}, {"read", "review.txt"}, {"bash", "blocked command"}, {"bash", "custom command"}, {"future_tool", "anything"}} {
					if got, want := permissionOutcome(current, sample.permission, sample.pattern), permissionOutcome(wanted, sample.permission, sample.pattern); got != want {
						t.Fatalf("step=%d sample=%+v got=%s want=%s rules=%+v", step, sample, got, want, current)
					}
				}
			}
		})
	}
}

func TestSessionPermissionDeliveryVerified(t *testing.T) {
	for _, reply := range []string{`true`, `{}`, `null`, `{"id":"wrong","permission":[]}`, `{"id":"session"}`, `{"id":"session","permission":[]}`, `{"id":"session","permission":[{"permission":"*","pattern":"*","action":"allow","future":true}]}`} {
		t.Run(reply, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config":
					_, _ = w.Write([]byte(`{}`))
				case "/agent":
					_, _ = w.Write([]byte(`[{"name":"build","mode":"primary","permission":[{"permission":"*","pattern":"*","action":"allow"}]}]`))
				default:
					_, _ = w.Write([]byte(reply))
				}
			}))
			defer server.Close()
			client := &serverClient{base: server.URL, http: server.Client()}
			if err := client.configureSessionPermissions(context.Background(), "", "session", nil, nil, false); err == nil {
				t.Fatal("unverified delivery accepted")
			}
		})
	}
}

func TestSessionPermissionFailureStopsBeforePrompt(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}, {Name: "AGENT_ADAPTOR_OPENCODE_IGNORE_SESSION_PERMISSIONS", Value: "1"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := d.Run(ctx, driver.Request{Prompt: "must not execute", Streaming: true}, &testutil.EventRecorder{})
	if err == nil || !strings.Contains(err.Error(), "permission update") || response.Checkpoint != nil {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(capture), "prompts.log")); !os.IsNotExist(err) {
		t.Fatal("prompt sent despite ignored native permission patch")
	}
}

func TestOriginalSessionBaselineSurvivesRepeatedPolicies(t *testing.T) {
	command, capture := testutil.ProtocolCommand(t, serverHappy, "", 0)
	d := Driver(Config{Command: command, Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}}).(configuredDriver)
	defer d.CloseProcesses(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	baseline := []permissionRule{{"edit", "protected/*", "deny"}, {"read", "public.env", "allow"}}
	seed, _ := json.Marshal(map[string]any{"ses_child": map[string]any{"id": "ses_child", "permission": baseline}})
	if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "session-info.json"), seed, 0600); err != nil {
		t.Fatal(err)
	}
	var state *driver.SessionState
	for _, gated := range []bool{true, false, true, false} {
		mode := driver.HumanDecisionAutoApprove
		if gated {
			mode = driver.HumanDecisionAsk
		}
		r := driver.Request{Prompt: "test", Streaming: true, Session: &driver.SessionContext{Mode: driver.SessionContinueOrStart, EngineSessionID: "record", State: state}}
		r.Policy.HumanDecision.Permission = mode
		// Use the actual Driver checkpoint, which carries the immutable baseline.
		response, err := d.Run(ctx, r, &testutil.EventRecorder{})
		if err != nil || response.Checkpoint == nil || !response.Checkpoint.Valid {
			t.Fatalf("response=%+v err=%v", response, err)
		}
		state = response.Checkpoint.State
		var saved []permissionRule
		if err := json.Unmarshal([]byte(state.Data["permission_base"]), &saved); err != nil || !reflect.DeepEqual(saved, baseline) {
			t.Fatalf("baseline drifted: %+v %v", saved, err)
		}
		if state.ResumeID != "ses_child" {
			t.Fatal("policy switch replaced native conversation")
		}
	}
}
