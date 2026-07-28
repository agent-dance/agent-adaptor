package adaptor_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
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

func TestUsageObservationIsEquivalentAcrossRunStreamAndRunError(t *testing.T) {
	cases := []struct {
		name     string
		usage    *driver.Usage
		observed bool
	}{
		{name: "unobserved", usage: nil, observed: false},
		{name: "observed zero", usage: &driver.Usage{}, observed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertUsage := func(label string, usage *adaptor.Usage) {
				t.Helper()
				if (usage != nil) != tc.observed {
					t.Fatalf("%s Usage = %#v, observed=%v", label, usage, tc.observed)
				}
				if usage != nil && *usage != (adaptor.Usage{}) {
					t.Fatalf("%s Usage = %#v, want observed zero", label, usage)
				}
			}

			success := newFakeDriver()
			success.response = driver.Response{Usage: tc.usage}
			agent := adaptor.New(success)
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
			assertUsage("Run", runResult.Usage)
			assertUsage("Stream.Result", streamResult.Usage)

			for _, call := range []struct {
				name string
				run  func(*adaptor.Agent) error
			}{
				{name: "RunError from Run", run: func(agent *adaptor.Agent) error {
					_, err := agent.Run(context.Background(), "prompt")
					return err
				}},
				{name: "RunError from Stream", run: func(agent *adaptor.Agent) error {
					stream := agent.Stream(context.Background(), "prompt")
					for range stream.Events() {
					}
					_, err := stream.Result()
					return err
				}},
			} {
				t.Run(call.name, func(t *testing.T) {
					failed := newFakeDriver()
					failed.response = driver.Response{
						Usage:   tc.usage,
						Failure: &driver.RunFailure{Code: driver.FailureAgentError, Message: "failed"},
					}
					err := call.run(adaptor.New(failed))
					var runErr *adaptor.RunError
					if !errors.As(err, &runErr) || runErr.Result == nil {
						t.Fatalf("error = %T %v, want RunError with Result", err, err)
					}
					assertUsage(call.name, runErr.Result.Usage)
				})
			}
		})
	}
}
