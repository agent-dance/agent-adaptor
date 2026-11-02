package codebuddy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
)

type turnTestSink struct {
	mu      sync.Mutex
	events  []driver.RunEvent
	turnErr error
}

func (s *turnTestSink) Emit(e driver.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	if e.Type == driver.RunEventTurnCompleted {
		return s.turnErr
	}
	return nil
}
func (*turnTestSink) EmitStream(driver.StreamPayload) error { return nil }
func (*turnTestSink) RequestDecision(context.Context, driver.DecisionRequest) (driver.DecisionResponse, error) {
	return driver.DecisionResponse{Result: driver.DecisionApproved}, nil
}
func (s *turnTestSink) turns() []*driver.TurnCompleted {
	s.mu.Lock()
	defer s.mu.Unlock()
	var turns []*driver.TurnCompleted
	for _, e := range s.events {
		if e.Type == driver.RunEventTurnCompleted {
			turns = append(turns, e.Turn)
		}
	}
	return turns
}

// No boundary is advertised, so CodeBuddy must never consume the message port.
type unusedMessagePort struct{ t *testing.T }

func (p unusedMessagePort) Window(bool) { p.t.Error("CodeBuddy opened an unverified message window") }
func (p unusedMessagePort) Signal() <-chan struct{} {
	p.t.Error("CodeBuddy watched an unverified message boundary")
	return nil
}
func (p unusedMessagePort) Claim(driver.Boundary) []driver.MessageItem {
	p.t.Error("CodeBuddy claimed at an unverified boundary")
	return nil
}
func (p unusedMessagePort) Ack(string, driver.MessageAck) {
	p.t.Error("CodeBuddy acknowledged an unverified message")
}

func TestCodeBuddyMessageDeclaration(t *testing.T) {
	d := Driver(Config{})
	caps := d.Descriptor().Messages
	if len(caps.Boundaries) != 0 || caps.Interrupt || caps.Continue != driver.ContinueResume || caps.ConfirmedDelivery || caps.PersistsDelivered {
		t.Fatalf("capabilities: %+v", caps)
	}
	codec := d.(driver.SessionCodecProvider).SessionCodec()
	if !d.Descriptor().Sessions.SupportsResume || codec == nil {
		t.Fatal("ContinueResume requires session resume")
	}
	state := &driver.SessionState{ResumeID: "resume", DisplayID: "display"}
	if got := codec.FromParams(codec.ToParams(state)); !reflect.DeepEqual(got, state) {
		t.Fatalf("resume round trip: %+v", got)
	}
}

// Reuses the existing subprocess protocol fixture directly at the Driver SPI,
// without constructing an Agent or depending on the migrating root API.
func codeBuddyTurnDriver(t *testing.T) (driver.Driver, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executable fixture uses a Unix process")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	spawnFile := filepath.Join(dir, "spawns")
	d := Driver(Config{CommonConfig: CommonConfig{Command: exe, CWD: dir, GracePeriod: 50 * time.Millisecond, Env: []driver.EnvBinding{
		{Name: codeBuddyPersistentHelperEnv, Value: "1"},
		{Name: "SPAWN_FILE", Value: spawnFile},
		{Name: "PID_FILE", Value: filepath.Join(dir, "pid")},
		{Name: "OVERLAP_FILE", Value: filepath.Join(dir, "overlap")},
		{Name: "CODEBUDDY_CONFIG_DIR", Value: profile},
		{Name: "HOME", Value: dir},
	}}})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.(driver.ProcessLifecycleDriver).CloseProcesses(ctx); err != nil {
			t.Error(err)
		}
	})
	return d, spawnFile
}

func TestCodeBuddyTurnCompletedEveryTransport(t *testing.T) {
	for _, transport := range []string{"headless", "control", "resident"} {
		t.Run(transport, func(t *testing.T) {
			d, spawnFile := codeBuddyTurnDriver(t)
			req := driver.Request{RunID: "run", Prompt: "work", Messages: unusedMessagePort{t}}
			if transport == "control" {
				req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
			}
			runs := 1
			if transport == "resident" {
				req.Session = &driver.SessionContext{EngineSessionID: "thread"}
				runs = 2
			}
			for i := 0; i < runs; i++ {
				sink := &turnTestSink{}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				res, err := d.Run(ctx, req, sink)
				cancel()
				if err != nil || res.Output != "ok" || res.Checkpoint == nil {
					t.Fatalf("response=%+v err=%v", res, err)
				}
				turns := sink.turns()
				if len(turns) != 1 || turns[0] == nil || turns[0].Index != 0 || turns[0].Text != res.Output || turns[0].CompletedAt.IsZero() || turns[0].Interrupted || !reflect.DeepEqual(turns[0].Usage, res.Usage) {
					t.Fatalf("turns=%+v", turns)
				}
				if transport == "resident" {
					req.Session = &driver.SessionContext{EngineSessionID: "thread", State: res.Checkpoint.State}
				}
			}
			data, err := os.ReadFile(spawnFile)
			if err != nil || len(strings.Fields(string(data))) != 1 {
				t.Fatalf("provider spawn count: %q %v", data, err)
			}
		})
	}
}

func TestCodeBuddyTurnCompletedSinkError(t *testing.T) {
	for _, transport := range []string{"headless", "control", "resident"} {
		t.Run(transport, func(t *testing.T) {
			d, _ := codeBuddyTurnDriver(t)
			req := driver.Request{Prompt: "work"}
			if transport == "control" {
				req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
			}
			if transport == "resident" {
				req.Session = &driver.SessionContext{EngineSessionID: "thread"}
			}
			want := errors.New("turn sink closed")
			sink := &turnTestSink{turnErr: want}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, err := d.Run(ctx, req, sink)
			if !errors.Is(err, want) || len(sink.turns()) != 1 {
				t.Fatalf("err=%v turns=%+v", err, sink.turns())
			}
		})
	}
}

func TestCodeBuddyTurnCompletedUsageAndExactlyOnce(t *testing.T) {
	sink := &turnTestSink{}
	p := newParser(sink)
	if err := p.onChunk("stdout", []byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"session","result":"done","usage":{"input_tokens":4,"output_tokens":3}}`+"\n"), time.Time{}); err != nil {
		t.Fatal(err)
	}
	p.finalize()
	for range 2 {
		if err := p.completeStream(nil, 0, "", false); err != nil {
			t.Fatal(err)
		}
	}
	turns := sink.turns()
	if len(turns) != 1 || turns[0].Text != "done" || turns[0].Usage == nil || turns[0].Usage.InputTokens != 4 || turns[0].Usage.OutputTokens != 3 {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestCodeBuddyTurnCompletedOnResidentDisconnect(t *testing.T) {
	d, _ := codeBuddyTurnDriver(t)
	cfg := d.(configuredDriver).cfg
	cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "FAIL_AFTER_PERSISTENT_READ", Value: "1"})
	sink := &turnTestSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := d.Run(ctx, driver.Request{Config: cfg, Prompt: "work", Session: &driver.SessionContext{EngineSessionID: "thread"}}, sink)
	if err == nil || len(sink.turns()) != 1 {
		t.Fatalf("disconnect err=%v turns=%+v", err, sink.turns())
	}
}
