package pi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
)

// Only the extension listener survives a turn. MCP connections, credentials,
// decisions and cancellation are rebound to the current invocation, then closed.
type rpcExtension struct {
	mu               sync.RWMutex
	active           *mcpBridge
	server           *http.Server
	dir, path, token string
	env              []driver.EnvBinding
}

func newRPCExtension(b *mcpBridge) (host *rpcExtension, err error) {
	if b == nil {
		return nil, nil
	}
	host = &rpcExtension{active: b}
	defer func(h *rpcExtension) {
		if err != nil {
			h.close()
		}
	}(host)
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return nil, err
	}
	host.token = hex.EncodeToString(secret)
	urlKey, tokenKey := "AGENT_ADAPTOR_PI_MCP_URL_"+host.token[:16], "AGENT_ADAPTOR_PI_MCP_TOKEN_"+host.token[:16]
	source, err := b.extensionSource(urlKey, tokenKey)
	if err != nil {
		return nil, err
	}
	host.dir, err = os.MkdirTemp("", "agent-adaptor-pi-rpc-")
	if err != nil {
		return nil, err
	}
	host.path = filepath.Join(host.dir, "tools.mjs")
	if err = os.WriteFile(host.path, []byte(source), 0600); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	host.server = &http.Server{Handler: http.HandlerFunc(host.serve), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = host.server.Serve(listener) }()
	host.env = []driver.EnvBinding{{Name: urlKey, Value: "http://" + listener.Addr().String()}, {Name: tokenKey, Value: host.token}}
	return host, nil
}

func (h *rpcExtension) bind(b *mcpBridge) {
	if h != nil {
		h.mu.Lock()
		h.active = b
		h.mu.Unlock()
	}
}
func (h *rpcExtension) close() {
	if h == nil {
		return
	}
	h.bind(nil)
	if h.server != nil {
		_ = h.server.Close()
	}
	if h.dir != "" {
		_ = os.RemoveAll(h.dir)
	}
}
func (h *rpcExtension) serve(w http.ResponseWriter, r *http.Request) {
	if !validBridgeRequest(w, r, h.token) {
		return
	}
	h.mu.RLock()
	b := h.active
	h.mu.RUnlock()
	if b == nil {
		http.Error(w, "no active invocation", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(b.ctx, cancel)
	defer func() { stop(); cancel() }()
	b.handle(w, r.WithContext(ctx))
}
