// assistant-ui demonstrates recorded AG-UI history and optional active observation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui/aguisession"
	"github.com/agent-dance/agent-adaptor/v2/examples/internal/exampleutil"
	"github.com/agent-dance/agent-adaptor/v2/hosttools/sessionrecorder"
)

const defaultAddr = "127.0.0.1:8092"

type config struct {
	addr, dataDir, cors, agent string
	unattended, observation    bool
	pace                       time.Duration
	resolve                    func(*http.Request, aguisession.Access) (string, error)
}
type application struct {
	handler  http.Handler
	session  *aguisession.Session
	agent    *adaptor.Agent
	recorder sessionrecorder.EventRecorder
}

func newApplication(ctx context.Context, c config) (*application, error) {
	if c.dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	if c.pace < 0 {
		return nil, errors.New("pace must be nonnegative")
	}
	store, err := openDiskStore(filepath.Join(c.dataDir, "checkpoints"))
	if err != nil {
		return nil, err
	}
	backend, err := sessionrecorder.NewJSONLEventBackend(filepath.Join(c.dataDir, "events"))
	if err != nil {
		return nil, err
	}
	rec := sessionrecorder.NewEventRecorder(backend)
	var ai *adaptor.Agent
	health := healthInfo{Mode: "fake", Agent: "fake", Unattended: c.unattended, Observation: c.observation}
	if c.agent == "" || c.agent == "fake" {
		ai = adaptor.New(scriptedDriver{pace: c.pace}, adaptor.WithThreadStore(store))
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			_ = rec.Close()
			return nil, err
		}
		cfg := exampleutil.ResolveLiveAgentConfig(c.agent, os.Getenv("AGUI_MODEL"), "", cwd)
		cfg.Env, err = exampleutil.OperatorProfileEnv(cfg.Agent)
		if err != nil {
			_ = rec.Close()
			return nil, err
		}
		health.Mode, health.Agent = "real", cfg.Agent
		ai = adaptor.New(exampleutil.NewLiveDriver(cfg), adaptor.WithThreadStore(store), exampleutil.NonInteractive(cfg.Agent, adaptor.WorkspaceWrite))
	}
	resolve := c.resolve
	if resolve == nil {
		resolve = demoResolve
	}
	var projection []agui.EventTranslatorOption
	if health.Mode == "fake" {
		projection = []agui.EventTranslatorOption{agui.WithProjectionHook(scriptedActivity)}
	}
	decisions := newApprovals(resolve)
	session := aguisession.New(ai, aguisession.Options{Recorder: rec, Resolve: resolve, Events: projection,
		Unattended: c.unattended, BaseContext: ctx, KeepAlivePing: 10 * time.Second,
		Observe: decisions.observe, OnRunEnd: decisions.end})
	mux := http.NewServeMux()
	mux.Handle("/agent", session.Handler())
	// In-run guidance for the active run; delivery receipts stay on its stream.
	mux.Handle("/message", session.MessageHandler())
	// Human-in-the-loop: decisions appear as "dec.*" tool calls on the wire and
	// are answered through this host-owned companion endpoint.
	mux.Handle("/decision/pending", decisions.pendingHandler())
	mux.Handle("/decision/resolve", decisions.resolveHandler())
	mux.Handle("/history", session.MessagesHandler())
	mux.Handle("/events", session.EventsHandler())
	if c.observation {
		mux.Handle("/snapshot", session.SnapshotHandler())
		mux.Handle("/observe", session.ObserveHandler())
		mux.Handle("/cancel", session.CancelHandler())
	}
	mux.Handle("/health", healthHandler(health))
	return &application{handler: cors(c.cors, mux), session: session, agent: ai, recorder: rec}, nil
}

// healthInfo contains public demo configuration, never local profile paths.
type healthInfo struct {
	Mode        string `json:"mode"`
	Agent       string `json:"agent"`
	Unattended  bool   `json:"unattended"`
	Observation bool   `json:"observation"`
}

func healthHandler(info healthInfo) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info)
	})
}

// demoResolve deliberately permits every caller. Replace this function with
// principal authentication and per-operation authorization in production; use
// a collision-free tenant/principal/business-key encoding for the scoped key.
func demoResolve(_ *http.Request, a aguisession.Access) (string, error) {
	if a.ThreadID == "" {
		return "", &agui.InputError{Status: 400, Code: "demo.thread_required", Message: "threadId is required"}
	}
	return aguisession.ThreadKey(a.ThreadID), nil
}
func (a *application) Close(ctx context.Context) error {
	if err := a.session.Shutdown(ctx); err != nil {
		return err
	}
	return errors.Join(a.agent.Close(ctx), a.recorder.Close())
}
func cors(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func envOr(k, v string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return v
}
func envBool(k string) bool {
	v, err := strconv.ParseBool(envOr(k, "false"))
	if err != nil {
		log.Fatalf("%s: %v", k, err)
	}
	return v
}
func main() {
	c := config{}
	flag.StringVar(&c.addr, "addr", envOr("ADDR", defaultAddr), "HTTP listen address")
	flag.StringVar(&c.dataDir, "data-dir", envOr("AGUI_DATA_DIR", ".local/assistant-ui"), "single-process durable data directory")
	flag.StringVar(&c.cors, "cors-origin", envOr("CORS_ORIGIN", ""), "optional exact CORS origin")
	flag.StringVar(&c.agent, "agent", envOr("AGUI_AGENT", "fake"), "fake (default), codex, claude, cursor, codebuddy")
	flag.BoolVar(&c.unattended, "unattended", envBool("AGUI_UNATTENDED"), "allow execution after browser disconnect")
	flag.BoolVar(&c.observation, "observation", envBool("AGUI_OBSERVATION"), "enable Snapshot/Observe/Cancel HTTP endpoints")
	pace, err := time.ParseDuration(envOr("AGUI_PACE", "0s"))
	if err != nil {
		log.Fatal(err)
	}
	flag.DurationVar(&c.pace, "pace", pace, "delay per fake event, e.g. 100ms")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := newApplication(ctx, c)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: c.addr, Handler: app.handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("assistant-ui: http://%s agent=%s data=%s unattended=%t observation=%t", c.addr, c.agent, c.dataDir, c.unattended, c.observation)
	select {
	case err = <-done:
	case <-ctx.Done():
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closeErr := app.Close(closeCtx)
	shutdownErr := server.Shutdown(closeCtx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Print(err)
	}
	if closeErr != nil || shutdownErr != nil {
		log.Print(errors.Join(closeErr, shutdownErr))
		os.Exit(1)
	}
}
