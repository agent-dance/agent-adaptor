package a2adelegation

// Per-run MCP sidecar lifecycle (P4.6). This consolidates the hand-written
// runtime from examples/showcases/team-agent-workflow/delegation_runtime.go
// (random bearer token, loopback listener, http.Server lifecycle, serve-error
// drain, graceful shutdown) into the Service. MCPServerOptions carries a
// fixed RunID, so attribution is per run: one sidecar per RunID.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	sidecarReadHeaderTimeout = 10 * time.Second
	sidecarIdleTimeout       = 60 * time.Second
	sidecarShutdownTimeout   = 5 * time.Second
)

// Sidecar describes one live per-run MCP endpoint: the loopback URL the
// leader's driver should be pointed at (mcp_servers entry) and the bearer
// token that authenticates it.
type Sidecar struct {
	// RunID is the leader run this sidecar attributes delegations to.
	RunID string
	// URL is the Streamable-HTTP MCP endpoint (http://127.0.0.1:PORT/mcp).
	URL string
	// BearerToken authenticates requests (Authorization: Bearer <token>).
	BearerToken string
	// ToolTimeout is the effective per-delegation wall clock configured on
	// the Service, surfaced so hosts can align driver-side tool timeouts.
	ToolTimeout time.Duration
}

// runSidecar owns the OS resources behind one Sidecar.
type runSidecar struct {
	info     Sidecar
	server   *http.Server
	listener net.Listener
	serveErr chan error
}

// newRunSidecar mints a bearer token, binds a loopback listener, and serves
// the delegate_to_agent MCP endpoint for one run.
func newRunSidecar(delegator *Delegator, runID, tenant string, toolTimeout time.Duration) (*runSidecar, error) {
	token, err := randomBearerToken()
	if err != nil {
		return nil, fmt.Errorf("a2adelegation: generate sidecar token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("a2adelegation: listen for sidecar: %w", err)
	}
	mcp := NewMCPServer(delegator, MCPServerOptions{
		RunID:       runID,
		Tenant:      tenant,
		BearerToken: token,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.Handler())
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: sidecarReadHeaderTimeout,
		IdleTimeout:       sidecarIdleTimeout,
	}
	sc := &runSidecar{
		info: Sidecar{
			RunID:       runID,
			URL:         fmt.Sprintf("http://%s/mcp", listener.Addr().String()),
			BearerToken: token,
			ToolTimeout: toolTimeout,
		},
		server:   server,
		listener: listener,
		serveErr: make(chan error, 1),
	}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			sc.serveErr <- serveErr
		}
		close(sc.serveErr)
	}()
	return sc, nil
}

// close gracefully shuts the sidecar down (bounded Shutdown, hard Close
// fallback) and drains the serve goroutine's error.
func (s *runSidecar) close() error {
	if s == nil || s.server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), sidecarShutdownTimeout)
	defer cancel()
	shutdownErr := s.server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = s.server.Close()
	}
	var serveFailure error
	for err := range s.serveErr {
		if serveFailure == nil {
			serveFailure = err
		}
	}
	if shutdownErr != nil {
		return fmt.Errorf("a2adelegation: shutdown sidecar for run %s: %w", s.info.RunID, shutdownErr)
	}
	if serveFailure != nil {
		return fmt.Errorf("a2adelegation: sidecar for run %s: %w", s.info.RunID, serveFailure)
	}
	return nil
}

// randomBearerToken returns a 32-byte cryptographically random hex token.
func randomBearerToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
