package main

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestBuildInvocationUsesLatestUserTextAndThreadKey(t *testing.T) {
	server := &appServer{}

	inv, err := server.buildInvocation(&agui.RunAgentInput{
		ThreadID: "thread-1",
		Messages: []agui.Message{
			{ID: "m1", Role: "user", Content: []byte(`"older"`)},
			{ID: "m2", Role: "assistant", Content: []byte(`"ignored"`)},
			{ID: "m3", Role: "user", Content: []byte(`"latest"`)},
		},
	})
	if err != nil {
		t.Fatalf("buildInvocation: %v", err)
	}
	if inv.threadID != "thread-1" {
		t.Fatalf("threadID = %q, want thread-1", inv.threadID)
	}
	// v1: the session namespace + key pair collapses into one host-owned
	// thread key handed to agent.Thread(...).
	if inv.threadKey != "agui/thread-1" {
		t.Fatalf("threadKey = %q, want agui/thread-1", inv.threadKey)
	}
	if inv.prompt != "latest" {
		t.Fatalf("prompt = %q, want latest", inv.prompt)
	}
	// Streaming is the verb now, and the thread key is not an option, so the
	// only call-scope option left is the policy override.
	if got := len(inv.opts); got != 1 {
		t.Fatalf("opts len = %d, want 1 (policy override only)", got)
	}
	if got := len(inv.userTurn); got != 3 {
		t.Fatalf("userTurn len = %d, want 3", got)
	}
	first, ok := inv.userTurn[0].(adaptor.TextDelta)
	if !ok {
		t.Fatalf("userTurn[0] is %T, want adaptor.TextDelta", inv.userTurn[0])
	}
	if first.MessageID != "m3" {
		t.Fatalf("userTurn message id = %q, want m3 (latest user message)", first.MessageID)
	}
}

func TestBuildInvocationAssignsAnonymousThreadWhenMissing(t *testing.T) {
	server := &appServer{}

	inv, err := server.buildInvocation(&agui.RunAgentInput{
		Messages: []agui.Message{
			{ID: "m1", Role: "user", Content: []byte(`"hello"`)},
		},
	})
	if err != nil {
		t.Fatalf("buildInvocation: %v", err)
	}
	if !strings.HasPrefix(inv.threadID, "anon-") {
		t.Fatalf("threadID = %q, want anon-*", inv.threadID)
	}
	if !strings.HasPrefix(inv.threadKey, "agui/anon-") {
		t.Fatalf("threadKey = %q, want agui/anon-*", inv.threadKey)
	}
	if inv.prompt != "hello" {
		t.Fatalf("prompt = %q, want hello", inv.prompt)
	}
}

func TestBuildInvocationRejectsMissingUserMessage(t *testing.T) {
	server := &appServer{}

	_, err := server.buildInvocation(&agui.RunAgentInput{
		ThreadID: "thread-1",
		Messages: []agui.Message{
			{Role: "assistant", Content: []byte(`"hello"`)},
		},
	})
	if err == nil {
		t.Fatal("want error for missing user message")
	}
}
