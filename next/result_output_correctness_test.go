package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	adaptor "github.com/agent-dance/agent-adaptor/next"
)

func TestRunAndStreamResultAllLayersAreEquivalent(t *testing.T) {
	fake := newFakeDriver()
	fake.response = driver.Response{
		Output:   "assistant",
		Summary:  "short",
		Model:    "model",
		Provider: "provider",
		Usage:    &driver.Usage{InputTokens: 2, OutputTokens: 3},
		Metadata: map[string]string{"key": "value"},
		RawStreams: &driver.RawStreams{
			Stdout: "stdout",
			Stderr: "stderr",
			Terminal: &driver.TerminalPayload{
				Event: "result",
				JSON:  json.RawMessage(`{"status":"success"}`),
			},
		},
		Transcript:      []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "assistant"}},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "svc", Name: "service"}},
	}
	agent := adaptor.New(fake)

	runResult, err := agent.Run(context.Background(), "prompt")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	stream := agent.Stream(context.Background(), "prompt")
	for range stream.Events() {
	}
	streamResult, err := stream.Result()
	if err != nil {
		t.Fatalf("Stream.Result: %v", err)
	}
	runComparable := *runResult
	streamComparable := *streamResult
	// Each invocation intentionally receives a distinct SDK RunID; every
	// driver-derived Result layer must otherwise be equivalent.
	runComparable.RunID = ""
	streamComparable.RunID = ""
	if !reflect.DeepEqual(runComparable, streamComparable) || !reflect.DeepEqual(runResult.Raw(), streamResult.Raw()) || !reflect.DeepEqual(runResult.Transcript(), streamResult.Transcript()) || !reflect.DeepEqual(runResult.Services(), streamResult.Services()) {
		t.Fatalf("Run and Stream.Result diverged:\nrun=%#v raw=%#v transcript=%#v services=%#v\nstream=%#v raw=%#v transcript=%#v services=%#v",
			runResult, runResult.Raw(), runResult.Transcript(), runResult.Services(), streamResult, streamResult.Raw(), streamResult.Transcript(), streamResult.Services())
	}
}

func TestRunErrorCarriesCompletePartialResult(t *testing.T) {
	fake := newFakeDriver()
	fake.response = driver.Response{
		Output:  "partial assistant output",
		Summary: "partial",
		RawStreams: &driver.RawStreams{
			Stdout:   "partial stdout",
			Stderr:   "partial stderr",
			Terminal: &driver.TerminalPayload{Event: "turn/completed", JSON: json.RawMessage(`{"status":"failed"}`)},
		},
		Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptResult, IsError: true, Subtype: "failed"}},
		Failure:    &driver.RunFailure{Code: driver.FailureAgentError, Message: "provider failed"},
	}
	result, err := adaptor.New(fake).Run(context.Background(), "prompt")
	if result != nil {
		t.Fatalf("success Result = %#v", result)
	}
	var runErr *adaptor.RunError
	if !errors.As(err, &runErr) || runErr.Result == nil {
		t.Fatalf("error = %T %v", err, err)
	}
	partial := runErr.Result
	if partial.Text != "partial assistant output" || partial.Summary != "partial" || partial.Raw().Terminal == nil || string(partial.Raw().Terminal.JSON) != `{"status":"failed"}` || len(partial.Transcript()) != 1 {
		t.Fatalf("partial Result = %#v raw=%#v transcript=%#v", partial, partial.Raw(), partial.Transcript())
	}
}
