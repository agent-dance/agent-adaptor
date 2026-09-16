//go:build codex_live

package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/codex/internal/livetest"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
)

// This separate diagnostic uses only a literal dummy key and loopback HTTP.
// It proves CLI request routing, never model correctness or live conformance.
// It is deliberately outside the TestAlignmentLive acceptance selector.
func TestCodexLiveRouteProbe(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_CODEX_ROUTE_PROBE") != "1" {
		t.Skip("explicit dummy routing diagnostic only")
	}
	const dummy = "dummy-route-probe-no-real-credentials"
	var mu sync.Mutex
	var denied []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		denied = append(denied, r.Method+" "+r.Host)
		mu.Unlock()
		http.Error(w, "no forwarding", http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, proxy.URL)
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "exec", true: "appserver"}[streaming], func(t *testing.T) {
			seen := make(chan struct{}, 1)
			var targetMu sync.Mutex
			var methods []string
			wrongAuth := false
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetMu.Lock()
				methods = append(methods, r.Method+" "+r.URL.Path)
				wrongAuth = wrongAuth || r.Header.Get("Authorization") != "Bearer "+dummy
				targetMu.Unlock()
				http.Error(w, `{"error":{"message":"dummy routing proof only"}}`, http.StatusServiceUnavailable)
				if r.Method == http.MethodPost && r.URL.Path == "/v1/responses" {
					select {
					case seen <- struct{}{}:
					default:
					}
				}
			}))
			defer target.Close()
			seed := t.TempDir()
			if err := os.WriteFile(filepath.Join(seed, "auth.json"), []byte(`{"OPENAI_API_KEY":"`+dummy+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
			route := filepath.Join(t.TempDir(), "route.json")
			b, _ := json.Marshal(map[string]any{"version": 1, "model_provider": "route.fixture", "name": "Dummy route fixture", "base_url": target.URL + "/v1", "wire_api": "responses", "requires_openai_auth": true})
			if err := os.WriteFile(route, b, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_ADAPTOR_CODEX_LIVE_PROFILE", seed)
			t.Setenv(livetest.RouteEnv, route)
			cfg := alignmentLiveConfig(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				select {
				case <-seen:
					cancel()
				case <-ctx.Done():
				}
				close(done)
			}()
			result, err := (adapter{}).Run(ctx, driver.Request{Config: cfg, Prompt: "Reply OK.", Streaming: streaming, Policy: driver.RunPolicy{HumanDecision: driver.HumanDecisionPolicy{Permission: driver.HumanDecisionAutoApprove}}}, &testutil.EventRecorder{})
			cancel()
			<-done
			targetMu.Lock()
			defer targetMu.Unlock()
			t.Logf("transport=%v target_requests=%v auth_dummy_only=%v error_type=%T terminal_present=%v", streaming, methods, !wrongAuth, err, result.RawStreams != nil && result.RawStreams.Terminal != nil)
			found := false
			for _, method := range methods {
				found = found || method == "POST /v1/responses"
			}
			if !found || wrongAuth {
				if result.RawStreams != nil {
					t.Logf("dummy-only stderr=%s stdout=%s", result.RawStreams.Stderr, result.RawStreams.Stdout)
				}
				t.Fatal("selected route did not receive dummy model request")
			}
		})
	}
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(denied)
	t.Logf("denied_proxy_attempts=%v external_forwarding=false", denied)
	for _, request := range denied {
		if strings.Contains(request, "api.openai.com") {
			t.Fatal("model route escaped to the default endpoint")
		}
	}
}
