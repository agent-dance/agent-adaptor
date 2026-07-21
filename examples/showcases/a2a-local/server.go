package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"time"

	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
)

const (
	agentCardPath = "/.well-known/agent-card.json"
	jsonRPCPath   = "/a2a"
)

func newHTTPServer(server *bridgea2a.Server) *http.Server {
	mux := http.NewServeMux()
	mux.Handle(agentCardPath, server.AgentCardHandler())
	mux.Handle(jsonRPCPath, exampleutil.WithRequestTimeout(server.Handler(), 3*time.Minute))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "agent-adaptor A2A demo\nagent card: %s\njson-rpc: %s\n", agentCardPath, jsonRPCPath)
	})
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

func shutdownServer(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func demoContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	base, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	if timeout <= 0 {
		return base, stop
	}
	ctx, cancel := context.WithTimeout(base, timeout)
	return ctx, func() {
		cancel()
		stop()
	}
}

func publicBaseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	switch host {
	case "", "::", "0.0.0.0":
		host = "localhost"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}
