package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/driverutil"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

// serverClient owns one private authenticated loopback server. HTTP redirects
// and automatic retries are disabled: once prompt delivery is possible, the
// invocation must never be replayed. SSE is a provider transport, not a second
// public SDK event stream.
type serverClient struct {
	base, password, cwd string
	http                *http.Client
}

type serverHTTPError struct {
	status int
	body   string
}

func (e *serverHTTPError) Error() string {
	return fmt.Sprintf("opencode server HTTP %d: %s", e.status, e.body)
}

func (c *serverClient) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		input = bytes.NewReader(raw)
	}
	u := c.base + path + "?directory=" + url.QueryEscape(c.cwd)
	req, err := http.NewRequestWithContext(ctx, method, u, input)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("opencode", c.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, &serverHTTPError{resp.StatusCode, string(raw)}
	}
	return resp, nil
}
func (c *serverClient) call(ctx context.Context, method, path string, body any) ([]byte, error) {
	r, err := c.request(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

type lockedBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedBuffer) text() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }

type sseFrame struct {
	data []byte
	err  error
}

// readSSE implements the provider's data-event framing, including CRLF,
// comments and multiline data. It does not reconnect or interpret JSON.
func readSSE(ctx context.Context, input io.Reader, out chan<- sseFrame) {
	defer close(out)
	send := func(frame sseFrame) bool {
		select {
		case out <- frame:
			return true
		case <-ctx.Done():
			return false
		}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 32<<20)
	var data []byte
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 && !send(sseFrame{data: bytes.TrimSuffix(data, []byte{'\n'})}) {
				return
			}
			data = nil
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			value = strings.TrimPrefix(value, " ")
			data = append(data, value...)
			data = append(data, '\n')
			if len(data) > 32<<20 {
				send(sseFrame{err: errors.New("opencode: SSE frame exceeds 32 MiB")})
				return
			}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	send(sseFrame{err: err})
}

func wantsServer(req driver.Request) bool {
	if len(req.MCP.Servers) > 0 || req.ProfilePayload.Declared.MCP {
		return true
	}
	p := req.Policy.HumanDecision
	return replacementInstructions(req) || (req.Session != nil && req.Session.Mode != driver.SessionStateless && req.Session.EngineSessionID != "") || req.Streaming || req.StructuredOutputSource == driver.StructuredOutputSourceNative ||
		p.Permission == driver.HumanDecisionAsk || p.Permission == driver.HumanDecisionAutoReject ||
		p.PlanReview != driver.HumanDecisionUnset || p.Question != driver.QuestionUnset
}

func (d configuredDriver) runServer(ctx context.Context, c Config, req driver.Request, sink driver.EventSink, cwd string, env []driver.EnvBinding, guards map[string]string, disabledMCP []string, agents agentInvocation) (response driver.Response, runErr error) {
	p := newServerParser(sink, req.Streaming)
	p.events.Stream(driver.StreamPayload{Kind: driver.StreamRunStarted, RunID: req.RunID})
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var eventRaw lockedBuffer
	var terminal []byte
	var lease *serverLease
	var proc *serverProcess
	var earlier driver.RawStreams
	var permissionBase string
	reused := false
	defer func() {
		runErr = errors.Join(runErr, context.Cause(ctx), p.events.Err)
		for _, child := range p.delegated {
			if incomplete := child.parser.closeOpenParts(); incomplete != "" && child.active && runErr == nil {
				p.protocolError = "OpenCode ended with an active incomplete delegated session"
			}
		}
		if proc != nil && !proc.alive() {
			runErr = errors.Join(runErr, errors.New("opencode: server exited before managed teardown"))
			response.ExitCode, response.Signal, response.TimedOut = proc.result.ExitCode, proc.result.Signal, proc.result.TimedOut
		}
		response = p.buildResponse(response, terminal, runErr, req)
		runErr = errors.Join(runErr, p.events.Err)
		response.RawStreams.Stdout = earlier.Stdout
		response.RawStreams.Stderr = earlier.Stderr
		response.Metadata = map[string]string{"opencode.server_reused": strconv.FormatBool(reused)}
		if lease != nil {
			defer lease.release()
			keep := lease.persistent && runErr == nil && response.Checkpoint != nil && response.Checkpoint.Valid
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
				raw, err := proc.output.end()
				runErr = errors.Join(runErr, err)
				response.RawStreams.Stdout = earlier.Stdout + raw.Stdout
				response.RawStreams.Stderr = earlier.Stderr + raw.Stderr
				response.Metadata["opencode.server_pid"] = fmt.Sprint(proc.output.processID())
				if !proc.alive() {
					response.Metadata["opencode.server_exit_code"] = strconv.Itoa(proc.result.ExitCode)
					response.Metadata["opencode.server_signal"] = proc.result.Signal
					runErr = errors.Join(runErr, proc.err)
				}
			}
		}
		p.complete(&response, runErr)
		runErr = errors.Join(runErr, p.events.Err)
		if runErr != nil && lease != nil && proc != nil && proc.alive() {
			runErr = errors.Join(runErr, lease.stop(context.Background()))
			raw, _ := proc.output.end()
			response.RawStreams.Stdout += raw.Stdout
			response.RawStreams.Stderr += raw.Stderr
			response.Checkpoint = nil
		}
		response.Metadata["opencode.event_stream"] = eventRaw.text()
		if response.Checkpoint != nil {
			response.Checkpoint.State.Data = guards
			response.Checkpoint.State.Data["permission_base"] = permissionBase
		}
	}()
	if c.Pure {
		env = skillruntime.WithBinding(env, "OPENCODE_PURE", "true")
	}
	if req.Policy.HumanDecision.Question != driver.QuestionUnset {
		env = skillruntime.WithBinding(env, "OPENCODE_ENABLE_QUESTION_TOOL", "true")
	}
	if req.Policy.HumanDecision.PlanReview != driver.HumanDecisionUnset {
		env = skillruntime.WithBinding(env, "OPENCODE_EXPERIMENTAL_PLAN_MODE", "true")
	}
	pool := d.pool
	if pool == nil {
		pool = newServerPool()
	}
	signature, err := serverSignature(c, cwd, env, guards)
	if err != nil {
		return response, err
	}
	// File contents are captured before startup, including when a host supplied
	// its own resource fingerprint. A changed replacement must never reuse the
	// previous process's prompt overlay.
	var replacement *string
	if replacementInstructions(req) {
		replacement = &req.Instructions.Content
	}
	gatePermissions := req.Policy.HumanDecision.Permission == driver.HumanDecisionAsk || req.Policy.HumanDecision.Permission == driver.HumanDecisionAutoReject
	mcpEntries, err := runtimeMCPEntries(req.MCP, env)
	if err != nil {
		return response, err
	}
	signature, err = driver.CanonicalSessionConfigFingerprint("opencode/server-resources/v1", struct {
		Base            string
		Replacement     *string
		MCP             map[string]map[string]any
		GatePermissions bool
	}{signature, replacement, mcpEntries, gatePermissions})
	if err != nil {
		return response, err
	}
	lease, err = pool.acquire(ctx, req)
	if err != nil {
		return response, err
	}
	pool.mu.Lock()
	proc = lease.slot.process
	oldSignature := lease.slot.signature
	pool.mu.Unlock()
	force := !lease.persistent || (req.Session != nil && req.Session.PreviousID != "")
	if proc != nil && (force || signature != oldSignature || !proc.alive()) {
		if err := lease.stop(context.Background()); err != nil {
			return response, err
		}
		raw, _ := proc.output.end()
		earlier = raw
		proc = nil
	}
	if proc != nil {
		reused = true
		if err := proc.output.begin(sink); err != nil {
			return response, err
		}
		if err := sink.Emit(driver.RunEvent{Type: driver.RunEventInvocation, Text: "reusing private OpenCode server", Data: map[string]any{"pid": proc.output.processID(), "cwd": cwd, "reused": true}}); err != nil {
			return response, err
		}
	}
	// Only startup/health can retry, and at most once. No session prompt has
	// been sent at this point. Later HTTP errors never enter this loop.
	for attempt := 0; attempt < 2; attempt++ {
		if proc == nil {
			reused = false
			proc, err = lease.start(ctx, c, cwd, env, sink)
		}
		if err == nil {
			healthCtx, cancelHealth := context.WithTimeout(ctx, 5*time.Second)
			_, err = proc.client.call(healthCtx, "GET", "/global/health", nil)
			cancelHealth()
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
	if !reused {
		if err := agents.verify(ctx, proc.client); err != nil {
			return response, err
		}
	}
	if (replacementInstructions(req) || gatePermissions) && !reused {
		// Read resolved native agent state before delivering any prompt. Restart
		// with process-local prompt/permission overlays, then verify native
		// delivery and preservation of other fields. No shared config is written.
		plan, err := proc.client.nativeAgentOverlay(ctx, c.Agent, replacement, gatePermissions, filepath.Join(guards["data_home"], "opencode", "tool-output", "*"), env)
		if err != nil {
			return response, err
		}
		if err := lease.stop(context.Background()); err != nil {
			return response, err
		}
		raw, outputErr := proc.output.end()
		earlier.Stdout += raw.Stdout
		earlier.Stderr += raw.Stderr
		if outputErr != nil {
			return response, outputErr
		}
		proc, err = lease.start(ctx, c, cwd, plan.env, sink)
		if err != nil {
			return response, err
		}
		if err := plan.verify(ctx, proc.client); err != nil {
			return response, err
		}
	}
	lease.slot.signature = signature
	if gatePermissions {
		probeRaw, err := proc.verifySavedPermissions(ctx, c, env, sink)
		earlier.Stdout += probeRaw.Stdout
		earlier.Stderr += probeRaw.Stderr
		if err != nil {
			return response, err
		}
	}
	client := proc.client
	if err := client.validateMCPStartup(ctx, req.MCP, disabledMCP); err != nil {
		return response, err
	}
	if !reused {
		if err := client.installMCP(ctx, mcpEntries); err != nil {
			return response, err
		}
	}
	if err := client.requireMCP(ctx, req.MCP, sink); err != nil {
		return response, err
	}
	// A process death cancels in-flight HTTP and approval waits immediately.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-proc.done:
			cancel(errors.New("opencode: private server exited during invocation"))
		case <-pool.closed:
			cancel(errServerPoolClosed)
		case <-watchDone:
		}
	}()
	var session struct {
		ID         string           `json:"id"`
		Permission []permissionRule `json:"permission"`
	}
	path, method, body := "/session", "POST", any(map[string]any{})
	stateful := req.Session != nil && req.Session.Mode != driver.SessionStateless
	if stateful && req.Session.State != nil && req.Session.State.ResumeID != "" {
		path, method, body = "/session/"+url.PathEscape(req.Session.State.ResumeID), "GET", nil
	}
	raw, err := client.call(ctx, method, path, body)
	if err != nil {
		var status *serverHTTPError
		if method == "GET" && errors.As(err, &status) && status.status == 404 {
			return response, &engine.ResumeRejectedError{Reason: "OpenCode session is unavailable"}
		}
		return response, err
	}
	if err := json.Unmarshal(raw, &session); err != nil || session.ID == "" {
		return response, errors.New("opencode: missing explicit session identifier")
	}
	if method == "GET" && session.ID != req.Session.State.ResumeID {
		return response, errors.New("opencode: resumed a different session")
	}
	if stateful && req.Session.Mode == driver.SessionFork {
		parent := session.ID
		raw, err = client.call(ctx, "POST", "/session/"+url.PathEscape(parent)+"/fork", map[string]any{})
		if err != nil {
			return response, err
		}
		if err := json.Unmarshal(raw, &session); err != nil || session.ID == "" || session.ID == parent {
			return response, errors.New("opencode: fork did not return a distinct session")
		}
	}
	p.sessionID = session.ID
	base := append([]permissionRule(nil), session.Permission...)
	if stateful && req.Session.State != nil {
		if encoded, ok := req.Session.State.Data["permission_base"]; ok {
			if err := json.Unmarshal([]byte(encoded), &base); err != nil {
				return response, errors.New("opencode: invalid native permission checkpoint")
			}
		}
	}
	if base == nil {
		base = []permissionRule{}
	}
	for _, rule := range base {
		if rule.Permission == "" || rule.Pattern == "" || (rule.Action != "allow" && rule.Action != "ask" && rule.Action != "deny") {
			return response, errors.New("opencode: invalid native permission rule")
		}
	}
	encodedBase, _ := json.Marshal(base)
	permissionBase = string(encodedBase)
	if err := client.configureSessionPermissions(ctx, c.Agent, session.ID, base, session.Permission, gatePermissions); err != nil {
		return response, err
	}
	p.events.Record(driver.TranscriptItem{Kind: driver.TranscriptInit, SessionID: session.ID})
	// Subscribe and consume the official connected event before submitting any
	// prompt. An established HTTP response alone is not a subscription barrier.
	stream, err := client.request(ctx, "GET", "/event", nil)
	if err != nil {
		return response, err
	}
	streamCtx, endStream := context.WithCancel(ctx)
	frames := make(chan sseFrame)
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); readSSE(streamCtx, io.TeeReader(stream.Body, &eventRaw), frames) }()
	defer func() { endStream(); stream.Body.Close(); <-readerDone }()
	select {
	case frame := <-frames:
		if frame.err != nil {
			return response, frame.err
		}
		var connected serverEvent
		if json.Unmarshal(frame.data, &connected) != nil || connected.Type != "server.connected" {
			return response, errors.New("opencode: missing SSE connection barrier")
		}
	case <-ctx.Done():
		return response, context.Cause(ctx)
	}
	prompt := req.Prompt
	if prefix := driverutil.RuntimePromptPrefix(req.Runtime); prefix != "" {
		prompt = prefix + "\n\n" + prompt
	}
	input := map[string]any{"parts": []any{map[string]any{"type": "text", "text": prompt}}}
	if req.Instructions != nil && !replacementInstructions(req) {
		instruction := req.Instructions.Content
		input["system"] = instruction
	}
	if c.Model != "" {
		provider, model, _ := strings.Cut(c.Model, "/")
		input["model"] = map[string]string{"providerID": provider, "modelID": model}
	}
	if c.Agent != "" {
		input["agent"] = c.Agent
	}
	if c.Variant != "" {
		input["variant"] = c.Variant
	}
	if req.OutputSchema != nil && req.StructuredOutputSource == driver.StructuredOutputSourceNative {
		input["format"] = map[string]any{"type": "json_schema", "schema": req.OutputSchema.SchemaJSON}
	}
	if _, err := client.call(ctx, "POST", "/session/"+url.PathEscape(session.ID)+"/prompt_async", input); err != nil {
		return response, err
	}
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return response, io.ErrUnexpectedEOF
			}
			if frame.err != nil {
				return response, frame.err
			}
			target, e, err := p.consumeDelegated(ctx, client, frame.data)
			if err != nil {
				p.protocolError = err.Error()
				return response, nil
			}
			if p.events.Err != nil {
				return response, p.events.Err
			}
			if e.Type == "permission.asked" || e.Type == "question.asked" {
				if err := target.decide(ctx, client, req, e); err != nil {
					return response, err
				}
			}
			if p.providerError != "" {
				return response, nil
			}
			if p.idle {
				if p.lastMessage == "" {
					p.protocolError = "OpenCode became idle without an assistant message"
					return response, nil
				}
				terminal, err = client.call(ctx, "GET", "/session/"+url.PathEscape(session.ID)+"/message/"+url.PathEscape(p.lastMessage), nil)
				return response, err
			}
		case <-ctx.Done():
			return response, context.Cause(ctx)
		}
	}
}
