package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

type delayedSupplementRecorder struct {
	sessionrecorder.EventRecorder
	releaseDriver chan struct{}
	entered       chan struct{}
	release       chan struct{}
	once          sync.Once
}

type limitedChatDriver struct{ *chatDriver }

func (d limitedChatDriver) Descriptor() driver.Descriptor {
	descriptor := d.chatDriver.Descriptor()
	descriptor.Messages.MaxUnits = 1
	return descriptor
}

func TestRejectedSupplementCreatesNoUserHistory(t *testing.T) {
	t.Setenv("AGUI_AGENT", "codex")
	for _, endpoint := range []string{"/agent", "/message"} {
		t.Run(endpoint, func(t *testing.T) {
			ctx := t.Context()
			fake := &chatDriver{started: make(chan struct{}), release: make(chan struct{})}
			server, err := newAppServer(adaptor.New(limitedChatDriver{fake}), "fake", "*")
			if err != nil {
				t.Fatal(err)
			}
			defer server.store.recorder.Close()
			initialEnded := make(chan struct{})
			go func() {
				defer close(initialEnded)
				request := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(`{"threadId":"thread","messages":[{"id":"initial","role":"user","content":"start"}]}`)).WithContext(ctx)
				server.routes().ServeHTTP(httptest.NewRecorder(), request)
			}()
			<-fake.started
			response := httptest.NewRecorder()
			body := `{"threadId":"thread","messageId":"rejected","text":"too large"}`
			if endpoint == "/agent" {
				body = `{"threadId":"thread","messages":[{"id":"rejected","role":"user","content":"too large"}]}`
			}
			server.routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)).WithContext(ctx))
			// Also prove the rejection releases the barrier for the original drain.
			close(fake.release)
			<-initialEnded
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("rejected supplement HTTP %d: %s", response.Code, response.Body.String())
			}
			records := server.store.historyAfter("thread", 0)
			for _, record := range records {
				if delta, ok := record.Event.(adaptor.TextDelta); ok && delta.Role == adaptor.RoleUser && delta.MessageID == "rejected" {
					t.Fatalf("rejected input was recorded: %+v", delta)
				}
			}
			if len(records) == 0 {
				t.Fatal("missing initial run history")
			}
			if _, done := records[len(records)-1].Event.(adaptor.Done); !done {
				t.Fatalf("terminal drain blocked after rejection: last=%T", records[len(records)-1].Event)
			}
		})
	}
}

func (r *delayedSupplementRecorder) Record(ctx context.Context, key string, event adaptor.Event) (sessionrecorder.EventRecord, error) {
	if delta, ok := event.(adaptor.TextDelta); ok && delta.Role == adaptor.RoleUser && delta.MessageID == "supplement" {
		r.once.Do(func() {
			close(r.releaseDriver)
			close(r.entered)
			select {
			case <-r.release:
			case <-ctx.Done():
			}
		})
	}
	return r.EventRecorder.Record(ctx, key, event)
}

func TestSupplementHistoryPrecedesDone(t *testing.T) {
	t.Setenv("AGUI_AGENT", "codex")
	for _, endpoint := range []string{"/agent", "/message"} {
		t.Run(endpoint, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fake := &chatDriver{started: make(chan struct{}), release: make(chan struct{})}
				server, err := newAppServer(adaptor.New(fake), "fake", "*")
				if err != nil {
					t.Fatal(err)
				}
				defer server.store.recorder.Close()
				recorder := &delayedSupplementRecorder{
					EventRecorder: server.store.recorder, releaseDriver: fake.release,
					entered: make(chan struct{}), release: make(chan struct{}),
				}
				server.store.recorder = recorder
				handler := server.routes()
				initialEnded := make(chan struct{})
				go func() {
					defer close(initialEnded)
					request := httptest.NewRequest(http.MethodPost, "/agent", strings.NewReader(`{"threadId":"thread","messages":[{"id":"initial","role":"user","content":"start"}]}`))
					handler.ServeHTTP(httptest.NewRecorder(), request)
				}()
				<-fake.started
				response := httptest.NewRecorder()
				supplementEnded := make(chan struct{})
				go func() {
					defer close(supplementEnded)
					body := `{"threadId":"thread","messageId":"supplement","text":"supplement"}`
					if endpoint == "/agent" {
						body = `{"threadId":"thread","messages":[{"id":"supplement","role":"user","content":"supplement"}]}`
					}
					handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
				}()
				<-recorder.entered
				// The provider and normal event drain can finish while history is
				// paused. The terminal drain must wait for this accepted triple.
				synctest.Wait()
				select {
				case <-initialEnded:
					t.Error("original consumer finished before accepted supplement history")
				default:
				}
				for _, record := range server.store.historyAfter("thread", 0) {
					if _, done := record.Event.(adaptor.Done); done {
						t.Error("Done was recorded before accepted supplement history")
					}
				}
				close(recorder.release)
				<-supplementEnded
				<-initialEnded
				if response.Code != http.StatusAccepted {
					t.Fatalf("admission HTTP %d: %s", response.Code, response.Body.String())
				}
				records := server.store.historyAfter("thread", 0)
				translator := agui.NewEventTranslator()
				var phases []adaptor.Phase
				var replay strings.Builder
				doneCount := 0
				for index, record := range records {
					if _, done := record.Event.(adaptor.Done); done {
						doneCount++
						if index != len(records)-1 {
							t.Fatalf("Done at history index %d followed by %d events", index, len(records)-index-1)
						}
					}
					if delta, ok := record.Event.(adaptor.TextDelta); ok && delta.Role == adaptor.RoleUser && delta.MessageID == "supplement" {
						phases = append(phases, delta.Phase)
					}
					for _, event := range translator.Translate(record.Event) {
						raw, err := json.Marshal(event)
						if err != nil {
							t.Fatal(err)
						}
						replay.Write(raw)
					}
				}
				if doneCount != 1 || len(phases) != 3 || phases[0] != adaptor.PhaseStart || phases[1] != adaptor.PhaseContent || phases[2] != adaptor.PhaseEnd {
					t.Fatalf("history Done count=%d supplement phases=%v", doneCount, phases)
				}
				if !strings.Contains(replay.String(), `"delta":"supplement"`) {
					t.Fatal("replay suppressed the accepted supplement")
				}
			})
		})
	}
}
