package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
)

// The two messages intentionally repeat cumulative deltas and an assistant
// snapshot. A message contributes once, while distinct messages are summed.
func alignmentClaudeUsageFrames() string {
	var raw strings.Builder
	for i := 1; i <= 2; i++ {
		fmt.Fprintf(&raw, `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_%d","usage":{"input_tokens":%d,"cache_read_input_tokens":%d}}}}`+"\n", i, 10*i, 2*i)
		for _, output := range []int{2 * i, 5 * i, 5 * i} {
			fmt.Fprintf(&raw, `{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":%d}}}`+"\n", output)
		}
		raw.WriteString(`{"type":"stream_event","event":{"type":"message_stop"}}` + "\n")
		for j := 0; j < 2; j++ {
			fmt.Fprintf(&raw, `{"type":"assistant","message":{"id":"msg_%d","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d},"content":[{"type":"text","text":"message %d"}]}}`+"\n", i, 10*i, 5*i, 2*i, i)
		}
	}
	return raw.String()
}

type alignmentClaudePartialWriter struct {
	io.WriteCloser
	lp    *liveProcess
	cause error
}

func (w alignmentClaudePartialWriter) Write(frame []byte) (int, error) {
	n, err := w.WriteCloser.Write(frame[:1])
	if err != nil {
		return n, err
	}
	// The child has consumed the byte and put its output into real OS pipes.
	// Only this test reader is active until Write returns to the turn reader.
	watchdog := time.AfterFunc(3*time.Second, w.lp.signalTerminate)
	defer watchdog.Stop()
	if _, err := w.lp.stdout.Peek(len(alignmentClaudePartial) + 1); err != nil {
		return n, err
	}
	return n, w.cause
}

func TestAlignmentClaudePartialWriteDrain(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("short_%t", short), func(t *testing.T) {
			for _, streamed := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream_%t", streamed), func(t *testing.T) {
					f := newAlignmentClaudeFixture(t, true)
					f.cfg.Env = append(f.cfg.Env, driver.EnvBinding{Name: "ALIGNMENT_PARTIAL_WRITE", Value: "1"})
					cause := errors.New("partial stdin failure")
					expected := cause
					if short {
						cause = nil
						expected = io.ErrShortWrite
					}
					var observed *liveProcess
					f.pool.spawnProcess = func(spec persistentSpec, sink driver.EventSink) (*liveProcess, error) {
						lp, err := f.pool.spawn(spec, sink)
						if err == nil {
							observed = lp
							lp.stdin = alignmentClaudePartialWriter{WriteCloser: lp.stdin, lp: lp, cause: cause}
						}
						return lp, err
					}
					a := f.agent(adaptor.WithThreadStore(memory.NewStore()))
					defer a.Close(context.Background())
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					th := a.Thread("partial-write")
					var result *adaptor.Result
					var err error
					if streamed {
						stream := th.Stream(ctx, "prompt")
						for range stream.Events() {
						}
						result, err = stream.Result()
					} else {
						result, err = th.Run(ctx, "prompt")
					}
					var runErr *adaptor.RunError
					if result != nil || !errors.As(err, &runErr) || runErr.Reason != adaptor.ReasonInfrastructure || !errors.Is(err, expected) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
						t.Fatalf("wrong write failure attribution: result=%#v error=%#v", result, err)
					}
					raw := runErr.Result.Raw()
					found := false
					for _, item := range runErr.Result.Transcript() {
						if item.Kind == "stderr" && item.Text == "observed diagnostic without newline" {
							found = true
						}
					}
					if !found || raw.Stdout != alignmentClaudePartial+"\n" || raw.Stderr != "observed diagnostic without newline" || runErr.Result.Text != "" {
						t.Fatalf("write failure discarded pipe output: raw=%#v transcript=%#v", raw, runErr.Result.Transcript())
					}
					if observed == nil || len(f.lines(t, "SPAWN_FILE")) != 1 || f.pool.lookup("session-c04") != nil {
						t.Fatal("partly delivered prompt was replayed or reused")
					}
					if checkpoint, err := th.Checkpoint(context.Background()); err == nil && checkpoint != nil {
						t.Fatalf("short write generated checkpoint: %#v", checkpoint)
					}
				})
			}
		})
	}
}

func TestAlignmentClaudeObservedUsageAndExit(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprintf("terminal_zero_%t", terminal), func(t *testing.T) {
			var prior *adaptor.Result
			for _, streamed := range []bool{false, true} {
				f := newAlignmentClaudeFixture(t, true)
				a := f.agent(adaptor.WithThreadStore(memory.NewStore()))
				defer a.Close(context.Background())
				th := a.Thread("observed-usage")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := th.Run(ctx, "warm"); err != nil {
					t.Fatal(err)
				}
				before, err := th.Checkpoint(ctx)
				if err != nil {
					t.Fatal(err)
				}
				prompt := "multiple-usage"
				if terminal {
					prompt += " terminal-zero"
				}
				var result *adaptor.Result
				if streamed {
					stream := th.Stream(ctx, prompt)
					for range stream.Events() {
					}
					result, err = stream.Result()
				} else {
					result, err = th.Run(ctx, prompt)
				}
				if !terminal {
					var runErr *adaptor.RunError
					var exitErr *exec.ExitError
					if result != nil || !errors.As(err, &runErr) || runErr.Reason != adaptor.ReasonAgentError || !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 || !errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
						t.Fatalf("actual provider exit/cause lost or became Cancel: %#v", err)
					}
					result = runErr.Result
					after, checkpointErr := th.Checkpoint(ctx)
					if checkpointErr != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("failed turn altered checkpoint: %#v %v", after, checkpointErr)
					}
					if len(f.lines(t, "SPAWN_FILE")) != 1 || f.pool.lookup("session-c04") != nil {
						t.Fatal("failed provider turn replayed or reused")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				want := driver.Usage{InputTokens: 30, OutputTokens: 15, CachedInputTokens: 6}
				if terminal {
					want = driver.Usage{}
				}
				if result.Usage == nil || result.Usage.InputTokens != want.InputTokens || result.Usage.OutputTokens != want.OutputTokens || result.Usage.CachedInputTokens != want.CachedInputTokens {
					t.Fatalf("wrong observed usage: %#v want %#v", result.Usage, want)
				}
				if !strings.Contains(result.Raw().Stdout, alignmentClaudeUsageFrames()) {
					t.Fatal("usage protocol missing from Raw")
				}
				if terminal != (result.Raw().Terminal != nil) {
					t.Fatal("terminal presence changed")
				}
				if prior != nil && (!reflect.DeepEqual(prior.Usage, result.Usage) || !reflect.DeepEqual(prior.Raw(), result.Raw()) || !reflect.DeepEqual(prior.Transcript(), result.Transcript()) || prior.Text != result.Text) {
					t.Fatal("Run and Stream partial results differ")
				}
				prior = result
			}
		})
	}
}

func TestAlignmentClaudeInterleavedMessageUsage(t *testing.T) {
	p := newClaudeParser(&streamSink{})
	p.enableStreaming("usage")
	raw := `{"type":"stream_event","event":{"type":"message_start","message":{"id":"root","usage":{"input_tokens":10}}}}
{"type":"stream_event","parent_tool_use_id":"child","event":{"type":"message_start","message":{"id":"nested","usage":{"input_tokens":20}}}}
{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":5}}}
{"type":"stream_event","parent_tool_use_id":"child","event":{"type":"message_delta","usage":{"output_tokens":10}}}
{"type":"assistant","message":{"id":"root","usage":{"input_tokens":10,"output_tokens":5},"content":[]}}
{"type":"assistant","parent_tool_use_id":"child","message":{"id":"nested","usage":{"input_tokens":20,"output_tokens":10},"content":[]}}
`
	if err := p.onChunk("stdout", []byte(raw), time.Now()); err != nil {
		t.Fatal(err)
	}
	p.finalize()
	got := p.observedUsage()
	if got == nil || got.InputTokens != 30 || got.OutputTokens != 15 {
		t.Fatalf("interleaved root/nested usage attribution: %#v", got)
	}
}

func TestAlignmentClaudeUsageObservation(t *testing.T) {
	frames := map[string]string{
		"start":     `{"type":"stream_event","event":{"type":"message_start","message":{"id":"observed","usage":%s}}}`,
		"delta":     `{"type":"stream_event","event":{"type":"message_delta","usage":%s}}`,
		"assistant": `{"type":"assistant","message":{"id":"observed","usage":%s,"content":[]}}`,
		"terminal":  `{"type":"result","subtype":"success","is_error":false,"session_id":"session-c04","result":"done","usage":%s}`,
	}
	for _, tc := range []struct {
		name, usage string
		want        *driver.Usage
	}{
		{"empty", `{}`, nil},
		{"unknown_field", `{"unknown":0}`, nil},
		{"string", `{"input_tokens":"unknown","output_tokens":"0","cache_read_input_tokens":"1"}`, nil},
		{"negative", `{"input_tokens":-2,"output_tokens":-2,"cache_read_input_tokens":-2}`, nil},
		{"fractional", `{"input_tokens":0.5,"output_tokens":0.5,"cache_read_input_tokens":0.5}`, nil},
		{"out_of_range", `{"input_tokens":1e40,"output_tokens":1e40,"cache_read_input_tokens":1e40}`, nil},
		{"null_bool", `{"input_tokens":null,"output_tokens":false,"cache_read_input_tokens":true}`, nil},
		{"input_zero", `{"input_tokens":0}`, &driver.Usage{}},
		{"output_zero", `{"output_tokens":0}`, &driver.Usage{}},
		{"cached_zero", `{"cache_read_input_tokens":0}`, &driver.Usage{}},
		{"positive", `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":2}`, &driver.Usage{InputTokens: 10, OutputTokens: 5, CachedInputTokens: 2}},
		{"valid_with_invalid", `{"input_tokens":2,"output_tokens":-1,"cache_read_input_tokens":"unknown"}`, &driver.Usage{InputTokens: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, frame := range frames {
				t.Run(name, func(t *testing.T) {
					p := newClaudeParser(&streamSink{})
					p.enableStreaming("usage-observation")
					raw := fmt.Sprintf(frame, tc.usage) + "\n"
					if err := p.onChunk("stdout", []byte(raw), time.Now()); err != nil {
						t.Fatal(err)
					}
					p.finalize()
					response, _ := buildClaudeResponse(driver.Request{}, p, driver.RawStreams{Stdout: raw}, -1, "", false, "", "", "", io.EOF)
					if !reflect.DeepEqual(response.Usage, tc.want) {
						t.Fatalf("wire usage %s: got %#v want %#v", tc.usage, response.Usage, tc.want)
					}
				})
			}
		})
	}
}

func TestAlignmentClaudeTerminalUsageAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        driver.Usage
	}{
		{"invalid_falls_back", `{"input_tokens":0.5,"output_tokens":-2,"cache_read_input_tokens":"unknown"}`, driver.Usage{InputTokens: 30, OutputTokens: 15, CachedInputTokens: 6}},
		{"explicit_zero_overrides", `{"input_tokens":0}`, driver.Usage{}},
		{"valid_overrides", `{"input_tokens":42,"output_tokens":8,"cache_read_input_tokens":3}`, driver.Usage{InputTokens: 42, OutputTokens: 8, CachedInputTokens: 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newClaudeParser(&streamSink{})
			p.enableStreaming("terminal-usage")
			raw := alignmentClaudeUsageFrames() + fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"session_id":"session-c04","result":"done","usage":%s}`, tc.usage) + "\n"
			if err := p.onChunk("stdout", []byte(raw), time.Now()); err != nil {
				t.Fatal(err)
			}
			p.finalize()
			got := p.observedUsage()
			if got == nil || !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("terminal authority: got %#v want %#v", got, tc.want)
			}
		})
	}
}
