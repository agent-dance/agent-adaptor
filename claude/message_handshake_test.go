package claude

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
)

func TestMessageInitializeUnavailable(t *testing.T) {
	for _, rejected := range []bool{true, false} {
		t.Run(map[bool]string{true: "rejected", false: "unanswered"}[rejected], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				port := newMessageTestPort(driver.MessageItem{ID: "pending", Text: "guidance"})
				s := newMessageSession(context.Background(), port)
				writes := make(chan []byte, 4)
				s.start(messageTestStdin{writes: writes}, func() {})
				defer s.finish(nil)
				done := make(chan struct{})
				var unavailable, fatal error
				started := time.Now()
				go func() {
					unavailable, fatal = s.initialize(make(chan struct{}))
					close(done)
				}()
				if got := <-writes; string(got) != string(messageInitializeFrame()) {
					t.Fatalf("initialize=%s", got)
				}
				if rejected {
					s.handleResponse(map[string]any{"response": map[string]any{"subtype": "error", "request_id": messageInitializeID, "error": "hooks disabled"}})
				}
				<-done
				wantReason, wantWait := "hooks disabled", time.Duration(0)
				if !rejected {
					wantReason, wantWait = "timed out after 10s", messageInitializeTimeout
				}
				if fatal != nil || unavailable == nil || !strings.Contains(unavailable.Error(), wantReason) || time.Since(started) != wantWait {
					t.Fatalf("unavailable=%v fatal=%v wait=%s", unavailable, fatal, time.Since(started))
				}
				// A late response and registered callback must still be answered,
				// but cannot claim queued input or open the delivery window.
				s.handleResponse(map[string]any{"response": map[string]any{"subtype": "success", "request_id": messageInitializeID}})
				p := newClaudeParser(&messageTestSink{})
				p.messages = s
				for _, event := range []string{"PreToolUse", "Stop"} {
					p.handlePayload("", messageHook(event, "", ""))
					if out := messageHookOutput(t, <-writes); len(out) != 0 {
						t.Fatalf("callback output=%v", out)
					}
				}
				if len(port.windows) != 0 || len(port.claims) != 0 || len(port.acks) != 0 || len(port.pending) != 1 {
					t.Fatalf("degraded hooks used port: %+v", port)
				}
			})
		})
	}
}

func TestMessageInitializeCancellationAndExit(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancelled", false: "process-exit"}[cancelled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := newMessageSession(ctx, newMessageTestPort())
				writes := make(chan []byte, 1)
				s.start(messageTestStdin{writes: writes}, func() {})
				defer s.finish(nil)
				readerDone, done := make(chan struct{}), make(chan struct{})
				var unavailable, fatal error
				started := time.Now()
				go func() { unavailable, fatal = s.initialize(readerDone); close(done) }()
				<-writes
				synctest.Wait()
				if cancelled {
					cancel()
				} else {
					close(readerDone)
				}
				<-done
				if unavailable != nil || fatal == nil || time.Since(started) != 0 {
					t.Fatalf("unavailable=%v fatal=%v wait=%s", unavailable, fatal, time.Since(started))
				}
				if cancelled && !errors.Is(fatal, context.Canceled) {
					t.Fatalf("cancel error=%v", fatal)
				}
				if !cancelled && !strings.Contains(fatal.Error(), "process exited during hooks initialization") {
					t.Fatalf("exit error=%v", fatal)
				}
			})
		})
	}
}

func messageHandshakeDriver(t *testing.T, mode string, hitl bool) configuredDriver {
	t.Helper()
	home := t.TempDir()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []driver.EnvBinding{{Name: messageHelperEnv, Value: "1"}, {Name: "CLAUDE_CONFIG_DIR", Value: home}, {Name: "MESSAGE_MODE", Value: mode}}
	if hitl {
		env = append(env, driver.EnvBinding{Name: "MESSAGE_HITL", Value: "1"})
	}
	d := Driver(Config{CommonConfig: CommonConfig{Command: command, CWD: home, Env: env}}).(configuredDriver)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.CloseProcesses(ctx); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return d
}

func TestMessageInitializeUnansweredRunContinues(t *testing.T) {
	d := messageHandshakeDriver(t, "ignore-init", true)
	port, sink := newMessageTestPort(driver.MessageItem{ID: "pending", Text: "guidance"}), &messageTestSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	response, err := d.Run(ctx, driver.Request{Prompt: "initial", Messages: port, Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAsk}}}, sink)
	if err != nil || response.Failure != nil || response.Output != "unchanged" || response.Checkpoint == nil || !response.Checkpoint.Valid {
		t.Fatalf("response=%+v error=%v", response, err)
	}
	assertMessageHookFallback(t, sink.events, "timed out after 10s")
	if len(port.windows) != 0 || len(port.claims) != 0 || len(port.acks) != 0 || len(port.pending) != 1 || sink.decisions != 1 {
		t.Fatalf("port=%+v decisions=%d", port, sink.decisions)
	}
}

func assertMessageHookFallback(t *testing.T, events []driver.RunEvent, reason string) {
	t.Helper()
	var notices int
	for _, event := range events {
		if event.Type == driver.RunEventLifecycle && event.Metadata["code"] == "claude_hooks_unavailable" {
			notices++
			if !strings.Contains(event.Text, reason) || !strings.Contains(event.Text, "turn end via resume") {
				t.Fatalf("notice=%+v", event)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("fallback notices=%d", notices)
	}
}

// Gate the first Driver call so core admission and resume are tested without
// depending on provider startup speed. Embedding preserves all configured SPI.
type gatedMessageHandshakeDriver struct {
	configuredDriver
	entered, release chan struct{}
	requests         []driver.Request
	ports            []*observedMessageHandshakePort
}

type observedMessageHandshakePort struct {
	driver.MessagePort
	windows []bool
	claims  []driver.Boundary
}

func (p *observedMessageHandshakePort) Window(open bool) {
	p.windows = append(p.windows, open)
	p.MessagePort.Window(open)
}

func (p *observedMessageHandshakePort) Claim(b driver.Boundary) []driver.MessageItem {
	p.claims = append(p.claims, b)
	return p.MessagePort.Claim(b)
}

func (d *gatedMessageHandshakeDriver) Run(ctx context.Context, req driver.Request, sink driver.EventSink) (driver.Response, error) {
	if len(d.requests) == 0 {
		close(d.entered)
		select {
		case <-d.release:
		case <-ctx.Done():
			return driver.Response{}, ctx.Err()
		}
	}
	p := &observedMessageHandshakePort{MessagePort: req.Messages}
	req.Messages = p
	d.requests, d.ports = append(d.requests, req), append(d.ports, p)
	return d.configuredDriver.Run(ctx, req, sink)
}

func TestMessageInitializeRejectedResumesInCore(t *testing.T) {
	for _, resident := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-run", true: "resident"}[resident], func(t *testing.T) {
			d := &gatedMessageHandshakeDriver{configuredDriver: messageHandshakeDriver(t, "reject-init", false), entered: make(chan struct{}), release: make(chan struct{})}
			a := adaptor.New(d)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			defer a.Close(context.Background())
			var runner adaptor.Runner = a
			if resident {
				runner = a.Thread("fallback")
			}
			stream := runner.Send(ctx, "initial")
			select {
			case <-d.entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := stream.Send(ctx, "guidance", adaptor.MessageID("supplement")); err != nil {
				t.Fatal(err)
			}
			close(d.release)
			var notices, turns int
			for event := range stream.Events() {
				switch e := event.(type) {
				case adaptor.Notice:
					if e.Metadata["code"] == "claude_hooks_unavailable" {
						notices++
					}
				case adaptor.TurnEnd:
					turns++
				}
			}
			result, err := stream.Result()
			if err != nil || result == nil || result.Text != "unchanged" {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if len(d.requests) != 2 || turns != 2 || notices != 2 {
				t.Fatalf("calls=%d turns=%d notices=%d", len(d.requests), turns, notices)
			}
			resume := d.requests[1]
			if resume.Session == nil || resume.Session.State == nil || resume.Session.State.ResumeID != "messages-session" || !strings.Contains(resume.Prompt, "guidance") {
				t.Fatalf("resume=%+v", resume)
			}
			for _, port := range d.ports {
				if len(port.windows) != 0 || len(port.claims) != 0 {
					t.Fatalf("degraded port=%+v", port)
				}
			}
			var delivered bool
			for _, outcome := range result.Messages() {
				if outcome.ID == "supplement" {
					delivered = outcome.State == adaptor.MessageDelivered && outcome.Mechanism == "core.resume" && outcome.Boundary == "turn_end"
				}
			}
			if !delivered {
				t.Fatalf("messages=%+v", result.Messages())
			}
			wantInitializations := 2
			if resident {
				wantInitializations = 1
			}
			if got := strings.Count(result.Raw().Stdout, `"subtype":"error"`); got != wantInitializations {
				t.Fatalf("initializations=%d want %d raw=%s", got, wantInitializations, result.Raw().Stdout)
			}
		})
	}
}

func TestMessageZeroQueuedPreservesResponseAndHITL(t *testing.T) {
	for _, resident := range []bool{false, true} {
		for _, hitl := range []bool{false, true} {
			t.Run(map[bool]string{false: "per-run", true: "resident"}[resident]+"/"+map[bool]string{false: "plain", true: "hitl"}[hitl], func(t *testing.T) {
				d := messageHandshakeDriver(t, "empty", hitl)
				var baseline driver.Response
				var baselineStreams []driver.StreamPayload
				for _, enabled := range []bool{false, true} {
					sink, port := &messageTestSink{}, newMessageTestPort()
					req := driver.Request{RunID: "empty", Prompt: "initial", Streaming: true, Spawn: !resident}
					if resident {
						req.Session = &driver.SessionContext{EngineSessionID: "record"}
					}
					if hitl {
						req.Policy.HumanDecision.Permission = driver.HumanDecisionAsk
					}
					if enabled {
						req.Messages = port
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					response, err := d.Run(ctx, req, sink)
					cancel()
					if err != nil || response.Failure != nil || response.Output != "unchanged" || response.Checkpoint == nil || !response.Checkpoint.Valid {
						t.Fatalf("enabled=%t response=%+v error=%v", enabled, response, err)
					}
					wantDecisions := 0
					if hitl {
						wantDecisions = 1
					}
					if sink.decisions != wantDecisions || len(port.acks) != 0 {
						t.Fatalf("decisions=%d acks=%v", sink.decisions, port.acks)
					}
					if !enabled {
						baseline, baselineStreams = response, sink.streams
						continue
					}
					if !reflect.DeepEqual(response.Transcript, baseline.Transcript) || !reflect.DeepEqual(response.Checkpoint, baseline.Checkpoint) || !reflect.DeepEqual(response.Usage, baseline.Usage) || !reflect.DeepEqual(response.RawStreams.Terminal, baseline.RawStreams.Terminal) || response.RawStreams.Stderr != baseline.RawStreams.Stderr || response.Summary != baseline.Summary || !reflect.DeepEqual(sink.streams, baselineStreams) {
						t.Fatalf("zero queued messages changed response/HITL:\nresponse=%+v\nbaseline=%+v\nstreams=%+v\nbaseline streams=%+v", response, baseline, sink.streams, baselineStreams)
					}
					var semanticLines []string
					for _, line := range strings.Split(response.RawStreams.Stdout, "\n") {
						if !strings.Contains(line, `"type":"control_response"`) && !strings.Contains(line, `"subtype":"hook_callback"`) {
							semanticLines = append(semanticLines, line)
						}
					}
					if got := strings.Join(semanticLines, "\n"); got != baseline.RawStreams.Stdout {
						t.Fatalf("provider output changed:\n%s\nbaseline:\n%s", got, baseline.RawStreams.Stdout)
					}
				}
			})
		}
	}
}
