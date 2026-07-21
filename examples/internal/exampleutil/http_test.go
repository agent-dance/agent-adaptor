package exampleutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithRequestTimeoutSetsDeadline(t *testing.T) {
	var seen error
	handler := WithRequestTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		seen = r.Context().Err()
		w.WriteHeader(http.StatusGatewayTimeout)
	}), 10*time.Millisecond)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(recorder, request)

	if seen != context.DeadlineExceeded {
		t.Fatalf("request context error = %v, want deadline exceeded", seen)
	}
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusGatewayTimeout)
	}
}

func TestHTTPURL(t *testing.T) {
	for _, tt := range []struct{ addr, want string }{
		{":8080", "http://localhost:8080/health"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080/health"},
	} {
		if got := HTTPURL(tt.addr, "/health"); got != tt.want {
			t.Errorf("HTTPURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestShutdownServerForceClosesActiveRequest(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()

	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = http.Get(server.URL)
	}()
	<-started

	if err := shutdownServer(server.Config, 10*time.Millisecond); err != nil {
		t.Fatalf("shutdownServer: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("active request context was not cancelled after forced close")
	}
	<-requestDone
}
