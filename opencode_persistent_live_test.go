package adaptor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/tool"
)

func TestLiveOpenCodePermissionReset(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	workspace := t.TempDir()
	for i := 0; i < 4; i++ {
		if err := os.WriteFile(filepath.Join(workspace, fmt.Sprintf("marker-%d.txt", i)), []byte(fmt.Sprintf("POLICY_ROUND_%d_593", i)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := adaptor.New(opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true}), adaptor.WithWorkspace(workspace), adaptor.WithThreadStore(memory.NewStore()))
	defer a.Close(context.Background())
	thread := a.Thread("native-permission-reset")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var session string
	for round, gated := range []bool{true, false, true, false} {
		mode := adaptor.ApprovalAutoApprove
		if gated {
			mode = adaptor.ApprovalAsk
		}
		var decisions atomic.Int32
		options := []adaptor.CallOption{
			adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: mode}}),
			adaptor.OnApproval(func(ctx context.Context, request *adaptor.ApprovalRequest) error {
				decisions.Add(1)
				return request.Approve(ctx)
			}),
		}
		if round == 3 {
			options = append(options, adaptor.WithSpawn())
		}
		result, err := thread.Run(ctx, fmt.Sprintf("Read marker-%d.txt using the read tool, without shell commands. Return only its exact content. You must actually read this file.", round), options...)
		if err != nil {
			t.Fatal(err)
		}
		assertLiveResult(t, result, fmt.Sprintf("POLICY_ROUND_%d_593", round))
		observed := ""
		for _, item := range result.Transcript() {
			if item.Kind == driver.TranscriptInit && item.SessionID != "" {
				observed = item.SessionID
				break
			}
		}
		if observed == "" || (session != "" && session != observed) {
			t.Fatal("policy switch lost the native conversation")
		}
		session = observed
		// AutoApprove can silently answer stale native asks; inspect official SSE
		// to prove the previous Ask rules were actually superseded.
		permissions := 0
		scanner := bufio.NewScanner(strings.NewReader(result.Metadata["opencode.event_stream"]))
		scanner.Buffer(make([]byte, 4096), 8<<20)
		for scanner.Scan() {
			line, ok := strings.CutPrefix(scanner.Text(), "data: ")
			if !ok {
				continue
			}
			var event struct {
				Type       string
				Properties struct{ Permission string }
			}
			if json.Unmarshal([]byte(line), &event) == nil && event.Type == "permission.asked" && event.Properties.Permission == "read" {
				permissions++
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		if gated && (permissions == 0 || decisions.Load() == 0) {
			t.Fatal("Ask did not reach the host")
		}
		if !gated && (permissions != 0 || decisions.Load() != 0) {
			t.Fatal("previous Ask rules survived native session PATCH")
		}
	}
}

func TestLiveOpenCodePersistent(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_OPENCODE_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_OPENCODE_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	var calls atomic.Int32
	type output struct {
		Word string `json:"word"`
	}
	definition := tool.Define("next_marker", "Obtain a fresh marker; always invoke it when asked for the next marker.", func(context.Context, struct{}) (output, error) {
		return output{Word: fmt.Sprintf("FRESH_%d", calls.Add(1))}, nil
	}, tool.ReadOnly(), tool.Revision("marker/v1"))
	a := adaptor.New(opencode.Driver(opencode.Config{Command: os.Getenv("AGENT_ADAPTOR_OPENCODE_COMMAND"), Model: model, Pure: true}), adaptor.WithWorkspace(t.TempDir()), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithInstructions("When reporting a marker, use only the fresh marker returned by next_marker."), adaptor.WithTools(definition), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove}}))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	thread := a.Thread("resident-with-tools")
	first, err := thread.Run(ctx, "Call next_marker and report the fresh marker.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := thread.Run(ctx, "Call next_marker again. Return the fresh marker as the word field.", adaptor.WithSchema[output]())
	if err != nil {
		t.Fatal(err)
	}
	var decoded output
	if err := second.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text, "FRESH_1") || decoded.Word != "FRESH_2" || calls.Load() != 2 {
		t.Fatalf("first=%q second=%+v actual tool calls=%d", first.Text, decoded, calls.Load())
	}
	pid := first.Metadata["opencode.server_pid"]
	if pid == "" || second.Metadata["opencode.server_pid"] != pid || second.Metadata["opencode.server_reused"] != "true" {
		t.Fatalf("native server was not reused: %v -> %v", first.Metadata["opencode.server_pid"], second.Metadata["opencode.server_pid"])
	}
	third, err := thread.Run(ctx, "What was the last fresh marker? Reply only with that marker without calling tools.", adaptor.WithSpawn())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third.Text, "FRESH_2") || third.Metadata["opencode.server_pid"] == pid || third.Metadata["opencode.server_reused"] != "false" {
		t.Fatal("WithSpawn did not retain conversation in a fresh process")
	}
	t.Logf("reused server %s across real hosted-tool calls and native schema; WithSpawn resumed on %s", pid, third.Metadata["opencode.server_pid"])
}
