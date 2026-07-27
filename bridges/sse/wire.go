// Package sse exposes HTTP Server-Sent Events handlers for the adaptor v1
// Runner/Event/Result contracts. It deliberately owns only HTTP and SSE wire
// translation; execution, thread coordination, and approval policy remain in
// the adaptor core and host.
package sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Protocol selects the full on-wire contract (inbound + outbound).
type Protocol int

const (
	// AGUI is the default. Inbound requests and outbound SSE events use the
	// AG-UI protocol.
	AGUI Protocol = iota
	// Raw accepts {"prompt","sessionKey"} and emits one typed adaptor event
	// per SSE frame.
	Raw
)

// RawRequest is the canonical Raw-protocol request body. SessionKey is one
// opaque host key; the bridge preserves it byte-for-byte.
type RawRequest struct {
	Prompt     string `json:"prompt"`
	SessionKey string `json:"sessionKey,omitempty"`
}

func decodeRawRequest(r *http.Request) (*RawRequest, error) {
	if r.Method == http.MethodGet {
		return &RawRequest{
			Prompt:     r.URL.Query().Get("prompt"),
			SessionKey: r.URL.Query().Get("sessionKey"),
		}, nil
	}
	defer r.Body.Close()
	var req RawRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("request body is empty")
		}
		return nil, fmt.Errorf("decode request: %w", err)
	}
	return &req, nil
}

func runKeepAlive(ctx context.Context, writeMu *sync.Mutex, w io.Writer, flusher http.Flusher, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			writeMu.Lock()
			_, err := io.WriteString(w, ":keep-alive\n\n")
			if err == nil {
				flusher.Flush()
			}
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func escapeEventName(name string) string {
	if name == "" {
		return "payload"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r':
			return ' '
		default:
			return r
		}
	}, name)
}
