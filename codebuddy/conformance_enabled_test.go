//go:build codebuddy_live

package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"os"
	"testing"
)

func codebuddyLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" {
		return false, adaptertest.SkipLiveRun("requires AGENT_ADAPTOR_LIVE_CONFORMANCE=1 in addition to codebuddy_live")
	}
	requireCodeBuddyCLI(t)
	return true, adaptertest.WithLiveRun("")
}
func codebuddyConformanceProfile(t *testing.T) string { t.Helper(); return isolatedConfigDir(t) }
