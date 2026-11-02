package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/tool"
)

type gateInput struct {
	Gate string `json:"gate"`
}

type gateArrival struct {
	Role string
	Gate string
	At   time.Time
}

type gateResult struct {
	Role     string `json:"role"`
	Gate     string `json:"gate"`
	Released bool   `json:"released"`
}

type gateKey struct {
	role string
	gate string
}

type gateState struct {
	arrival  gateArrival
	arrived  chan struct{}
	released chan struct{}
	open     bool
}

// gates coordinates actual tool calls with the host. Each role/gate pair is a
// one-use checkpoint; waiting or releasing alone never records an arrival.
type gates struct {
	mu          sync.Mutex
	entries     map[gateKey]*gateState
	allReleased bool
}

func newGates() *gates {
	return &gates{entries: make(map[gateKey]*gateState)}
}

func (g *gates) stateLocked(role, name string) *gateState {
	key := gateKey{role: role, gate: name}
	state := g.entries[key]
	if state == nil {
		state = &gateState{arrived: make(chan struct{}), released: make(chan struct{})}
		if g.allReleased {
			close(state.released)
			state.open = true
		}
		g.entries[key] = state
	}
	return state
}

func validGate(role, name string) error {
	if strings.TrimSpace(role) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("checkpoint requires a role and gate name")
	}
	return nil
}

func (g *gates) toolFor(role string) tool.Definition {
	return tool.Define("host_gate", "Pause at a named checkpoint until the host releases it. Your role is assigned by the host.",
		func(ctx context.Context, input gateInput) (gateResult, error) {
			if err := validGate(role, input.Gate); err != nil {
				return gateResult{}, err
			}
			g.mu.Lock()
			if err := ctx.Err(); err != nil {
				g.mu.Unlock()
				return gateResult{}, err
			}
			state := g.stateLocked(role, input.Gate)
			if state.arrival.At.IsZero() {
				state.arrival = gateArrival{Role: role, Gate: input.Gate, At: time.Now().UTC()}
				close(state.arrived)
			}
			released := state.released
			g.mu.Unlock()
			select {
			case <-ctx.Done():
				return gateResult{}, ctx.Err()
			case <-released:
				if err := ctx.Err(); err != nil {
					return gateResult{}, err
				}
				return gateResult{Role: role, Gate: input.Gate, Released: true}, nil
			}
		},
		tool.Revision("multica-routing-gate-v1/"+role), tool.ReadOnly(), tool.ClosedWorld(),
		tool.InputSchemaJSON([]byte(`{"type":"object","properties":{"gate":{"type":"string","minLength":1}},"required":["gate"],"additionalProperties":false}`)),
	)
}

// wait observes the first handler invocation for this exact role/gate pair.
func (g *gates) wait(ctx context.Context, role, name string) (gateArrival, error) {
	if err := validGate(role, name); err != nil {
		return gateArrival{}, err
	}
	g.mu.Lock()
	state := g.stateLocked(role, name)
	g.mu.Unlock()
	select {
	case <-ctx.Done():
		return gateArrival{}, ctx.Err()
	case <-state.arrived:
		if err := ctx.Err(); err != nil {
			return gateArrival{}, err
		}
		g.mu.Lock()
		arrival := state.arrival
		g.mu.Unlock()
		return arrival, nil
	}
}

func (g *gates) release(role, name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.stateLocked(role, name)
	if !state.open {
		state.open = true
		close(state.released)
	}
}

// releaseAll also releases future checkpoints so deferred cleanup cannot leave
// a late provider tool invocation waiting for a host that has already stopped.
func (g *gates) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.allReleased = true
	for _, state := range g.entries {
		if !state.open {
			state.open = true
			close(state.released)
		}
	}
}
