package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
	"github.com/agent-dance/agent-adaptor/driver"
)

func alignmentChildMetadata(id, parent, role string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"thread":{"id":%q,"agentRole":%q,"source":{"subAgent":{"thread_spawn":{"parent_thread_id":%q,"agent_role":%q,"depth":1}}}}}`, id, role, parent, role))
}
func alignmentChildStatus(id, status string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"threadId":%q,"status":%s}`, id, status))
}

func TestAlignmentChildStatusIsolation(t *testing.T) {
	for _, queued := range []bool{false, true} {
		for _, status := range []string{`{"type":"notLoaded"}`, `{"type":"idle"}`, `{"type":"systemError"}`, `{"type":"active","activeFlags":[]}`, `{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`} {
			t.Run(fmt.Sprintf("queued-%v/%s", queued, status), func(t *testing.T) {
				sink := &recordingSink{}
				s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
				s.setThread("parent")
				if !queued {
					s.setTurn("turn")
				}
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
				s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", status))
				if queued {
					s.setTurn("turn")
				}
				if s.protocolError() != nil {
					t.Fatalf("formally related child control rejected: %v", s.protocolError())
				}
				select {
				case <-s.done:
					t.Fatal("child status completed parent")
				default:
				}
				if s.threadID != "parent" || s.turnID != "turn" || s.usage != nil || s.terminal != nil || s.finalAgentText != "" {
					t.Fatal("child status mutated parent outcome")
				}
				caps, plans := alignmentFacts(sink)
				if len(caps) != 0 || len(plans) != 0 || len(s.transcript) != 1 || len(sink.streams) != 1 || sink.streams[0].Kind != driver.StreamRunStarted || sink.streams[0].ThreadID != "parent" || sink.streams[0].TurnID != "turn" {
					t.Fatalf("child status published parent semantics: caps=%+v plans=%+v streams=%+v transcript=%+v", caps, plans, sink.streams, s.transcript)
				}
				// The parent's actual terminal, not the child's idle/systemError status,
				// remains the only source of a healthy parent checkpoint.
				s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
				result := s.snapshot(Options{}, "parent", "raw", "", 0, "", false)
				if s.protocolError() != nil || result.Checkpoint == nil || !result.Checkpoint.Valid || result.RawStreams.Terminal == nil {
					t.Fatal("parent terminal no longer completes")
				}
			})
		}
	}
}

func TestAlignmentChildStatusRequiresProvenIdentity(t *testing.T) {
	for _, scenario := range []string{"unknown", "wrong-parent", "conflicting-role", "conflicting-source-role", "overflow", "malformed", "missing-status", "null-status", "unknown-status", "active-missing-flags", "active-null-flags", "active-unknown-flag"} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.setThread("parent")
			s.setTurn("turn")
			child := "child"
			switch scenario {
			case "unknown":
			case "wrong-parent":
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata(child, "other", "reviewer"))
			case "conflicting-role":
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata(child, "parent", "reviewer"))
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata(child, "parent", "writer"))
			case "conflicting-source-role":
				s.onNotification(NotifyThreadStarted, json.RawMessage(`{"thread":{"id":"child","agentRole":"reviewer","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","agent_role":"writer","depth":1}}}}}`))
			case "overflow":
				for i := 0; i < 128; i++ {
					s.onNotification(NotifyThreadStarted, alignmentChildMetadata(fmt.Sprint(i), "parent", "reviewer"))
				}
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata(child, "parent", "reviewer"))
			default:
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata(child, "parent", "reviewer"))
			}
			body := alignmentChildStatus(child, `{"type":"idle"}`)
			switch scenario {
			case "malformed":
				body = json.RawMessage(`{"threadId":"child","status":[]}`)
			case "missing-status":
				body = json.RawMessage(`{"threadId":"child"}`)
			case "null-status":
				body = alignmentChildStatus(child, `null`)
			case "unknown-status":
				body = alignmentChildStatus(child, `{"type":"future"}`)
			case "active-missing-flags":
				body = alignmentChildStatus(child, `{"type":"active"}`)
			case "active-null-flags":
				body = alignmentChildStatus(child, `{"type":"active","activeFlags":null}`)
			case "active-unknown-flag":
				body = alignmentChildStatus(child, `{"type":"active","activeFlags":["guess"]}`)
			}
			s.onNotification(NotifyThreadStatusChanged, body)
			if s.protocolError() == nil {
				t.Fatal("unproven or malformed child status accepted")
			}
			if result := s.snapshot(Options{}, "parent", string(body), "", 0, "", false); result.Checkpoint != nil {
				t.Fatal("invalid child status gained checkpoint")
			}
		})
	}
}

func TestAlignmentChildStatusDoesNotRelaxOtherScope(t *testing.T) {
	for _, method := range []string{NotifyTurnStarted, NotifyTurnCompleted, NotifyItemStarted, NotifyItemCompleted, NotifyItemAgentMessageDelta, NotifyTurnPlanUpdated, NotifyThreadTokenUsageUpdated, NotifyError} {
		t.Run(method, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.setThread("parent")
			s.setTurn("turn")
			s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
			s.onNotification(method, json.RawMessage(`{"threadId":"child","turnId":"turn","turn":{"id":"turn","status":"completed"}}`))
			if s.protocolError() == nil || !strings.Contains(s.protocolError().Error(), "belongs to thread") {
				t.Fatal("child turn/item/error/usage bypassed parent fence")
			}
		})
	}
	// A status does not authorize a role, a spawn receiver, or a new turn.
	sink := &recordingSink{}
	s := newRunState("run", sink)
	s.setThread("parent")
	s.setTurn("turn")
	s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "unknown-role"))
	s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", `{"type":"idle"}`))
	caps, _ := alignmentFacts(sink)
	if s.protocolError() != nil || len(caps) != 0 {
		t.Fatal("control isolation incorrectly claimed catalog resolution")
	}
	s.onNotification(NotifyItemStarted, json.RawMessage(`{"threadId":"parent","turnId":"old","item":{"id":"x","type":"agentMessage","text":"wrong turn"}}`))
	if s.protocolError() == nil || !strings.Contains(s.protocolError().Error(), "belongs to turn") {
		t.Fatal("parent old turn guard weakened")
	}
}

func TestAlignmentChildStatusWire(t *testing.T) {
	command := alignmentFixture(t)
	for _, resident := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("resident-%v/unknown-%v", resident, unknown), func(t *testing.T) {
				opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture.jsonl"))
				opts.CWD = t.TempDir()
				opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical-reviewer", RuntimeName: "reviewer"}}
				scenario := "child-status"
				if unknown {
					scenario = "unknown-child-status"
				}
				opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: scenario}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				sink := &recordingSink{}
				var result driver.Response
				var err error
				if resident {
					p, e := Open(ctx, opts, sink)
					if e != nil {
						t.Fatal(e)
					}
					defer p.TerminateAndWait(context.Background())
					result, _, err = p.RunTurn(ctx, opts, sink)
				} else {
					result, err = Run(ctx, opts, sink)
				}
				if result.RawStreams == nil || !strings.Contains(result.RawStreams.Stdout, `"method":"thread/status/changed"`) {
					t.Fatal("child control Raw lost")
				}
				if unknown {
					if err == nil || !strings.Contains(err.Error(), "belongs to thread") || result.Checkpoint != nil {
						t.Fatalf("unproven identity admitted: err=%v cp=%+v", err, result.Checkpoint)
					}
					return
				}
				if err != nil || result.Failure != nil || result.Checkpoint == nil || !result.Checkpoint.Valid || result.Output != "answer" || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != NotifyTurnCompleted || result.Usage == nil || result.Usage.OutputTokens != 2 {
					t.Fatalf("related child poisoned parent: err=%v response=%+v", err, result)
				}
				caps, _ := alignmentFacts(sink)
				if len(caps) != 2 || caps[0].Phase != capability.Started || caps[1].Phase != capability.Completed || caps[1].Ref.Key != "canonical-reviewer" {
					t.Fatalf("formal child/receiver fact lost: %+v", caps)
				}
				for _, item := range result.Transcript {
					if item.SessionID == "agent-child" || item.Text == "child control" {
						t.Fatal("child control became parent transcript")
					}
				}
			})
		}
	}
}
