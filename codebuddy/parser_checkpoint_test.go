package codebuddy

import (
	"testing"

	driver "github.com/agent-dance/agent-adaptor/driver"
)

func TestCodeBuddyCheckpointRequiresOfficialSuccessAndCleanOutcome(t *testing.T) {
	parse := func(stdout string) *parser {
		p := newParser(nil)
		_ = p.onChunk("stdout", []byte(stdout), timeNow())
		p.finalize()
		return p
	}
	success := parse("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\"}\n")
	if cp := success.checkpointForOutcome(0, "", false, nil); cp == nil || !cp.Valid {
		t.Fatalf("clean official result success = %#v, want valid", cp)
	}
	for _, tc := range []struct {
		name     string
		exitCode int
		signal   string
		timedOut bool
		failure  *driver.RunFailure
	}{
		{name: "nonzero", exitCode: 1},
		{name: "signal", signal: "SIGTERM"},
		{name: "timeout", timedOut: true},
		{name: "failure", failure: &driver.RunFailure{Code: driver.FailureAgentError}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cp := success.checkpointForOutcome(tc.exitCode, tc.signal, tc.timedOut, tc.failure); cp != nil {
				t.Fatalf("unsafe outcome produced checkpoint %#v", cp)
			}
		})
	}
	for name, stdout := range map[string]string{
		"init_only": "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n",
		"failed":    "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{\"type\":\"result\",\"subtype\":\"error_during_execution\",\"session_id\":\"s\"}\n",
		"malformed": "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\"}\n{broken\n{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\"}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if cp := parse(stdout).checkpoint(0); cp != nil {
				t.Fatalf("incomplete/failed/malformed protocol produced checkpoint %#v", cp)
			}
		})
	}
}
