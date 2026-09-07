//go:build !codebuddy_live

package codebuddy

import (
	"github.com/agent-dance/agent-adaptor/adaptertest"
	"testing"
)

func codebuddyLiveGate(t *testing.T) (bool, adaptertest.Option) {
	t.Helper()
	return false, adaptertest.SkipLiveRun("requires codebuddy_live build tag and AGENT_ADAPTOR_LIVE_CONFORMANCE=1")
}
func codebuddyConformanceProfile(t *testing.T) string { t.Helper(); return t.TempDir() }
