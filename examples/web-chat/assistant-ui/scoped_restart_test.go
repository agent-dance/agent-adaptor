package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/agui"
	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"
)

func TestAuthenticatedScopedThreadSurvivesReconstruction(t *testing.T) {
	// A test-only authentication boundary stands in for verified middleware.
	// The allowlist applies to every Op; the public ID alone grants no access.
	scopedKey := func(principal, thread string) string {
		tuple, _ := json.Marshal([]string{"test-tenant", principal, thread})
		return fmt.Sprintf("scope-%x", sha256.Sum256(tuple))
	}
	resolve := func(r *http.Request, a aguisession.Access) (string, error) {
		principal := r.Header.Get("X-Test-Principal")
		if principal != "alice" || a.ThreadID != "shared-public-id" {
			return "", &agui.InputError{Status: 403, Code: "host.denied", Message: "principal has no grant"}
		}
		return scopedKey(principal, a.ThreadID), nil
	}
	ctx := context.Background()
	dir := t.TempDir()
	key := scopedKey("alice", "shared-public-id")
	if key == aguisession.ThreadKey("shared-public-id") || key == scopedKey("bob", "shared-public-id") {
		t.Fatal("key is not principal scoped")
	}
	c := config{dataDir: dir, observation: true, resolve: resolve}
	a := appForTest(t, c)
	send := func(app *application, principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Test-Principal", principal)
		w := httptest.NewRecorder()
		app.handler.ServeHTTP(w, r)
		return w
	}
	var prior []aguisession.Message
	for turn := 1; turn <= 2; turn++ {
		messages := append(append([]aguisession.Message{}, prior...), aguisession.Message{"id": fmt.Sprintf("user-%d", turn), "role": "user", "content": "continue"})
		raw, err := json.Marshal(map[string]any{"protocolVersion": "1.0", "threadId": "shared-public-id", "runId": fmt.Sprint(turn), "messages": messages})
		if err != nil {
			t.Fatal(err)
		}
		// Missing identity and a different authenticated principal cannot start,
		// read, follow or cancel, even when supplied the complete known history.
		for _, principal := range []string{"", "bob"} {
			for _, endpoint := range []struct{ method, path, body string }{
				{"POST", "/agent", string(raw)},
				{"GET", "/history?thread_id=shared-public-id", ""},
				{"GET", "/events?thread_id=shared-public-id", ""},
				{"GET", "/snapshot?thread_id=shared-public-id", ""},
				{"GET", "/observe?thread_id=shared-public-id", ""},
				{"POST", "/cancel?thread_id=shared-public-id", ""},
			} {
				w := send(a, principal, endpoint.method, endpoint.path, endpoint.body)
				if w.Code != 403 || !strings.Contains(w.Body.String(), "host.denied") || strings.Contains(w.Body.String(), "RUN_STARTED") {
					t.Fatalf("%s %s: %d %s", principal, endpoint.path, w.Code, w.Body)
				}
			}
		}
		w := send(a, "alice", "POST", "/agent", string(raw))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "RUN_FINISHED") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if turn == 2 && !strings.Contains(w.Body.String(), "same Thread using checkpoint 1") {
			t.Fatal("did not resume scoped checkpoint")
		}
		w = send(a, "alice", "GET", "/history?thread_id=shared-public-id", "")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &prior); err != nil {
			t.Fatal(err)
		}
		checkpoint, err := a.agent.Thread(key).Checkpoint(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Close(ctx); err != nil {
			t.Fatal(err)
		}
		a = appForTest(t, c) // reconstruct Agent, store, recorder and resolver between turns
		w = send(a, "alice", "GET", "/history?thread_id=shared-public-id", "")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		var restored []aguisession.Message
		if err := json.Unmarshal(w.Body.Bytes(), &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(prior, restored) {
			t.Fatal("scoped history changed across restart")
		}
		got, err := a.agent.Thread(key).Checkpoint(ctx)
		if err != nil || !reflect.DeepEqual(checkpoint, got) {
			t.Fatalf("scoped checkpoint changed: %v", err)
		}
	}
}
