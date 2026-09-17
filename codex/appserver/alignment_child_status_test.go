package appserver

import (
	"context"
	"encoding/json"
	"errors"
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
		for _, order := range []string{"before", "after", "absent"} {
			for _, status := range []string{`{"type":"notLoaded"}`, `{"type":"idle"}`, `{"type":"systemError"}`, `{"type":"active","activeFlags":[]}`, `{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`} {
				t.Run(fmt.Sprintf("queued-%v/%s/%s", queued, order, status), func(t *testing.T) {
					sink := &recordingSink{}
					s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
					s.setThread("parent")
					if !queued {
						s.setTurn("turn")
					}
					if order == "before" {
						s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
					}
					s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", status))
					if order == "after" {
						s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
					}
					if queued {
						s.setTurn("turn")
					}
					if s.protocolError() != nil {
						t.Fatalf("valid status control rejected: %v", s.protocolError())
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
}

func TestAlignmentChildStatusValidation(t *testing.T) {
	for _, scenario := range []string{"unknown", "wrong-parent", "conflicting-role", "conflicting-source-role", "overflow", "malformed", "missing-status", "null-status", "unknown-status", "active-missing-flags", "active-null-flags", "active-unknown-flag", "missing-id", "empty-id", "unknown-malformed", "whitespace-id", "active-wrong-typed-flags", "active-wrong-typed-flag"} {
		t.Run(scenario, func(t *testing.T) {
			s := newRunState("run", &recordingSink{})
			s.setThread("parent")
			s.setTurn("turn")
			child := "child"
			switch scenario {
			case "unknown", "unknown-malformed":
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
			case "missing-id":
				body = json.RawMessage(`{"status":{"type":"idle"}}`)
			case "whitespace-id":
				body = alignmentChildStatus(" ", `{"type":"idle"}`)
			case "empty-id":
				body = alignmentChildStatus("", `{"type":"idle"}`)
			case "malformed", "unknown-malformed":
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
			case "active-wrong-typed-flags":
				body = alignmentChildStatus(child, `{"type":"active","activeFlags":"waitingOnApproval"}`)
			case "active-wrong-typed-flag":
				body = alignmentChildStatus(child, `{"type":"active","activeFlags":[1]}`)
			case "active-unknown-flag":
				body = alignmentChildStatus(child, `{"type":"active","activeFlags":["guess"]}`)
			}
			s.onNotification(NotifyThreadStatusChanged, body)
			if scenario == "unknown" || scenario == "overflow" {
				// Official 0.153.4 broadcasts status before attaching child
				// listeners; Raw-only control is not proof of child identity.
				if _, known := s.observation.children[child]; known || s.protocolError() != nil || len(s.observation.children) > 128 {
					t.Fatal("control registered identity or breached capacity")
				}
				if result := s.snapshot(Options{}, "parent", string(body), "", 0, "", false); result.Checkpoint != nil || result.RawStreams.Stdout != string(body) {
					t.Fatal("control changed checkpoint or Raw")
				}
				return
			}
			if s.protocolError() == nil {
				t.Fatal("conflicting identity or malformed status accepted")
			}
			if result := s.snapshot(Options{}, "parent", string(body), "", 0, "", false); result.Checkpoint != nil {
				t.Fatal("invalid child status gained checkpoint")
			}
		})
	}
}

func TestAlignmentChildStatusDoesNotRelaxOtherScope(t *testing.T) {
	for _, announced := range []bool{false, true} {
		for _, method := range []string{NotifyTurnStarted, NotifyTurnCompleted, NotifyItemStarted, NotifyItemCompleted, NotifyItemAgentMessageDelta, NotifyTurnPlanUpdated, NotifyThreadTokenUsageUpdated, NotifyError} {
			t.Run(fmt.Sprintf("announced-%v/%s", announced, method), func(t *testing.T) {
				s := newRunState("run", &recordingSink{})
				s.setThread("parent")
				s.setTurn("turn")
				if announced {
					s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
				}
				s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", `{"type":"idle"}`))
				if s.protocolError() != nil {
					t.Fatal("valid control poisoned parent")
				}
				s.onNotification(method, json.RawMessage(`{"threadId":"child","turnId":"turn","turn":{"id":"turn","status":"completed"}}`))
				if s.protocolError() == nil || !strings.Contains(s.protocolError().Error(), "belongs to thread") {
					t.Fatal("child turn/item/error/usage bypassed parent fence")
				}
			})
		}
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

func TestAlignmentChildStatusLateConflictingEvidence(t *testing.T) {
	for _, wrongParent := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongParent), func(t *testing.T) {
			sink := &recordingSink{}
			s := newRunState("run", sink, Options{ResolvedAgents: []driver.AgentSpec{{Key: "canonical", RuntimeName: "reviewer"}}})
			s.setThread("parent")
			s.setTurn("turn")
			s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", `{"type":"active","activeFlags":[]}`))
			if s.protocolError() != nil || len(s.observation.children) != 0 {
				t.Fatal("early control assigned identity")
			}
			if wrongParent {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "other", "reviewer"))
			} else {
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "reviewer"))
				s.onNotification(NotifyThreadStarted, alignmentChildMetadata("child", "parent", "writer"))
				s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("child", `{"type":"idle"}`))
			}
			if s.protocolError() == nil {
				t.Fatal("late wrong-parent/conflict escaped")
			}
			s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
			result := s.snapshot(Options{}, "parent", "raw", "", 0, "", false)
			facts, _ := alignmentFacts(sink)
			if result.Checkpoint != nil || len(facts) != 0 {
				t.Fatal("later evidence erased failure or invented fact")
			}
		})
	}
}

func TestAlignmentChildStatusWire(t *testing.T) {
	command := alignmentFixture(t)
	for _, resident := range []bool{false, true} {
		for _, scenario := range []string{"child-status", "unknown-child-status", "early-child-status", "unrelated-status", "unrelated-status-cancel"} {
			name := fmt.Sprintf("resident-%v/%s", resident, scenario)
			if scenario == "child-status" || scenario == "unknown-child-status" {
				name = fmt.Sprintf("resident-%v/unknown-%v", resident, scenario == "unknown-child-status")
			}
			t.Run(name, func(t *testing.T) {
				opts := alignmentOptions(command, filepath.Join(t.TempDir(), "capture.jsonl"))
				opts.CWD = t.TempDir()
				opts.ResolvedAgents = []driver.AgentSpec{{Key: "canonical-reviewer", RuntimeName: "reviewer"}}
				opts.Env = append(opts.Env, driver.EnvBinding{Name: "ALIGNMENT_SCENARIO", Value: scenario}, driver.EnvBinding{Name: "CODEX_HOME", Value: t.TempDir()})
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				sink := &recordingSink{}
				cancelled := scenario == "unrelated-status-cancel"
				if cancelled {
					sink.onStream = func(p driver.StreamPayload) {
						if p.Kind == driver.StreamTextContent {
							cancel()
						}
					}
				}
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
				if result.RawStreams == nil || !strings.Contains(result.RawStreams.Stdout, `"method":"thread/status/changed"`) || !strings.Contains(result.RawStreams.Stdout, `"threadId":"agent-child"`) {
					t.Fatal("control Raw lost")
				}
				if cancelled {
					if !errors.Is(err, context.Canceled) || result.Checkpoint != nil || result.RawStreams.Terminal != nil || result.Usage == nil || result.Usage.OutputTokens != 2 || result.RawStreams.Stderr != "fixture-stderr" {
						t.Fatalf("control cancellation lost cause/audit or gained checkpoint: %v %+v", err, result)
					}
				} else if err != nil || result.Failure != nil || result.Checkpoint == nil || !result.Checkpoint.Valid || result.Output != "answer" || result.RawStreams.Terminal == nil || result.RawStreams.Terminal.Event != NotifyTurnCompleted || result.Usage == nil || result.Usage.OutputTokens != 2 {
					t.Fatalf("control poisoned parent: err=%v response=%+v", err, result)
				}
				caps, _ := alignmentFacts(sink)
				if scenario == "child-status" || scenario == "early-child-status" {
					if len(caps) != 2 || caps[0].Phase != capability.Started || caps[1].Phase != capability.Completed || caps[1].Ref.Key != "canonical-reviewer" {
						t.Fatalf("formal child/receiver fact lost: %+v", caps)
					}
				} else if len(caps) != 0 {
					t.Fatalf("Raw control became subagent evidence: %+v", caps)
				}
				for _, item := range result.Transcript {
					if item.SessionID == "agent-child" || item.Text == "child control" {
						t.Fatal("control became parent transcript")
					}
				}
			})
		}
	}
}

func TestAlignmentChildStatusBoundaries(t *testing.T) {
	t.Run("unassociated-controls-do-not-fill-children", func(t *testing.T) {
		sink := &recordingSink{}
		s := newRunState("run", sink)
		s.setThread("parent")
		s.setTurn("turn")
		for i := 0; i < 256; i++ {
			s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus(fmt.Sprint(i), `{"type":"idle"}`))
		}
		facts, plans := alignmentFacts(sink)
		if s.protocolError() != nil || len(s.observation.children) != 0 || len(facts) != 0 || len(plans) != 0 || s.terminal != nil || len(s.transcript) != 1 || len(sink.streams) != 1 {
			t.Fatal("controls allocated identity or semantics")
		}
		s.onNotification(NotifyThreadStarted, alignmentChildMetadata("actual", "parent", "reviewer"))
		if len(s.observation.children) != 1 {
			t.Fatal("control flood consumed child capacity")
		}
		s.onNotification(NotifyThreadStarted, alignmentChildMetadata("actual", "parent", "writer"))
		s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("actual", `{"type":"idle"}`))
		if s.protocolError() == nil {
			t.Fatal("control flood repaired later proven conflict")
		}
	})
	t.Run("prior-error-not-cleared", func(t *testing.T) {
		s := newRunState("run", &recordingSink{})
		s.setThread("parent")
		s.setTurn("turn")
		s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"foreign","turn":{"id":"turn","status":"completed"}}`))
		prior := s.protocolError()
		s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("foreign", `{"type":"idle"}`))
		s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
		if prior == nil || s.protocolError() != prior || s.snapshot(Options{}, "parent", "raw", "", 0, "", false).Checkpoint != nil {
			t.Fatal("control erased an established protocol failure")
		}
	})
	t.Run("terminal-remains-final", func(t *testing.T) {
		sink := &recordingSink{}
		s := newRunState("run", sink)
		s.setThread("parent")
		s.setTurn("turn")
		s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
		count := len(s.transcript)
		terminal := string(s.terminal.JSON)
		s.onNotification(NotifyThreadStatusChanged, alignmentChildStatus("unknown", `{"type":"active","activeFlags":[]}`))
		if s.protocolError() != nil || len(s.transcript) != count || string(s.terminal.JSON) != terminal || len(s.observation.children) != 0 {
			t.Fatal("trailing control changed completed result")
		}
		s.onNotification(NotifyTurnCompleted, json.RawMessage(`{"threadId":"parent","turn":{"id":"turn","status":"completed"}}`))
		if s.protocolError() == nil || s.snapshot(Options{}, "parent", "raw", "", 0, "", false).Checkpoint != nil {
			t.Fatal("control relaxed duplicate terminal guard")
		}
	})
}
