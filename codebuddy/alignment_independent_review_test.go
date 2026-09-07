// Regression coverage derived from C01's fixed-object T15 review. Original
// fixture bytes and failing output are retained in the task handoff evidence.
package codebuddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/todo"
)

func TestAlignmentReviewT15SkillAliasValidation(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        int
	}{
		{"nonstring_skill_valid_command", `{"skill":17,"command":" review "}`, 0},
		{"null_skill_valid_command", `{"skill":null,"command":" review "}`, 0},
		{"empty_skill_valid_command", `{"skill":"","command":" review "}`, 0},
		{"command_only", `{"command":" review "}`, 2},
		{"skill_only", `{"skill":" review "}`, 2},
		{"equal_aliases", `{"skill":" review ","command":" review "}`, 2},
		{"nonstring_command_valid_skill", `{"skill":" review ","command":17}`, 0},
		{"null_command_valid_skill", `{"skill":" review ","command":null}`, 0},
		{"empty_command_valid_skill", `{"skill":" review ","command":""}`, 0},
		{"object_command_valid_skill", `{"skill":" review ","command":{}}`, 0},
		{"array_skill_valid_command", `{"skill":[],"command":" review "}`, 0},
		{"boolean_skill_valid_command", `{"skill":true,"command":" review "}`, 0},
		{"control_skill_valid_command", `{"skill":"\n","command":" review "}`, 0},
		{"different_aliases", `{"skill":"other","command":" review "}`, 0},
		{"missing_aliases", `{"additive":true}`, 0},
		{"unknown_additive", `{"skill":" review ","command":" review ","additive":{"value":null}}`, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &testutil.EventRecorder{}
			p := newParser(rec)
			p.configureObservations(context.Background(), alignmentObservationRequest())
			alignmentFeed(t, p, alignmentCall("Skill", "s", tc.input), alignmentResult("s", `"ok"`, false, ""))
			p.completeStream(nil, 0, "", false)
			facts := alignmentCapabilities(rec)
			t.Logf("facts=%+v", facts)
			if len(facts) != tc.want {
				t.Fatalf("invalid present alias was hidden by fallback: got %d want %d", len(facts), tc.want)
			}
		})
	}
}

func alignmentReviewT15Protocol(t *testing.T, frames ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "protocol.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(append(frames, `{"type":"result","subtype":"success","is_error":false,"session_id":"codebuddy-persistent-session","result":"terminal-authority","usage":{"input_tokens":0,"output_tokens":0}}`, ""), "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func alignmentReviewT15Env(root string) []driver.EnvBinding {
	return []driver.EnvBinding{{Name: "USERPROFILE", Value: root}, {Name: "XDG_CONFIG_HOME", Value: filepath.Join(root, "xdg")}, {Name: "ALIGNMENT_CODEBUDDY_NATIVE_EXEC", Value: "1"}}
}

func TestAlignmentReviewT15PublicTaskUpdateConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, content, raw string
		failed             bool
		want               int
		input              string
	}{
		{"empty_raw_unknown_text", `"not a confirmed task update"`, `{}`, false, 1, ""},
		{"unknown_raw_unknown_text", `"not a confirmed task update"`, `{"unrelated":true}`, false, 1, ""},
		{"real_task", `"updated"`, `{"task":{"id":"91","subject":"original","status":"completed"}}`, false, 2, ""},
		{"real_full_todos", `"updated"`, `{"todos":[{"id":"91","content":"original","status":"completed"}]}`, false, 2, ""},
		{"exact_text_without_meta", `"Updated task #91 status"`, "", false, 2, ""},
		{"failed_full_todos", `"failure"`, `{"todos":[]}`, true, 1, ""},
		{"missing_raw_unknown_text", `"not a confirmed task update"`, "", false, 1, ""},
		{"unknown_raw_formal_text", `"Updated task #91 status"`, `{"unrelated":true}`, false, 2, ""},
		{"null_task_formal_text", `"Updated task #91 status"`, `{"task":null}`, false, 1, ""},
		{"wrong_task_formal_text", `"Updated task #91 status"`, `{"task":{"id":"other","status":"completed"}}`, false, 1, ""},
		{"unknown_success_prefix", `"Updated task #91 not a confirmed task update"`, "", false, 1, ""},
		{"trailing_success_suffix", `"Updated task #91 status but unsuccessful"`, "", false, 1, ""},
		{"unconfirmed_status_field", `"Updated task #91 description"`, "", false, 1, ""},
		{"failed_formal_text", `"Updated task #91 status"`, `{}`, true, 1, ""},
		{"multiple_formal_fields", `"Updated task #91 subject, description, activeForm, owner, metadata, blocks, blockedBy, status"`, "", false, 2, `{"taskId":"91","subject":"renamed","description":"detail","activeForm":"updating","owner":"agent","metadata":{},"addBlocks":[],"addBlockedBy":[],"status":"completed","unknownAdditive":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			updateInput := tc.input
			if updateInput == "" {
				updateInput = `{"taskId":"91","status":"completed"}`
			}
			path := alignmentReviewT15Protocol(t, `{"type":"system","subtype":"init","session_id":"codebuddy-persistent-session"}`, alignmentCall("TaskCreate", "create", `{"subject":"original"}`), alignmentResult("create", `"Task #91 created successfully: original"`, false, `{"task":{"id":"91","subject":"original","status":"pending"},"todos":[{"id":"91","content":"original","status":"pending"}]}`), alignmentCall("TaskUpdate", "update", updateInput), alignmentResult("update", tc.content, tc.failed, tc.raw), `{"type":"assistant","message":{"content":[{"type":"text","text":"intermediate"}]}}`)
			service := &alignmentObservationService{events: map[string][]adaptor.Event{}}
			env := append(alignmentReviewT15Env(t.TempDir()), driver.EnvBinding{Name: "ALIGNMENT_CODEBUDDY_PROTOCOL", Value: path})
			f := newPersistentCodeBuddyFixtureWithOptions(t, env, adaptor.WithRunServices(service))
			defer f.close()
			result, err := f.agent.Run(context.Background(), "read protocol")
			if err != nil {
				t.Fatal(err)
			}
			var got []todo.Snapshot
			service.mu.Lock()
			for _, events := range service.events {
				for _, e := range events {
					if v, ok := e.(adaptor.TodoUpdated); ok {
						got = append(got, v.Snapshot)
					}
				}
			}
			service.mu.Unlock()
			t.Logf("snapshots=%+v", got)
			if len(got) != tc.want {
				t.Fatalf("unconfirmed input patch mutated table: got %d want %d", len(got), tc.want)
			}
			if len(got) > 0 && (got[0].Items[0].ID != "91" || got[0].Items[0].SyntheticID || got[0].Revision != 1) {
				t.Fatal("real ID/snapshot changed")
			}
			if tc.want == 2 && got[len(got)-1].Items[0].Status != todo.Completed {
				t.Fatal("confirmed task output not applied")
			}
			if result.Text != "terminal-authority" || result.Usage == nil || result.Raw().Terminal == nil || !strings.Contains(result.Raw().Stdout, tc.content) || !strings.Contains(result.Raw().Stdout, updateInput) || len(result.Transcript()) < 5 {
				t.Fatal("CodeBuddy terminal/Raw/usage lost")
			}
		})
	}
}
