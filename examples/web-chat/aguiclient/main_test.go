package main

import "testing"

func TestDirectClientNetworkDefaultsAreLoopbackAndOriginScoped(t *testing.T) {
	t.Setenv("ADDR", "")
	t.Setenv("CORS_ORIGIN", "")
	if got := envOr("ADDR", defaultListenAddr); got != "127.0.0.1:8090" {
		t.Fatalf("default listen address = %q, want loopback", got)
	}
	if got := envOr("CORS_ORIGIN", defaultCORSOrigin); got != "http://localhost:5173" {
		t.Fatalf("default CORS origin = %q, want the local Vite UI only", got)
	}
}

func TestDirectClientNetworkDefaultsCanBeExplicitlyOverridden(t *testing.T) {
	t.Setenv("ADDR", "0.0.0.0:9090")
	t.Setenv("CORS_ORIGIN", "https://chat.example.com")
	if got := envOr("ADDR", defaultListenAddr); got != "0.0.0.0:9090" {
		t.Fatalf("listen override = %q", got)
	}
	if got := envOr("CORS_ORIGIN", defaultCORSOrigin); got != "https://chat.example.com" {
		t.Fatalf("CORS override = %q", got)
	}
}
