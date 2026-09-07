// This executable is a gate canary, never a provider protocol fixture.
package main

import "os"

func main() {
	if marker := os.Getenv("AGENT_ADAPTOR_CODEX_CANARY_FILE"); marker != "" {
		_ = os.WriteFile(marker, []byte("unexpected CLI invocation\n"), 0600)
	}
	os.Exit(91)
}
