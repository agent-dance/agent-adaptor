package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui"
	"github.com/agent-dance/agent-adaptor/v2/bridges/agui/aguisession"
	"github.com/agent-dance/agent-adaptor/v2/bridges/sse"
)

// approvals keeps the live ApprovalRequest responders observed in each run's
// drain. The AG-UI wire is one-way: it shows a decision as a "dec.*" tool call,
// and this host-owned companion endpoint answers it exactly once. Who may
// decide is a host policy, authorized here through the session resolver.
type approvals struct {
	mu      sync.Mutex
	pending map[string]map[string]*adaptor.ApprovalRequest
	resolve func(*http.Request, aguisession.Access) (string, error)
}

func newApprovals(resolve func(*http.Request, aguisession.Access) (string, error)) *approvals {
	return &approvals{pending: map[string]map[string]*adaptor.ApprovalRequest{}, resolve: resolve}
}

// observe is installed as aguisession.Options.Observe. It runs in the drain
// after the event is durably recorded, so a decision is answerable only once
// history already shows it.
func (a *approvals) observe(key string, ev adaptor.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch e := ev.(type) {
	case *adaptor.ApprovalRequest:
		if e == nil || e.ID == "" {
			return
		}
		if a.pending[key] == nil {
			a.pending[key] = map[string]*adaptor.ApprovalRequest{}
		}
		a.pending[key][e.ID] = e
	case adaptor.Notice:
		if e.Kind == adaptor.NoticeApprovalResolved {
			if id, _ := e.Data["request_id"].(string); id != "" {
				delete(a.pending[key], id)
			}
		}
	}
}

// end is installed as aguisession.Options.OnRunEnd: responders never outlive
// their run.
func (a *approvals) end(key string) {
	a.mu.Lock()
	delete(a.pending, key)
	a.mu.Unlock()
}

type pendingDecision struct {
	RequestID  string           `json:"requestId"`
	Kind       string           `json:"kind"`
	Title      string           `json:"title"`
	Source     string           `json:"source,omitempty"`
	ToolCallID string           `json:"toolCallId,omitempty"`
	Choices    []adaptor.Choice `json:"choices,omitempty"`
}

// pendingHandler serves GET /decision/pending?thread_id=T, authorized as a read.
func (a *approvals) pendingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key, err := a.resolve(r, aguisession.Access{Op: aguisession.OpRead, ThreadID: r.URL.Query().Get("thread_id")})
		if err != nil {
			writeInputError(w, err)
			return
		}
		a.mu.Lock()
		out := []pendingDecision{}
		for _, req := range a.pending[key] {
			out = append(out, pendingDecision{RequestID: req.ID, Kind: string(req.Kind), Title: req.Title,
				Source: req.Source, ToolCallID: req.ToolCallID, Choices: req.Choices})
		}
		a.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"pending": out})
	})
}

type decision struct {
	ThreadID  string `json:"threadId"`
	RequestID string `json:"requestId"`
	// Result is approve, deny or answer.
	Result string `json:"result"`
	// Text is the deny reason or a free-text answer; Choice selects a choice key.
	Text   string `json:"text,omitempty"`
	Choice string `json:"choice,omitempty"`
}

// resolveHandler serves POST /decision/resolve. It authorizes the decision as
// in-run input (OpMessage) and calls exactly one responder method. A repeated,
// expired or unknown request returns the SDK's stable approval error.
func (a *approvals) resolveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body decision
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&body)
		if err == nil && !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
			err = errors.New("a single JSON object is required")
		}
		if err == nil && (body.ThreadID == "" || body.RequestID == "" || body.Result == "") {
			err = errors.New("threadId, requestId and result are required")
		}
		if err != nil {
			writeInputError(w, &agui.InputError{Code: "demo.invalid_decision", Message: err.Error()})
			return
		}
		key, err := a.resolve(r, aguisession.Access{Op: aguisession.OpMessage, ThreadID: body.ThreadID})
		if err != nil {
			writeInputError(w, err)
			return
		}
		a.mu.Lock()
		req := a.pending[key][body.RequestID]
		a.mu.Unlock()
		if req == nil {
			sse.WriteApprovalError(w, adaptor.ErrApprovalResolved)
			return
		}
		switch strings.ToLower(body.Result) {
		case "approve":
			err = req.Approve(r.Context())
		case "deny":
			err = req.Deny(r.Context(), body.Text)
		case "answer":
			answer := body.Choice
			if answer == "" {
				answer = body.Text
			}
			err = req.Answer(r.Context(), answer)
		default:
			err = fmt.Errorf("%w: unsupported decision %q", adaptor.ErrApprovalKindMismatch, body.Result)
		}
		if err == nil || errors.Is(err, adaptor.ErrApprovalResolved) {
			a.mu.Lock()
			delete(a.pending[key], body.RequestID)
			a.mu.Unlock()
		}
		sse.WriteApprovalError(w, err)
	})
}

func writeInputError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var in *agui.InputError
	if errors.As(err, &in) {
		status = http.StatusBadRequest
		if in.Status != 0 {
			status = in.Status
		}
	} else {
		in = &agui.InputError{Code: "demo.request_failed", Message: err.Error()}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": in})
}
