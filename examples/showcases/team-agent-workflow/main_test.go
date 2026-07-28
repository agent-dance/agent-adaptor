package main

import "testing"

// This package's tests are deliberately configuration-only: they must never
// execute any of the four live CLI calls made by the showcase.
func TestPaidWebModeDefaultsAreLoopbackAndCORSDisabled(t *testing.T) {
	if defaultWebListenAddr != "127.0.0.1:8080" {
		t.Fatalf("default web address = %q, want loopback", defaultWebListenAddr)
	}
	if defaultWebCORSOrigin != "" {
		t.Fatalf("default CORS origin = %q, want disabled", defaultWebCORSOrigin)
	}
}
