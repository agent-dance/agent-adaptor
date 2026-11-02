//go:build codex_live

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/codex/appserver"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
	"github.com/sourcegraph/jsonrpc2"
)

func TestLiveCodexInRunMessages(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		t.Skip("set AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to -tags codex_live")
	}
	command, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI not in PATH")
	}
	nativeHome := os.Getenv("CODEX_HOME")
	if nativeHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		nativeHome = filepath.Join(home, ".codex")
	}
	testHome := t.TempDir()
	for _, name := range []string{"config.toml", "config.json", "auth.json"} {
		raw, err := os.ReadFile(filepath.Join(nativeHome, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(testHome, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := t.TempDir()
	cfg := Config{CommonConfig: CommonConfig{Command: command, CWD: workspace, Env: []driver.EnvBinding{{Name: "CODEX_HOME", Value: testHome}}}, ReasoningEffort: "low"}
	d := Driver(cfg)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.(driver.ProcessLifecycleDriver).CloseProcesses(ctx); err != nil {
			t.Error(err)
		}
	}()
	p := newCodexMessageTestPort()
	p.immediate = []driver.MessageItem{{ID: "steer", Text: "Additional human guidance: change the required reply to exactly STEERED_TOKEN. Reply only STEERED_TOKEN, with no separate acknowledgement."}}
	p.ends = [][]driver.MessageItem{{{ID: "continuation", Text: "Reply exactly CONTINUED_TOKEN and nothing else.", Timing: driver.TimingAfterTurn}}}
	p.signal <- struct{}{}
	sink := &testutil.EventRecorder{}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := d.Run(ctx, driver.Request{RunID: "live-codex-messages", Prompt: "Reply exactly ORIGINAL_TOKEN and nothing else.", Streaming: true, Messages: p,
		Workspace: driver.WorkspaceLease{CWD: workspace}, Policy: driver.RunPolicy{Isolation: driver.IsolationReadOnly, HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}}, sink)
	if err != nil || result.Failure != nil {
		t.Fatalf("Run err=%v failure=%+v", err, result.Failure)
	}
	var turns []*driver.TurnCompleted
	for _, e := range sink.Snapshot() {
		if e.Turn != nil {
			turns = append(turns, e.Turn)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("turns=%d, output=%q", len(turns), result.Output)
	}
	if !strings.Contains(turns[0].Text, "STEERED_TOKEN") || !strings.Contains(turns[1].Text, "CONTINUED_TOKEN") {
		t.Fatalf("steer/continuation outcomes: %q -> %q", turns[0].Text, turns[1].Text)
	}
	if result.Checkpoint == nil || !result.Checkpoint.Valid {
		t.Fatal("checkpoint missing")
	}
	for _, id := range []string{"steer", "continuation"} {
		if a := p.acks[id]; len(a) != 1 || !a[0].Delivered {
			t.Fatalf("Ack %s=%+v", id, a)
		}
	}
	if p.acks["steer"][0].Mechanism != "codex.turn_steer" || p.acks["continuation"][0].Mechanism != "codex.turn_start" {
		t.Fatalf("mechanisms=%+v", p.acks)
	}
	threadID := result.Checkpoint.State.ResumeID
	var terminal struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result.RawStreams.Terminal.JSON, &terminal); err != nil {
		t.Fatal(err)
	}
	t.Logf("thread=%s turns=%q -> %q usage=%+v windows=%v", threadID, turns[0].Text, turns[1].Text, result.Usage, p.windows)
	// Resume the now-idle thread without running another inference, then probe a
	// steer with the completed turn ID to record the provider's real error shape.
	liveProbeEndedTurn(t, ctx, command, testHome, workspace, threadID, terminal.Turn.ID)
}

type liveMessageStream struct {
	in  io.WriteCloser
	dec *json.Decoder
	enc *json.Encoder
	mu  sync.Mutex
}

func (s *liveMessageStream) ReadObject(v any) error { return s.dec.Decode(v) }
func (s *liveMessageStream) WriteObject(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(v)
}
func (s *liveMessageStream) Close() error { return s.in.Close() }
func liveProbeEndedTurn(t *testing.T, ctx context.Context, command, home, workspace, threadID, turnID string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, command, "app-server", "--listen", "stdio://")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := appserver.NewClient(ctx, &liveMessageStream{in: in, dec: json.NewDecoder(out), enc: json.NewEncoder(in)})
	defer func() {
		_ = in.Close()
		_ = client.Close()
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			t.Errorf("probe process: %v", err)
		}
	}()
	if _, err := client.Initialize(ctx, appserver.InitializeParams{ClientInfo: appserver.ClientInfo{Name: "agent-adaptor-live", Version: "v2"}}); err != nil {
		t.Fatal(err)
	}
	if err := client.NotifyInitialized(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ThreadResume(ctx, appserver.ThreadResumeParams{ThreadID: threadID}); err != nil {
		t.Fatal(err)
	}
	_, err = client.TurnSteer(ctx, appserver.TurnSteerParams{ThreadID: threadID, ExpectedTurnID: turnID, Input: []appserver.UserInput{appserver.TextInput("late steer probe")}})
	var rpcErr *jsonrpc2.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("late steer expected provider error, got %v", err)
	}
	t.Logf("late steer error: code=%d message=%q data=%v", rpcErr.Code, rpcErr.Message, rpcErr.Data)
}
