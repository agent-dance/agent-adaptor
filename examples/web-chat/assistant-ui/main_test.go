package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/bridges/agui/aguisession"
	"github.com/agent-dance/agent-adaptor/threadstore"
)

func appForTest(t *testing.T, c config) *application {
	t.Helper()
	a, err := newApplication(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return a
}
func postTurn(t *testing.T, a *application, thread, run string, prior []aguisession.Message) *httptest.ResponseRecorder {
	t.Helper()
	ms := append(append([]aguisession.Message{}, prior...), aguisession.Message{"id": "user-" + run, "role": "user", "content": "continue"})
	b, err := json.Marshal(map[string]any{"protocolVersion": "1.0", "threadId": thread, "runId": run, "messages": ms})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest("POST", "/agent", strings.NewReader(string(b))))
	return w
}
func TestScriptedThreadSurvivesReconstruction(t *testing.T) {
	dir := t.TempDir()
	a := appForTest(t, config{dataDir: dir})
	key := aguisession.ThreadKey("scoped/thread")
	type trace struct {
		RunID   string                `json:"runId"`
		Wire    string                `json:"wire"`
		History []aguisession.Message `json:"history"`
	}
	var traces []trace
	for turn := 1; turn <= 2; turn++ {
		prior, err := a.session.Messages(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		w := postTurn(t, a, "scoped/thread", fmt.Sprint(turn), prior)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), `"protocolVersion":"1.0"`) {
			t.Fatal("missing producer version")
		}
		if turn == 1 {
			if strings.Count(w.Body.String(), `"type":"TOOL_CALL_RESULT"`) != 13 || !strings.Contains(w.Body.String(), `"exitCode":1`) || !strings.Contains(w.Body.String(), `"status":"failed"`) {
				t.Fatal("missing failed tool 13")
			}
		} else if !strings.Contains(w.Body.String(), "same Thread using checkpoint 1") {
			t.Fatal("did not resume checkpoint")
		}
		history, err := a.session.Messages(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if turn == 1 {
			assertScriptedActivity(t, w.Body.String(), history)
		}
		traces = append(traces, trace{RunID: fmt.Sprint(turn), Wire: w.Body.String(), History: history})
	}
	before, err := a.session.Messages(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := a.agent.Thread(key).Checkpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := appForTest(t, config{dataDir: dir})
	after, err := b.session.Messages(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history changed on restart")
	}
	restored, err := b.agent.Thread(key).Checkpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoint, restored) {
		t.Fatal("checkpoint changed on restart")
	}
	w := postTurn(t, b, "scoped/thread", "3", after)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Turn 3 continues turn 1 on the same Thread using checkpoint 2") {
		t.Fatalf("restart continuation: %d %s", w.Code, w.Body)
	}
	history, err := b.session.Messages(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	traces = append(traces, trace{RunID: "3", Wire: w.Body.String(), History: history})
	// Optional handoff to the offline official-client test. Never rewrites a
	// committed golden; the caller supplies a temporary output file.
	if path := os.Getenv("AGUI_EXAMPLE_TRACE"); path != "" {
		raw, err := json.Marshal(traces)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestExampleEndpointGatesAndInputErrors(t *testing.T) {
	a := appForTest(t, config{dataDir: t.TempDir(), cors: "http://localhost:5173"})
	if defaultAddr != "127.0.0.1:8092" {
		t.Fatal("listen must be loopback")
	}
	for _, path := range []string{"/snapshot", "/observe", "/cancel"} {
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("%s enabled by default", path)
		}
	}
	for _, body := range []string{`{`, `{"protocolVersion":"1.0","threadId":"t","runId":"r","messages":[{"id":"u","role":"tool","toolCallId":"tc","content":"result"}]}`} {
		w := httptest.NewRecorder()
		a.handler.ServeHTTP(w, httptest.NewRequest("POST", "/agent", strings.NewReader(body)))
		if w.Code != 400 || strings.Contains(w.Body.String(), "RUN_STARTED") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
			t.Fatal("CORS")
		}
	}
	b := appForTest(t, config{dataDir: t.TempDir(), observation: true})
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, httptest.NewRequest("GET", "/snapshot?thread_id=t", nil))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
}
func TestCheckpointCorruptionAndWriteFailureAreExplicit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "threads.json"), []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDiskStore(dir); err == nil {
		t.Fatal("corrupt store accepted")
	}
	s, err := openDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "k", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Rename onto a directory fails on every platform, including root.
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	req := threadstore.FinalizeRequest{Record: threadstore.Record{ID: "r", Key: "k", Status: threadstore.StatusActive}, HeldLeases: []threadstore.Lease{lease}, Key: "k", RebindActive: true}
	if err = s.Finalize(ctx, req); err == nil {
		t.Fatal("write failure hidden")
	}
	if r, err := s.Resolve(ctx, threadstore.Query{Key: "k"}); err != nil || r != nil {
		t.Fatalf("failed save became visible: %v %v", r, err)
	}
}
func TestDiskStoreLeaseAtomicity(t *testing.T) {
	s, err := openDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "k", "one", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcquireLease(ctx, "k", "two", time.Minute); !errors.Is(err, threadstore.ErrBusy) {
		t.Fatal(err)
	}
	req := threadstore.FinalizeRequest{Record: threadstore.Record{ID: "first", Key: "k", Status: threadstore.StatusActive}, Key: "k", RebindActive: true, RequireKeyAbsent: true, HeldLeases: []threadstore.Lease{lease}}
	if err = s.Finalize(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.Record.ID = "second"
	if err = s.Finalize(ctx, req); !errors.Is(err, threadstore.ErrAlreadyExists) {
		t.Fatal(err)
	}
	req.RequireKeyAbsent = false
	req.PreviousID = "first"
	req.ArchiveOld = true
	if err = s.Finalize(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	newer, err := s.AcquireLease(ctx, "k", "two", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Finalize(ctx, req); !errors.Is(err, threadstore.ErrLeaseLost) {
		t.Fatal(err)
	}
	if err = s.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err = s.RenewLease(ctx, newer, time.Minute); err != nil {
		t.Fatal(err)
	}
	reopened, err := openDiskStore(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reopened.Resolve(ctx, threadstore.Query{Key: "k"})
	if err != nil || r == nil || r.ID != "second" {
		t.Fatalf("%+v %v", r, err)
	}
	old, err := reopened.Resolve(ctx, threadstore.Query{ID: "first", IncludeArchived: true})
	if err != nil || old == nil || old.Status != threadstore.StatusArchived {
		t.Fatalf("%+v %v", old, err)
	}
}

func TestMissingCheckpointRejectsRecordedHistory(t *testing.T) {
	dir := t.TempDir()
	a := appForTest(t, config{dataDir: dir})
	key := aguisession.ThreadKey("t")
	w := postTurn(t, a, "t", "first", nil)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	prior, err := a.session.Messages(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(dir, "checkpoints/threads.json")); err != nil {
		t.Fatal(err)
	}
	b := appForTest(t, config{dataDir: dir})
	w = postTurn(t, b, "t", "second", prior)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "agui.history_unrecoverable") || strings.Contains(w.Body.String(), "RUN_STARTED") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestExampleUnattendedDisconnectAndHostCancel(t *testing.T) {
	a := appForTest(t, config{dataDir: t.TempDir(), unattended: true, observation: true, pace: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/agent", strings.NewReader(`{"protocolVersion":"1.0","threadId":"paced","runId":"paced-run","messages":[{"id":"u","role":"user","content":"go"}]}`)).WithContext(ctx)
	returned := make(chan struct{})
	go func() { a.handler.ServeHTTP(w, request); close(returned) }()
	key := aguisession.ThreadKey("paced")
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, err := a.session.Snapshot(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveRun != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("POST did not detach")
	}
	// Observer disconnect leaves the worker-owned run alive until success.
	deadline = time.Now().Add(10 * time.Second)
	for {
		snap, err := a.session.Snapshot(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveRun == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, err := a.session.Events(context.Background(), key, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for _, r := range events {
		for _, e := range r.Events {
			last = string(e.Type())
		}
	}
	if last != "RUN_FINISHED" {
		t.Fatalf("disconnect terminal %s", last)
	}
	// A new key is used because cancelled runs deliberately cannot attest a
	// healthy provider checkpoint for their newly recorded incomplete turn.
	w = httptest.NewRecorder()
	done := make(chan struct{})
	cancelRequest := httptest.NewRequest("POST", "/agent", strings.NewReader(`{"protocolVersion":"1.0","threadId":"cancelled","runId":"cancel","messages":[{"id":"cancel-user","role":"user","content":"go"}]}`))
	go func() { defer close(done); a.handler.ServeHTTP(httptest.NewRecorder(), cancelRequest) }()
	deadline = time.Now().Add(10 * time.Second)
	for {
		snap, err := a.session.Snapshot(context.Background(), aguisession.ThreadKey("cancelled"))
		if err != nil {
			t.Fatal(err)
		}
		if snap.ActiveRun != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel run did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.handler.ServeHTTP(w, httptest.NewRequest("POST", "/cancel?thread_id=cancelled", nil))
	if w.Code != 204 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not drain")
	}
	events, err = a.session.Events(context.Background(), aguisession.ThreadKey("cancelled"), 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"code":"run.cancelled"`) {
		t.Fatal(string(b))
	}
}

func TestHealthExposesModeWithoutLocalConfiguration(t *testing.T) {
	a := appForTest(t, config{dataDir: t.TempDir()})
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	var got healthInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "fake" || got.Agent != "fake" {
		t.Fatalf("wrong default health: %+v", got)
	}
	w = httptest.NewRecorder()
	healthHandler(healthInfo{Mode: "real", Agent: "codex", Unattended: true, Observation: true}).ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "real" || got.Agent != "codex" || !got.Unattended || !got.Observation {
		t.Fatalf("real mode health: %+v", got)
	}
	if strings.Contains(w.Body.String(), "HOME") || strings.Contains(w.Body.String(), "command") || strings.Contains(w.Body.String(), "cwd") {
		t.Fatal("private configuration in health")
	}
}
