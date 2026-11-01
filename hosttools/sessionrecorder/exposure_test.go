package sessionrecorder_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	recorder "github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

type auditDriver struct{}

func (auditDriver) Descriptor() driver.Descriptor { return driver.Descriptor{Type: "audit"} }
func (auditDriver) ValidateConfig(any) error      { return nil }
func (auditDriver) Run(context.Context, driver.Request, driver.EventSink) (driver.Response, error) {
	return driver.Response{
		Output: "answer", Summary: "summary", Model: "private-model", Provider: "private-provider",
		Metadata: map[string]string{"private": "private-metadata"}, Usage: &driver.Usage{},
		RawStreams: &driver.RawStreams{
			Stdout: "private-stdout", Stderr: "private-stderr",
			Terminal: &driver.TerminalPayload{Event: "done", JSON: json.RawMessage(`{"private":"private-terminal"}`)},
		},
		Transcript:      []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "private-transcript"}},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "service", URL: "private-service"}},
	}, nil
}

func TestTerminalExposureAndDurableReplay(t *testing.T) {
	result, err := adaptor.New(auditDriver{}).Send(t.Context(), "start").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			backend, err := recorder.NewJSONLEventBackend(dir)
			if err != nil {
				t.Fatal(err)
			}
			policy := recorder.ExposurePolicy{IncludeMetadata: enabled, IncludeUsage: enabled, IncludeRawStreams: enabled, IncludeProviderResult: enabled, IncludeTranscript: enabled, IncludeServices: enabled}
			rec := recorder.NewEventRecorder(backend, recorder.WithExposure(policy))
			events := []adaptor.Event{
				adaptor.MessageUpdate{ID: "m", State: adaptor.MessageAccepted},
				adaptor.MessageUpdate{ID: "m", State: adaptor.MessageDelivered, Turn: 1, Boundary: "tool", Mechanism: "fixture"},
				adaptor.MessageUpdate{ID: "failed", State: adaptor.MessageFailed, Reason: adaptor.MessageRunFailed, Text: "original"},
				adaptor.TurnEnd{Turn: 1, Result: result, MessageIDs: []string{"m"}},
				adaptor.Done{Err: &adaptor.RunError{Reason: adaptor.ReasonAgentError, Message: "failed", Details: map[string]any{"private": "private-details"}, Result: result}},
				adaptor.Done{Result: result},
			}
			for i, event := range events {
				meta := adaptor.EventMeta{RunID: "run", ThreadKey: "thread", Sequence: uint64(i + 1), Time: time.Unix(123, int64(i)).UTC(), Source: &adaptor.EventSourceMeta{RunID: "provider", Sequence: 99}}
				record, err := rec.Record(t.Context(), "session", adaptor.WithEventMeta(event, meta))
				if err != nil {
					t.Fatal(err)
				}
				if record.Event.Meta().Sequence != meta.Sequence {
					t.Fatal("metadata lost before persistence")
				}
				// Memory and durable storage obey identical terminal projections.
				if done, ok := record.Event.(adaptor.Done); ok && done.Err != nil {
					var failure *adaptor.RunError
					if !errors.As(done.Err, &failure) {
						t.Fatal(done.Err)
					}
					if (failure.Result.Metadata != nil) != enabled || (failure.Details != nil) != enabled || failure.Result.Raw().Stdout != "" {
						t.Fatal("unsafe memory result")
					}
				}
			}
			if err := rec.Close(); err != nil {
				t.Fatal(err)
			}
			backend, err = recorder.NewJSONLEventBackend(dir)
			if err != nil {
				t.Fatal(err)
			}
			replay := recorder.NewEventRecorder(backend)
			defer replay.Close()
			records, err := replay.Since(t.Context(), "session", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != len(events) {
				t.Fatal(len(records))
			}
			for i, record := range records {
				if reflect.TypeOf(record.Event) != reflect.TypeOf(events[i]) || record.Event.Meta().Sequence != uint64(i+1) || record.Event.Meta().ThreadKey != "thread" || record.Event.Meta().Source.Sequence != 99 {
					t.Fatalf("record=%+v", record)
				}
				wire, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range []string{"private-model", "private-provider", "private-metadata", "private-stdout", "private-stderr", "private-terminal", "private-transcript", "private-details", "private-service"} {
					present := strings.Contains(string(wire), secret)
					terminal := i >= 3
					if present != (enabled && terminal && (secret != "private-details" || i == 4)) {
						t.Fatalf("secret=%s enabled=%v index=%d record=%s", secret, enabled, i, wire)
					}
				}
			}
			turn := records[3].Event.(adaptor.TurnEnd)
			if turn.Turn != 1 || turn.Result.Text != "answer" || !reflect.DeepEqual(turn.MessageIDs, []string{"m"}) {
				t.Fatal(turn)
			}
			failed := records[2].Event.(adaptor.MessageUpdate)
			if failed.Text != "original" || failed.State != adaptor.MessageFailed {
				t.Fatal(failed)
			}
			if enabled && (records[4].Diagnostics == nil || records[4].Diagnostics.Raw.Stdout != "private-stdout" || len(records[4].Diagnostics.Transcript) != 1) {
				t.Fatal("missing opt-in diagnostics")
			}
		})
	}
}

func TestTerminalExposureOptionsAreIndependent(t *testing.T) {
	result, err := adaptor.New(auditDriver{}).Send(t.Context(), "start").Result()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		policy recorder.ExposurePolicy
		want   string
	}{
		{name: "default"},
		{name: "metadata", policy: recorder.ExposurePolicy{IncludeMetadata: true}, want: "private-metadata"},
		{name: "usage", policy: recorder.ExposurePolicy{IncludeUsage: true}, want: `"Usage"`},
		{name: "raw", policy: recorder.ExposurePolicy{IncludeRawStreams: true}, want: "private-stdout"},
		{name: "terminal", policy: recorder.ExposurePolicy{IncludeProviderResult: true}, want: "private-terminal"},
		{name: "transcript", policy: recorder.ExposurePolicy{IncludeTranscript: true}, want: "private-transcript"},
		{name: "services", policy: recorder.ExposurePolicy{IncludeServices: true}, want: "private-service"},
	}
	markers := []string{"private-metadata", "private-stdout", "private-terminal", "private-transcript", "private-service"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := recorder.NewEventRecorder(recorder.NewMemoryEventBackend(), recorder.WithExposure(tc.policy))
			defer rec.Close()
			record, err := rec.Record(t.Context(), "session", adaptor.Done{Result: result})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range markers {
				if strings.Contains(string(wire), marker) != (marker == tc.want) {
					t.Fatalf("marker %q with %s policy: %s", marker, tc.name, wire)
				}
			}
			var envelope struct {
				Event struct {
					Result map[string]json.RawMessage
				}
			}
			if err := json.Unmarshal(wire, &envelope); err != nil {
				t.Fatal(err)
			}
			_, hasUsage := envelope.Event.Result["Usage"]
			if hasUsage != tc.policy.IncludeUsage {
				t.Fatalf("observed zero usage did not follow opt-in: %s", wire)
			}
			var replay recorder.EventRecord
			if err := json.Unmarshal(wire, &replay); err != nil {
				t.Fatal(err)
			}
			wireAgain, err := json.Marshal(replay)
			if err != nil {
				t.Fatal(err)
			}
			if string(wireAgain) != string(wire) {
				t.Fatalf("replay changed opted-in fields:\n%s\n%s", wire, wireAgain)
			}
		})
	}
	// Manually constructed records must also use the safe default projection.
	wire, err := json.Marshal(recorder.EventRecord{Event: adaptor.Done{Result: result}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "private-") || strings.Contains(string(wire), `"Usage"`) {
		t.Fatalf("direct EventRecord serialization leaked diagnostics: %s", wire)
	}
}

func TestTerminalReplayPreservesBusinessAndContextErrorIdentity(t *testing.T) {
	for _, ctxErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(ctxErr.Error(), func(t *testing.T) {
			failure := errors.Join(&adaptor.RunError{
				Reason: adaptor.ReasonCancelled, Message: "stopped", Details: map[string]any{"private": "secret"},
				Result: &adaptor.Result{Text: "partial", Metadata: map[string]string{"private": "secret"}},
			}, ctxErr)
			rec := recorder.NewEventRecorder(recorder.NewMemoryEventBackend())
			defer rec.Close()
			for _, event := range []adaptor.Event{adaptor.Done{Err: failure}, adaptor.TurnEnd{Turn: 1, Err: failure}} {
				record, err := rec.Record(t.Context(), "session", event)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(wire), "secret") {
					t.Fatalf("cancellation leaked private result: %s", wire)
				}
				var replay recorder.EventRecord
				if err := json.Unmarshal(wire, &replay); err != nil {
					t.Fatal(err)
				}
				for _, candidate := range []adaptor.Event{record.Event, replay.Event} {
					var gotErr error
					switch typed := candidate.(type) {
					case adaptor.Done:
						gotErr = typed.Err
					case adaptor.TurnEnd:
						gotErr = typed.Err
					}
					var got *adaptor.RunError
					if !errors.Is(gotErr, ctxErr) || !errors.Is(gotErr, adaptor.ErrRunCancelled) || !errors.As(gotErr, &got) || got.Result.Text != "partial" {
						t.Fatalf("terminal event lost error identity or partial result: %v", gotErr)
					}
				}
			}
		})
	}
}
