package main

import "testing"

func TestWebChatNetworkDefaultsAreLoopbackAndSameOrigin(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("CORS_ORIGIN", "")
	if got := envOverride("ADDR", defaultListenAddr); got != "127.0.0.1:8080" {
		t.Fatalf("default listen address = %q, want loopback", got)
	}
	if got := envOverride("CORS_ORIGIN", defaultCORSOrigin); got != "" {
		t.Fatalf("default CORS origin = %q, want disabled for same-origin UI", got)
	}
}

func TestWebChatNetworkDefaultsCanBeExplicitlyOverridden(t *testing.T) {
	t.Setenv("ADDR", "0.0.0.0:9080")
	t.Setenv("CORS_ORIGIN", "https://chat.example.com")
	if got := envOverride("ADDR", defaultListenAddr); got != "0.0.0.0:9080" {
		t.Fatalf("listen override = %q", got)
	}
	if got := envOverride("CORS_ORIGIN", defaultCORSOrigin); got != "https://chat.example.com" {
		t.Fatalf("CORS override = %q", got)
	}
}
