package a2a_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	bridge "github.com/agent-dance/agent-adaptor/v2/bridges/a2a"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/team"
)

type leaderBridgeDriver struct {
	scriptedDriver
	execute func(context.Context, driver.Request) (driver.Response, error)
}

func (d *leaderBridgeDriver) Descriptor() driver.Descriptor {
	desc := d.scriptedDriver.Descriptor()
	desc.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	return desc
}
func (d *leaderBridgeDriver) Run(ctx context.Context, req driver.Request, _ driver.EventSink) (driver.Response, error) {
	return d.execute(ctx, req)
}

func teamTool(ctx context.Context, group *team.Team, req driver.Request, name string, args map[string]any) (map[string]any, error) {
	token := ""
	for _, binding := range req.Runtime.SecretEnv {
		if strings.HasPrefix(binding.Name, "AGENT_ADAPTOR_TEAM_TOKEN_") {
			token = binding.Value
		}
	}
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "team_" + name, "arguments": args}})
	if err != nil {
		return nil, err
	}
	request := httptest.NewRequest(http.MethodPost, "http://team.invalid/mcp", bytes.NewReader(payload)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	group.ServeHTTP(recorder, request)
	var envelope struct {
		Error  any
		Result struct {
			IsError bool           `json:"isError"`
			Content map[string]any `json:"structuredContent"`
		}
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		return nil, err
	}
	if recorder.Code != http.StatusOK || envelope.Error != nil || envelope.Result.IsError {
		return nil, fmt.Errorf("team tool: HTTP %d %s", recorder.Code, recorder.Body.String())
	}
	return envelope.Result.Content, nil
}

// A2A discovery and every protocol request use an in-memory HTTP transport.
// Team's public constructor still opens its mandatory MCP listener; tool calls
// below invoke ServeHTTP directly and never connect to that socket.
func TestTeamRemoteSameTaskSupplementToBridge(t *testing.T) {
	ctx := deadline(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	remoteDriver := &scriptedDriver{run: func(turn int, req driver.Request, _ driver.EventSink) (driver.Response, error) {
		if turn == 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return driver.Response{}, ctx.Err()
			}
			return healthy("first"), nil
		}
		if !strings.Contains(req.Prompt, "remote supplement") {
			return driver.Response{}, fmt.Errorf("remote supplement missing: %s", req.Prompt)
		}
		return healthy("remote answer"), nil
	}}
	remote := adaptor.New(remoteDriver)
	server := bridge.NewServer(remote, bridge.ServerOptions{AgentCard: testCard(), Session: bridge.ThreadByContextID()})
	mux := http.NewServeMux()
	mux.Handle("/card", server.AgentCardHandler())
	mux.Handle("/a2a", server.Handler())
	var group *team.Team
	var leaderOnce sync.Once
	leaderDriver := &leaderBridgeDriver{execute: func(ctx context.Context, req driver.Request) (driver.Response, error) {
		var toolErr error
		leaderOnce.Do(func() {
			task, err := teamTool(ctx, group, req, "delegate", map[string]any{"member": "remote", "objective": "work"})
			if err != nil {
				toolErr = err
				return
			}
			_, toolErr = teamTool(ctx, group, req, "send", map[string]any{"task": task["task"], "text": "remote supplement"})
			releaseOnce.Do(func() { close(release) })
		})
		return healthy("leader answer"), toolErr
	}}
	store := team.NewMemoryStore()
	var err error
	group, err = team.New(team.Config{Leader: adaptor.New(leaderDriver), Store: store, Limits: team.Limits{WakeWindow: time.Nanosecond}, Members: []team.Member{{Key: "remote", Remote: &team.RemoteSpec{AgentCardURL: "https://example.com/card", HTTPClient: &http.Client{Transport: handlerTransport{mux}}}}}})
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("Team.New requires a loopback listener unavailable in this sandbox: %v", err)
		}
		t.Fatal(err)
	}
	defer group.Close(ctx)
	stream := group.Thread("scope").Send(ctx, "delegate and supplement")
	for range stream.Events() {
	}
	result, err := stream.Result()
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := store.Tasks(ctx, "scope")
	if err != nil || len(tasks) != 1 || tasks[0].RemoteTask == "" || tasks[0].RemoteSnapshot == nil {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if len(result.Subagents()) != 1 || result.Subagents()[0].State != "succeeded" {
		t.Fatal(result.Subagents())
	}
	remoteDriver.mu.Lock()
	defer remoteDriver.mu.Unlock()
	if len(remoteDriver.requests) != 2 || remoteDriver.requests[1].Session == nil || remoteDriver.requests[1].Session.Mode != driver.SessionContinueOnly {
		t.Fatalf("requests=%+v", remoteDriver.requests)
	}
}
