package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

func TestPrepareCodexCommandDefaultsToBundledVerifier(t *testing.T) {
	command, mode, note, cleanup := prepareCodexCommand("")
	defer cleanup()

	if mode != "bundled-verifier" {
		t.Fatalf("expected bundled verifier mode, got %q", mode)
	}
	if !strings.Contains(strings.ToLower(note), "bundled verifier") {
		t.Fatalf("expected bundled verifier note, got %q", note)
	}
	if _, err := os.Stat(command); err != nil {
		t.Fatalf("expected built verifier command at %q: %v", command, err)
	}

	check := exec.Command(command, "--help")
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("expected bundled verifier to be runnable, err=%v output=%s", err, strings.TrimSpace(string(output)))
	}
}

func TestPrepareCodexCommandUsesExplicitHealthyCommand(t *testing.T) {
	dir := t.TempDir()
	command := testutil.WriteCommand(t, dir, "healthy-codex",
		"#!/bin/sh\nset -eu\nif [ \"$1\" = \"--help\" ]; then\n  echo healthy\n  exit 0\nfi\nexit 0\n",
		"@echo off\r\nsetlocal\r\nif \"%~1\"==\"--help\" (\r\n  echo healthy\r\n  exit /b 0\r\n)\r\nexit /b 0\r\n",
	)

	resolved, mode, note, cleanup := prepareCodexCommand(command)
	defer cleanup()

	if resolved != command {
		t.Fatalf("expected explicit command %q, got %q", command, resolved)
	}
	if mode != "external" {
		t.Fatalf("expected external mode, got %q", mode)
	}
	if !strings.Contains(strings.ToLower(note), "explicitly requested") {
		t.Fatalf("expected explicit command note, got %q", note)
	}
}
