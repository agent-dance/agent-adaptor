package appserver

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

// Exercise formal notifications, not direct table mutation. Both identities and
// roles remain independently proven by thread/started and the current collab.
func TestAlignmentChildRoleCapacity(t *testing.T) {
	for _, count := range []int{127, 128} {
		for _, scenario := range []string{"matching-replay", "conflicting-replay", "conflicting-source-role", "new-child", "no-collab"} {
			t.Run(fmt.Sprintf("%d/%s", count, scenario), func(t *testing.T) {
				sink := &recordingSink{}
				s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical-reviewer", RuntimeName: "reviewer"}, {Key: "canonical-writer", RuntimeName: "writer"}}})
				s.setThread("parent")
				s.setTurn("turn")
				child := func(id, role, sourceRole string) {
					s.onNotification(NotifyThreadStarted, json.RawMessage(fmt.Sprintf(`{"thread":{"id":%q,"agentRole":%q,"source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":%q,"depth":1}}}}}`, id, role, sourceRole)))
				}
				collab := func(method, status, receiver string) {
					s.onNotification(method, json.RawMessage(fmt.Sprintf(`{"threadId":"parent","turnId":"turn","item":{"id":"spawn","type":"collabAgentToolCall","tool":"spawnAgent","senderThreadId":"parent","receiverThreadIds":[%q],"status":%q}}`, receiver, status)))
				}
				for i := 0; i < count; i++ {
					child(fmt.Sprintf("child-%d", i), "reviewer", "")
				}
				if len(s.observation.children) != count {
					t.Fatal("initial identity count changed")
				}
				receiver := "child-0"
				if scenario == "new-child" {
					receiver = "new-child"
					child(receiver, "reviewer", "")
					child(receiver, "reviewer", "") // A refused new ID must remain refused.
				}
				if scenario != "no-collab" {
					collab(NotifyItemStarted, "inProgress", receiver)
				}
				want := capability.Completed
				switch scenario {
				case "conflicting-replay":
					child("child-0", "writer", "")
					child("child-0", "reviewer", "") // Matching replay cannot erase a conflict.
					want = capability.Interrupted
				case "conflicting-source-role":
					child("child-0", "reviewer", "writer")
					child("child-0", "reviewer", "reviewer")
					want = capability.Interrupted
				default:
					child("child-0", "reviewer", "")
					child("child-0", "reviewer", "")
				}
				if scenario != "no-collab" {
					collab(NotifyItemCompleted, "completed", receiver)
					collab(NotifyItemCompleted, "completed", receiver)
					child("child-0", "reviewer", "")
				}
				s.closeObservations(nil)
				if s.protocolError() != nil {
					t.Fatalf("valid notification envelope rejected: %v", s.protocolError())
				}
				if s.threadID != "parent" || s.turnID != "turn" {
					t.Fatal("child metadata changed current thread/turn")
				}
				wantCount := count
				if scenario == "new-child" && count == 127 {
					wantCount++
				}
				if len(s.observation.children) != wantCount {
					t.Fatalf("identity count=%d want=%d", len(s.observation.children), wantCount)
				}
				facts, _ := alignmentFacts(sink)
				if scenario == "no-collab" || (scenario == "new-child" && count == 128) {
					if len(facts) != 0 {
						t.Fatalf("unproven child/receiver produced facts: %+v", facts)
					}
					if scenario == "new-child" {
						if _, exists := s.observation.children[receiver]; exists {
							t.Fatal("overflow identity was inserted")
						}
						if !s.observation.notices["capability_unresolved"] {
							t.Fatal("overflow did not report unavailable observation")
						}
					}
					return
				}
				if len(facts) != 2 || facts[0].Phase != capability.Started || facts[1].Phase != want {
					t.Fatalf("facts=%+v want exactly Started then %s; stored=%+v", facts, want, s.observation.children["child-0"])
				}
				for _, fact := range facts {
					if fact.Ref != (capability.Ref{Kind: capability.Subagent, Key: "canonical-reviewer", Operation: "spawn"}) || fact.Evidence != capability.ProviderProtocol || fact.Source != capability.Provider || fact.ParentToolCallID != "" {
						t.Fatalf("unproved attribution: %+v", fact)
					}
				}
				if want == capability.Interrupted {
					if !s.observation.children["child-0"].conflict || facts[1].ErrorCode != capability.RunInterrupted {
						t.Fatal("conflict was cleared or pending lifecycle lost")
					}
				} else if s.observation.children["child-0"].conflict || facts[1].ErrorCode != "" {
					t.Fatal("matching replay changed a healthy lifecycle")
				}
			})
		}
	}
}
