package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/clihelper"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
)

type rpcReply struct {
	Type, ID, Command string
	Success           *bool
	Data              json.RawMessage
	Error             string
}

var errRPCProtocol = errors.New("pi: malformed RPC protocol")

type rpcState struct {
	SessionID           string `json:"sessionId"`
	SessionFile         string `json:"sessionFile"`
	IsStreaming         *bool  `json:"isStreaming"`
	IsCompacting        *bool  `json:"isCompacting"`
	PendingMessageCount *int   `json:"pendingMessageCount"`
}

func decodeRPCState(raw json.RawMessage) (rpcState, error) {
	var state rpcState
	if err := json.Unmarshal(raw, &state); err != nil || state.SessionID == "" || state.IsStreaming == nil || state.IsCompacting == nil || state.PendingMessageCount == nil {
		return state, errors.New("pi: RPC get_state omitted required session fields")
	}
	if *state.IsStreaming || *state.IsCompacting || *state.PendingMessageCount != 0 {
		return state, errors.New("pi: RPC session is not idle")
	}
	return state, nil
}
func (p *rpcProcess) write(ctx context.Context, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() { written <- p.stdin.Write(append(raw, '\n')) }()
	select {
	case err := <-written:
		return err
	case <-ctx.Done():
		p.cancel()
		return context.Cause(ctx)
	case <-p.done:
		return errors.New("pi: RPC process exited during write")
	}
}
func (p *rpcProcess) command(ctx context.Context, kind string, values map[string]any) (string, error) {
	p.nextID++
	id := "adaptor-" + strconv.FormatUint(p.nextID, 10)
	if values == nil {
		values = map[string]any{}
	}
	values["type"], values["id"] = kind, id
	return id, p.write(ctx, values)
}
func (p *rpcProcess) receive(ctx context.Context) ([]byte, rpcReply, error) {
	var frame rpcFrame
	select {
	case frame = <-p.frames:
	case <-ctx.Done():
		return nil, rpcReply{}, context.Cause(ctx)
	case <-p.done:
		// Drain already observed protocol bytes before reporting process death.
		select {
		case frame = <-p.frames:
		default:
			return nil, rpcReply{}, errors.Join(errors.New("pi: RPC process exited"), p.err)
		}
	}
	var reply rpcReply
	if frame.err != nil {
		return frame.raw, reply, frame.err
	}
	if err := json.Unmarshal(frame.raw, &reply); err != nil || reply.Type == "" {
		return frame.raw, reply, errRPCProtocol
	}
	return frame.raw, reply, nil
}

// The pinned official RPC protocol emits the same agent events as JSON mode,
// plus correlated responses and extension UI records. Only the Driver routes
// those envelopes; the shared process helper never interprets them.
func (p *rpcProcess) response(ctx context.Context, req driver.Request, parser *parser, id, command string) (json.RawMessage, error) {
	for {
		raw, reply, err := p.receive(ctx)
		if err != nil {
			return nil, err
		}
		if reply.Type == "response" {
			parser.events.Record(driverutil.UnknownRecord(raw, "response"))
			if reply.ID != id || reply.Command != command || reply.Success == nil {
				return nil, errors.New("pi: mismatched RPC response")
			}
			if !*reply.Success {
				return nil, fmt.Errorf("pi: RPC %s failed: %s", command, reply.Error)
			}
			return reply.Data, nil
		}
		if err := p.outOfBand(ctx, req, parser, raw, reply.Type); err != nil {
			return nil, err
		}
	}
}
func (p *rpcProcess) outOfBand(ctx context.Context, req driver.Request, parser *parser, raw []byte, kind string) error {
	parser.events.Record(driverutil.UnknownRecord(raw, kind))
	switch kind {
	case "extension_ui_request":
		return p.extensionUI(ctx, req, parser.events.Sink, raw)
	case "extension_error":
		return errors.New("pi: extension reported an RPC error")
	case "agent_start", "agent_end", "agent_settled", "message_start", "message_update", "message_end", "tool_execution_start", "tool_execution_end", "tool_execution_update", "turn_start", "turn_end", "session":
		return errors.New("pi: unexpected agent activity outside the current prompt")
	}
	return parser.events.Err
}

func (d configuredDriver) runRPC(ctx context.Context, c Config, req driver.Request, sink driver.EventSink, cwd string, env []driver.EnvBinding, args []string, prompt string, guards map[string]string, bridge *mcpBridge) (response driver.Response, runErr error) {
	p := newParser(sink, req.Streaming)
	stateful := req.Session != nil && req.Session.Mode != driver.SessionStateless
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	pool := d.pool
	if pool == nil {
		pool = newRPCPool()
	}
	var lease *rpcLease
	var proc *rpcProcess
	var earlier driver.RawStreams
	var session rpcState
	reused := false
	promptSent := false
	defer func() {
		if errors.Is(runErr, errRPCProtocol) {
			p.malformed = true
			runErr = nil
		}
		runErr = errors.Join(runErr, context.Cause(ctx), p.events.Err)
		var outcome clihelper.CommandResult
		if proc != nil && !proc.alive() {
			runErr = errors.Join(runErr, errors.New("pi: RPC process exited before managed teardown"))
			outcome.ExitCode, outcome.Signal, outcome.TimedOut = proc.result.ExitCode, proc.result.Signal, proc.result.TimedOut
		}
		response = p.buildResponse(outcome, runErr)
		if runErr == nil {
			response.StructuredOutput, response.Failure = engine.FinalizeStructuredOutput(req.OutputSchema, req.StructuredOutputSource, response.Output, response.StructuredOutput, response.Failure)
		}
		if response.Failure != nil {
			response.Checkpoint = nil
		}
		response.Metadata = map[string]string{"pi.rpc_reused": strconv.FormatBool(reused)}
		if lease != nil {
			defer lease.release()
			keep := lease.persistent && runErr == nil && response.Checkpoint != nil
			if keep {
				if err := lease.register(response.Checkpoint.State.ResumeID); err != nil {
					runErr = errors.Join(runErr, err)
					keep = false
				}
			}
			if !keep {
				runErr = errors.Join(runErr, lease.stop(context.Background()))
			}
			if proc != nil {
				proc.extension.bind(nil)
				raw, err := proc.output.end()
				runErr = errors.Join(runErr, err)
				earlier.Stdout += raw.Stdout
				earlier.Stderr += raw.Stderr
				response.Metadata["pi.rpc_pid"] = fmt.Sprint(proc.output.processID())
				if !proc.alive() {
					response.Metadata["pi.rpc_exit_code"] = strconv.Itoa(proc.result.ExitCode)
					response.Metadata["pi.rpc_signal"] = proc.result.Signal
					runErr = errors.Join(runErr, proc.err)
				}
			}
		}
		response.RawStreams.Stdout, response.RawStreams.Stderr = earlier.Stdout, earlier.Stderr
		p.complete(&response, runErr)
		runErr = errors.Join(runErr, p.events.Err)
		if runErr != nil && lease != nil && proc != nil && proc.alive() {
			runErr = errors.Join(runErr, lease.stop(context.Background()))
			raw, _ := proc.output.end()
			response.RawStreams.Stdout += raw.Stdout
			response.RawStreams.Stderr += raw.Stderr
			response.Checkpoint = nil
		}
		if response.Checkpoint != nil {
			response.Checkpoint.State.Data = guards
			if !stateful {
				response.Checkpoint = nil
			}
		}
		if response.Provider == "" {
			response.Provider = c.Provider
		}
		if response.Model == "" {
			response.Model = c.Model
		}
		if stateful && req.Session.State != nil && !promptSent && strings.Contains(response.RawStreams.Stderr, "No session found matching '") {
			runErr = &engine.ResumeRejectedError{Reason: "Pi session is unavailable"}
		}
	}()
	var source string
	var err error
	if bridge != nil {
		source, err = bridge.extensionSource("url", "token")
		if err != nil {
			return response, err
		}
	}
	signature, err := rpcSignature(c, args, cwd, env, guards, source)
	if err != nil {
		return response, err
	}
	lease, err = pool.acquire(ctx, req)
	if err != nil {
		return response, err
	}
	pool.mu.Lock()
	proc, oldSignature := lease.slot.process, lease.slot.signature
	pool.mu.Unlock()
	if proc != nil && (!lease.persistent || (stateful && req.Session.PreviousID != "") || signature != oldSignature || !proc.alive()) {
		if err := lease.stop(context.Background()); err != nil {
			return response, err
		}
		earlier, _ = proc.output.end()
		proc = nil
	}
	if proc != nil {
		reused = true
		proc.uiRequests = nil
		if err := proc.output.begin(sink); err != nil {
			return response, err
		}
		proc.extension.bind(bridge)
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventInvocation, Text: "reusing private Pi RPC process", Data: map[string]any{"pid": proc.output.processID(), "cwd": cwd, "reused": true}}); err != nil {
			return response, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if proc == nil {
			reused = false
			startArgs := append([]string(nil), args...)
			if stateful && req.Session.State != nil && req.Session.State.ResumeID != "" {
				flag := "--session"
				if req.Session.Mode == driver.SessionFork {
					flag = "--fork"
				}
				startArgs = append(startArgs, flag, req.Session.State.ResumeID)
			}
			proc, err = lease.start(c, startArgs, cwd, env, bridge, sink)
		}
		if err == nil {
			readyCtx, readyCancel := context.WithTimeout(ctx, 45*time.Second)
			var id string
			var raw json.RawMessage
			id, err = proc.command(readyCtx, "get_state", nil)
			if err == nil {
				raw, err = proc.response(readyCtx, req, p, id, "get_state")
			}
			if err == nil {
				session, err = decodeRPCState(raw)
			}
			readyCancel()
		}
		if err == nil {
			break
		}
		if stopErr := lease.stop(context.Background()); stopErr != nil {
			return response, errors.Join(err, stopErr)
		}
		if proc != nil {
			raw, _ := proc.output.end()
			earlier.Stdout += raw.Stdout
			earlier.Stderr += raw.Stderr
		}
		proc = nil
		if attempt == 1 || ctx.Err() != nil || pool.isClosed() {
			return response, err
		}
	}
	lease.slot.signature = signature
	if stateful && req.Session.State != nil && req.Session.State.ResumeID != "" {
		previous := req.Session.State
		if req.Session.Mode == driver.SessionFork && session.SessionID == previous.ResumeID {
			return response, errors.New("pi: RPC fork returned its parent session")
		}
		if req.Session.Mode != driver.SessionFork && session.SessionID != previous.ResumeID {
			return response, errors.New("pi: RPC resumed a different session")
		}
	}
	p.sessionID = session.SessionID
	p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptInit, SessionID: session.SessionID})
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-proc.done:
			cancel(errors.New("pi: RPC process exited during invocation"))
		case <-pool.closed:
			cancel(errRPCPoolClosed)
		case <-watchDone:
		}
	}()
	// No error after this write permits automatic prompt replay, including a
	// negative acknowledgement or a write whose delivery status is uncertain.
	promptSent = true
	id, err := proc.command(ctx, "prompt", map[string]any{"message": prompt})
	if err != nil {
		return response, err
	}
	accepted := false
	for !p.settled || !accepted {
		raw, reply, err := proc.receive(ctx)
		if err != nil {
			return response, err
		}
		switch reply.Type {
		case "response":
			p.events.Record(driverutil.UnknownRecord(raw, "response"))
			if accepted || reply.ID != id || reply.Command != "prompt" || reply.Success == nil {
				return response, errors.New("pi: unexpected prompt acknowledgement")
			}
			if !*reply.Success {
				return response, fmt.Errorf("pi: prompt rejected: %s", reply.Error)
			}
			accepted = true
		case "extension_ui_request", "extension_error":
			if err := proc.outOfBand(ctx, req, p, raw, reply.Type); err != nil {
				return response, err
			}
		default:
			p.line(raw)
			if p.malformed {
				return response, errRPCProtocol
			}
		}
		if p.events.Err != nil {
			return response, p.events.Err
		}
	}
	id, err = proc.command(ctx, "get_state", nil)
	if err != nil {
		return response, err
	}
	raw, err := proc.response(ctx, req, p, id, "get_state")
	if err != nil {
		return response, err
	}
	final, err := decodeRPCState(raw)
	if err != nil {
		return response, err
	}
	if final.SessionID != session.SessionID || (stateful && final.SessionFile == "") {
		return response, errors.New("pi: RPC terminal session is not resumable")
	}
	return response, nil
}
