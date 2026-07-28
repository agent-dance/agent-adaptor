package main

import (
	"bytes"
	"errors"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
)

func TestWriteResultTextFallback(t *testing.T) {
	tests := []struct {
		name      string
		seen      bool
		result    *adaptor.Result
		err       error
		want      string
		wantWrote bool
	}{
		{name: "cursor-like final result", result: &adaptor.Result{Text: "final answer"}, want: "final answer", wantWrote: true},
		{name: "streamed text is not duplicated", seen: true, result: &adaptor.Result{Text: "final answer"}},
		{name: "empty result", result: &adaptor.Result{}},
		{name: "business failure partial result", err: &adaptor.RunError{Reason: adaptor.ReasonAgentError, Result: &adaptor.Result{Text: "partial"}}, want: "partial", wantWrote: true},
		{name: "plain error is not assistant text", err: errors.New("transport failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if got := writeResultTextFallback(&output, test.seen, test.result, test.err); got != test.wantWrote {
				t.Fatalf("wrote = %v, want %v", got, test.wantWrote)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("output = %q, want %q", got, test.want)
			}
		})
	}
}
