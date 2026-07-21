package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	agentadaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/examples/internal/exampleutil"
	bridgea2a "github.com/agent-dance/agent-adaptor/pkg/bridges/a2a"
	clienta2a "github.com/agent-dance/agent-adaptor/pkg/clients/a2a"
	"github.com/agent-dance/agent-adaptor/pkg/hosttools/a2adelegation"
)

type roleHubConfig struct {
	Fixture     *workflowFixture
	Claude      exampleutil.LiveAgentConfig
	Codex       exampleutil.LiveAgentConfig
	RoleTimeout time.Duration
}

type roleDefinition struct {
	Key          string
	DisplayName  string
	Provider     string
	Instructions string
	Isolation    agentadaptor.IsolationLevel
	Config       exampleutil.LiveAgentConfig
}

type roleHub struct {
	BaseURL       string
	RoleEndpoints map[string]map[string]string
	Audit         *roleAudit
	server        *http.Server
	listener      net.Listener
	serveErr      chan error
}

func startRoleHub(cfg roleHubConfig) (*roleHub, []a2adelegation.RemoteAgentSpec, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("listen for A2A role hub: %w", err)
	}
	baseURL := (&url.URL{Scheme: "http", Host: listener.Addr().String()}).String()
	mux := http.NewServeMux()
	hub := &roleHub{
		BaseURL:       baseURL,
		RoleEndpoints: map[string]map[string]string{},
		Audit:         newRoleAudit(cfg.Fixture),
		listener:      listener,
		serveErr:      make(chan error, 1),
	}

	roles := []roleDefinition{
		{
			Key: "plan", DisplayName: "Codex planner", Provider: exampleutil.AgentCodex,
			Isolation: agentadaptor.IsolationReadOnly, Config: cfg.Codex,
			Instructions: "Act only as the planning stage. Inspect TASK.md, code, and tests. Do not modify files. Return a concise ordered plan with acceptance checks.",
		},
		{
			Key: "impl", DisplayName: "Claude Code implementer", Provider: exampleutil.AgentClaude,
			Isolation: agentadaptor.IsolationWorkspaceWrite, Config: cfg.Claude,
			Instructions: "Act only as the implementation stage. Use the supplied plan context, modify only slug.go, run go test ./... and git diff --check, and do not commit.",
		},
		{
			Key: "review", DisplayName: "Codex reviewer", Provider: exampleutil.AgentCodex,
			Isolation: agentadaptor.IsolationReadOnly, Config: cfg.Codex,
			Instructions: "Act only as the review stage. Do not modify files. Inspect TASK.md, slug.go, tests, and the diff. The host attaches authoritative go test, git diff --check, changed-file, and diff evidence before this run; evaluate that evidence and do not rerun the Go toolchain inside the provider sandbox. End with a line containing exactly TEAM_REVIEW_APPROVED only if every requirement is satisfied; otherwise end with TEAM_REVIEW_REJECTED.",
		},
	}

	remote := make([]a2adelegation.RemoteAgentSpec, 0, len(roles))
	for _, role := range roles {
		cardPath := fmt.Sprintf("/agents/%s/.well-known/agent-card.json", role.Key)
		rpcPath := fmt.Sprintf("/agents/%s/a2a", role.Key)
		binding := exampleutil.NewLiveAgentBinding(
			role.Config,
			cfg.Fixture.CloneProfileOption(role.Key+"-"+role.Provider),
			agentadaptor.WithDefaultWorkspace(agentadaptor.SharedWorkspace{}),
			agentadaptor.WithDefaultIdentity(agentadaptor.AgentIdentity{
				ID: "team-" + role.Key, TenantID: "example", ProfileID: role.Key + "-" + role.Provider, Name: role.DisplayName,
			}),
			agentadaptor.WithDefaultMetadata("example", "team-agent-workflow"),
			agentadaptor.WithDefaultMetadata("workflow_role", role.Key),
		)
		roleSDK := agentadaptor.New(agentadaptor.WithDefaultAgent(binding))
		server := bridgea2a.NewServer(observeRoleRunner(role.Key, roleSDK.Default(), hub.Audit.Record), bridgea2a.ServerOptions{
			AgentCard: bridgea2a.AgentCard{
				Name:        role.DisplayName,
				Description: fmt.Sprintf("%s role in the plan -> impl -> review team workflow.", role.Key),
				Version:     "1.0.0",
				URL:         baseURL + rpcPath,
				Skills: []bridgea2a.Skill{{
					ID: role.Key, Name: role.DisplayName, Description: role.Instructions,
					Tags: []string{"team-agent", role.Key, role.Provider},
				}},
			},
			Session: bridgea2a.Stateless(),
			Prompt:  rolePromptBuilder(role, cfg.Fixture),
			Exposure: bridgea2a.ExposurePolicy{
				IncludeToolCalls: true,
			},
			RunOptions: []agentadaptor.RunOption{
				exampleutil.NonInteractiveRunOption(role.Isolation),
			},
			TaskLifecycle: bridgea2a.TaskLifecycleOptions{
				Ephemeral: &bridgea2a.EphemeralTaskStoreOptions{MaxTasks: 32, TTL: 30 * time.Minute},
			},
		})
		mux.Handle(cardPath, server.AgentCardHandler())
		mux.Handle(rpcPath, server.Handler())
		hub.RoleEndpoints[role.Key] = map[string]string{
			"provider":   role.Provider,
			"agent_card": baseURL + cardPath,
			"json_rpc":   baseURL + rpcPath,
		}
		remote = append(remote, a2adelegation.RemoteAgentSpec{
			Key:          role.Key,
			DisplayName:  role.DisplayName,
			Protocol:     a2adelegation.ProtocolA2A,
			AgentCardURL: baseURL + cardPath,
			PreferredTransports: []clienta2a.TransportProtocol{
				clienta2a.TransportJSONRPC,
			},
			Policy: a2adelegation.DelegationPolicy{
				MaxTimeout: cfg.RoleTimeout, RequireStreaming: true, MaxArtifactBytes: 1 << 20,
			},
		})
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	hub.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		err := hub.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		hub.serveErr <- err
	}()
	return hub, remote, nil
}

type observedRoleRunner struct {
	role   string
	next   agentadaptor.Runner
	record func(string)
}

func observeRoleRunner(role string, next agentadaptor.Runner, record func(string)) agentadaptor.Runner {
	return observedRoleRunner{role: role, next: next, record: record}
}

func (r observedRoleRunner) Run(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunResult, error) {
	started := time.Now()
	term.Logf("[role] %s run started", r.role)
	result, err := r.next.Run(ctx, prompt, opts...)
	logRoleResult(r.role, started, result, err)
	if r.record != nil {
		r.record(r.role)
	}
	return result, err
}

func (r observedRoleRunner) Start(ctx context.Context, prompt string, opts ...agentadaptor.RunOption) (agentadaptor.RunHandle, error) {
	handle, err := r.next.Start(ctx, prompt, opts...)
	if err != nil {
		return nil, err
	}
	term.Logf("[role] %s run_id=%s started", r.role, handle.RunID())
	return observedRoleHandle{RunHandle: handle, role: r.role, started: time.Now(), record: r.record}, nil
}

type observedRoleHandle struct {
	agentadaptor.RunHandle
	role    string
	started time.Time
	record  func(string)
}

func (h observedRoleHandle) Wait(ctx context.Context) (agentadaptor.RunResult, error) {
	result, err := h.RunHandle.Wait(ctx)
	logRoleResult(h.role, h.started, result, err)
	if h.record != nil {
		h.record(h.role)
	}
	return result, err
}

type roleAudit struct {
	mu      sync.Mutex
	fixture *workflowFixture
	digests map[string]string
	errors  map[string]error
}

func newRoleAudit(fixture *workflowFixture) *roleAudit {
	audit := &roleAudit{fixture: fixture, digests: map[string]string{}, errors: map[string]error{}}
	audit.Record("initial")
	return audit
}

func (a *roleAudit) Record(role string) {
	digest, err := a.fixture.Digest()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.errors[role] = err
		return
	}
	a.digests[role] = digest
}

func (a *roleAudit) ValidateStageBoundaries() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, role := range []string{"initial", "plan", "impl", "review", "final"} {
		if err := a.errors[role]; err != nil {
			return fmt.Errorf("snapshot workspace after %s: %w", role, err)
		}
		if a.digests[role] == "" {
			return fmt.Errorf("missing workspace snapshot after %s", role)
		}
	}
	if a.digests["initial"] != a.digests["plan"] {
		return errors.New("plan stage modified the workspace")
	}
	if a.digests["plan"] == a.digests["impl"] {
		return errors.New("implementation stage did not change the workspace")
	}
	if a.digests["impl"] != a.digests["review"] {
		return errors.New("review role modified the workspace")
	}
	if a.digests["review"] != a.digests["final"] {
		return errors.New("leader modified the workspace after review")
	}
	return nil
}

func logRoleResult(role string, started time.Time, result agentadaptor.RunResult, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	failure := ""
	if result.Failure != nil {
		failure = result.Failure.Message
	}
	term.Logf("[role] %s completed elapsed=%s error=%q failure=%q", role, time.Since(started).Round(time.Millisecond), errText, failure)
}

func rolePromptBuilder(role roleDefinition, fixture *workflowFixture) bridgea2a.PromptBuilder {
	return bridgea2a.PromptBuilderFunc(func(ctx context.Context, req bridgea2a.InboundRequest) (string, []agentadaptor.RunOption, error) {
		var body strings.Builder
		for _, part := range req.Message.Parts {
			if part.Kind == bridgea2a.PartText {
				body.WriteString(part.Text)
				body.WriteString("\n")
			}
		}
		if strings.TrimSpace(body.String()) == "" {
			return "", nil, errors.New("delegated role prompt is empty")
		}
		evidence := ""
		if role.Key == "review" {
			var err error
			evidence, err = buildReviewEvidence(ctx, fixture)
			if err != nil {
				return "", nil, err
			}
		}
		prompt := fmt.Sprintf("Role boundary:\n%s\n\nDelegated objective and context:\n%s%s", role.Instructions, strings.TrimSpace(body.String()), evidence)
		return prompt, nil, nil
	})
}

func buildReviewEvidence(ctx context.Context, fixture *workflowFixture) (string, error) {
	validation, err := fixture.Validate(ctx)
	if err != nil {
		return "", fmt.Errorf("prepare review validation evidence: %w", err)
	}
	diff, err := runIn(ctx, fixture.WorkspaceDir, "git", "diff", "--no-ext-diff", "--unified=80", "HEAD", "--", "slug.go")
	if err != nil {
		return "", fmt.Errorf("prepare review diff evidence: %w: %s", err, diff)
	}
	return fmt.Sprintf(`

Host-verified review evidence (generated immediately before this A2A run):
- go test ./...: %s
- git diff --check: %s
- changed files: %s

Authoritative git diff:
%s
`, validation.Tests, validation.DiffCheck, strings.Join(validation.ChangedFiles, ", "), diff), nil
}

func (h *roleHub) Close() error {
	if h == nil || h.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := h.server.Shutdown(ctx)
	if err != nil {
		_ = h.server.Close()
	}
	serveErr := <-h.serveErr
	if err != nil {
		return err
	}
	return serveErr
}
