package main

import (
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/pkg/bridges/agui"
)

func TestBuildInvocationUsesLatestUserTextAndThreadID(t *testing.T) {
	server := &appServer{}

	inv, err := server.buildInvocation(&agui.RunAgentInput{
		ThreadID: "thread-1",
		Messages: []agui.Message{
			{Role: "user", Content: []byte(`"older"`)},
			{Role: "assistant", Content: []byte(`"ignored"`)},
			{Role: "user", Content: []byte(`"latest"`)},
		},
	})
	if err != nil {
		t.Fatalf("buildInvocation: %v", err)
	}
	if inv.threadID != "thread-1" {
		t.Fatalf("threadID = %q, want thread-1", inv.threadID)
	}
	if inv.prompt != "latest" {
		t.Fatalf("prompt = %q, want latest", inv.prompt)
	}
	if got := len(inv.opts); got != 3 {
		t.Fatalf("opts len = %d, want 3 (streaming + run policy + session key)", got)
	}
}

func TestBuildInvocationAssignsAnonymousThreadWhenMissing(t *testing.T) {
	server := &appServer{}

	inv, err := server.buildInvocation(&agui.RunAgentInput{
		Messages: []agui.Message{
			{Role: "user", Content: []byte(`"hello"`)},
		},
	})
	if err != nil {
		t.Fatalf("buildInvocation: %v", err)
	}
	if !strings.HasPrefix(inv.threadID, "anon-") {
		t.Fatalf("threadID = %q, want anon-*", inv.threadID)
	}
	if inv.prompt != "hello" {
		t.Fatalf("prompt = %q, want hello", inv.prompt)
	}
	if got := len(inv.opts); got != 2 {
		t.Fatalf("opts len = %d, want 2 (streaming + run policy)", got)
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
