package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/sourcegraph/jsonrpc2"
)

const approvalShutdownGrace = 250 * time.Millisecond

// handleRunRequests binds requests to this execution's lifetime. Unbinding
// excludes new requests before waiting, so neither the sink nor RPC connection
// is used by a decision goroutine after the run returns.
func (c *Client) handleRunRequests(ctx context.Context, runID string, sink driver.EventSink, state *runState) func() {
	requestCtx, cancel := context.WithCancel(ctx)
	var pending sync.WaitGroup
	var stop sync.Once
	c.handlerMu.Lock()
	c.requestHandler = func(conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
		pending.Add(1)
		go func() {
			defer pending.Done()
			result, rpcErr, decisionErr := approvalResponse(requestCtx, runID, sink, req, state)
			if ctx.Err() == nil && requestCtx.Err() != nil && errors.Is(decisionErr, requestCtx.Err()) {
				// Completion also cancels the private decision context. That
				// unresolved provider request is not a host cancellation.
				decisionErr = fmt.Errorf("codex app-server completed with unresolved approval: %v", decisionErr)
			}
			var replyErr error
			if rpcErr != nil {
				replyErr = conn.ReplyWithError(requestCtx, req.ID, rpcErr)
			} else {
				replyErr = conn.Reply(requestCtx, req.ID, result)
			}
			if replyErr != nil {
				decisionErr = errors.Join(decisionErr, fmt.Errorf("codex app-server approval reply: %w", replyErr))
			}
			state.abortRequest(decisionErr)
		}()
	}
	c.handlerMu.Unlock()
	return func() {
		stop.Do(func() {
			c.handlerMu.Lock()
			c.requestHandler = nil
			c.handlerMu.Unlock()
			cancel()
			settled := make(chan struct{})
			go func() {
				pending.Wait()
				close(settled)
			}()
			select {
			case <-settled:
				return
			default:
			}
			// jsonrpc2 does not use the reply context while writing. A peer
			// that has stopped reading stdin can therefore block cancellation
			// and resident completion until the transport is retired.
			timer := time.NewTimer(approvalShutdownGrace)
			defer timer.Stop()
			select {
			case <-settled:
				return
			case <-ctx.Done():
			case <-timer.C:
			}
			_ = c.Close()
			<-settled
		})
	}
}

// These discriminated envelopes are intentionally hand-written, like union.go;
// no generated schema or generated model is edited here. Preserve every provider
// field in Payload, including MCP schema, server name, URL and metadata.
func approvalResponse(ctx context.Context, runID string, sink driver.EventSink, req *jsonrpc2.Request, state *runState) (any, *jsonrpc2.Error, error) {
	switch req.Method {
	case "mcpServer/elicitation/request", "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
	default:
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "unsupported app-server request " + req.Method}, nil
	}
	var body struct {
		ThreadID   string `json:"threadId"`
		TurnID     string `json:"turnId"`
		ItemID     string `json:"itemId"`
		ServerName string `json:"serverName"`
		Mode       string `json:"mode"`
		Message    string `json:"message"`
		Reason     string `json:"reason"`
	}
	var payload map[string]any
	if req.Params == nil || json.Unmarshal(*req.Params, &body) != nil || json.Unmarshal(*req.Params, &payload) != nil || strings.TrimSpace(body.ThreadID) == "" {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: "invalid approval request parameters"}, nil
	}
	if req.Method == "mcpServer/elicitation/request" && (body.ServerName == "" || (body.Mode != "form" && body.Mode != "url")) {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: "invalid MCP elicitation mode or server name"}, nil
	}
	state.mu.Lock()
	foreign := (state.threadID != "" && body.ThreadID != state.threadID) ||
		(body.TurnID != "" && state.turnID != "" && body.TurnID != state.turnID)
	state.mu.Unlock()
	if foreign {
		return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: "approval request belongs to another thread or turn"}, nil
	}
	if req.Method == "mcpServer/elicitation/request" && body.Mode == "form" {
		schema, ok := payload["requestedSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		if !ok || schema["type"] != "object" || len(properties) != 0 || len(required) != 0 {
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeMethodNotFound, Message: "MCP elicitation forms requiring input are unsupported"}, nil
		}
	}
	prompt := body.Message
	if prompt == "" {
		prompt = body.Reason
	}
	if prompt == "" {
		prompt = "Approve " + req.Method + "?"
	}
	decision := driver.DecisionResponse{Result: driver.DecisionRejected}
	var decisionErr error
	if decisions, ok := sink.(driver.DecisionCapableSink); ok {
		decision, decisionErr = decisions.RequestDecision(ctx, driver.DecisionRequest{
			RequestID: requestID(req.ID), RunID: runID, ThreadID: body.ThreadID,
			Kind: driver.HumanDecisionPermission, Source: "codex.app-server." + req.Method,
			ToolCallID: body.ItemID, Prompt: prompt, Payload: payload,
		})
		if decisionErr != nil && decision.Result != driver.DecisionRejected && decision.Result != driver.DecisionTimedOut {
			decision.Result = driver.DecisionAborted
		}
	}
	accepted := decision.Result == driver.DecisionApproved || decision.Result == driver.DecisionAnswered
	if req.Method == "mcpServer/elicitation/request" {
		action := "decline"
		if accepted {
			action = "accept"
		} else if decision.Result == driver.DecisionAborted {
			action = "cancel"
		}
		return map[string]any{"action": action, "content": decision.Answer, "_meta": nil}, nil, decisionErr
	}
	outcome := "decline"
	if accepted {
		outcome = "accept"
	} else if decision.Result == driver.DecisionAborted {
		outcome = "cancel"
	}
	return map[string]any{"decision": outcome}, nil, decisionErr
}

func requestID(id jsonrpc2.ID) string {
	if id.IsString {
		return id.Str
	}
	return id.String()
}
