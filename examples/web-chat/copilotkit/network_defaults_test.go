package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCopilotKitNetworkDefaultsAreLoopbackAndOriginScoped(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("CORS_ORIGIN", "")
	if got := envOr("ADDR", defaultListenAddr); got != "127.0.0.1:8080" {
		t.Fatalf("default listen address = %q, want loopback", got)
	}
	if got := envOr("CORS_ORIGIN", defaultCORSOrigin); got != "http://localhost:3000" {
		t.Fatalf("default CORS origin = %q, want the local CopilotKit UI only", got)
	}
}

func TestCopilotKitNetworkDefaultsCanBeExplicitlyOverridden(t *testing.T) {
	t.Setenv("ADDR", "0.0.0.0:9080")
	t.Setenv("CORS_ORIGIN", "https://chat.example.com")
	if got := envOr("ADDR", defaultListenAddr); got != "0.0.0.0:9080" {
		t.Fatalf("listen override = %q", got)
	}
	if got := envOr("CORS_ORIGIN", defaultCORSOrigin); got != "https://chat.example.com" {
		t.Fatalf("CORS override = %q", got)
	}
}

func TestCORSMiddlewareOmitsHeadersWhenDisabled(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	corsMiddleware("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want omitted", got)
	}
}

func TestCORSMiddlewareUsesExplicitOrigin(t *testing.T) {
	const origin = "https://chat.example.com"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/agent", nil)
	corsMiddleware(origin, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight must not reach the next handler")
	})).ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != origin {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, origin)
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
