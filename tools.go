package adaptor

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/internal/toolruntime"
	"github.com/agent-dance/agent-adaptor/tool"
)

// configureTools validates the final construction-scope Tool declaration and
// prepares its immutable internal runtime projection. New cannot return an
// error, so declaration errors are retained and surfaced by openStream before
// Driver validation, resource acquisition, or provider launch.
func (a *Agent) configureTools() {
	if a == nil || a.defaults.tools == nil || len(*a.defaults.tools) == 0 {
		return
	}
	definitions := append([]tool.Definition(nil), (*a.defaults.tools)...)
	seen := make(map[string]struct{}, len(definitions))
	var missingRevision string
	for index, definition := range definitions {
		if definition == nil {
			a.toolConfigErr = fmt.Errorf("%w: nil definition at index %d", tool.ErrInvalidDefinition, index)
			return
		}
		descriptor, err := definition.Descriptor()
		if err != nil {
			a.toolConfigErr = err
			return
		}
		if _, duplicate := seen[descriptor.Name]; duplicate {
			a.toolConfigErr = fmt.Errorf("%w: duplicate tool name %q", tool.ErrInvalidDefinition, descriptor.Name)
			return
		}
		seen[descriptor.Name] = struct{}{}
		if missingRevision == "" && strings.TrimSpace(descriptor.Revision) == "" {
			missingRevision = descriptor.Name
		}
	}

	runtime, err := toolruntime.New(definitions)
	if err != nil {
		a.toolConfigErr = fmt.Errorf("prepare host-defined Tools: %w", err)
		return
	}
	a.toolRuntime = runtime
	a.toolProvider = &hostedToolProvider{
		runtime:     runtime,
		fingerprint: runtime.Fingerprint(),
	}
	if missingRevision != "" {
		a.toolThreadErr = &engine.SessionIncompatibleError{
			Reason: fmt.Sprintf("host-defined tool %q has no semantic revision", missingRevision),
		}
	}
}

// hostedToolProvider is deliberately private. Public callers install Tools
// only through construction-scope WithTools and cannot smuggle this stable
// Agent capability into one call through WithRunServices.
type hostedToolProvider struct {
	runtime     *toolruntime.Runtime
	fingerprint string
}

func (p *hostedToolProvider) AttachRun(ctx context.Context, _ string) (RunAttachment, error) {
	if p == nil || p.runtime == nil {
		return RunAttachment{}, toolruntime.ErrClosed
	}
	endpoint, err := p.runtime.Start(ctx)
	if err != nil {
		return RunAttachment{}, err
	}
	token, ok := p.runtime.BearerToken()
	if !ok {
		return RunAttachment{}, toolruntime.ErrClosed
	}
	mcpServer := driver.MCPServerSpec{
		Key:               toolruntime.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               endpoint.URL,
		BearerTokenEnvVar: endpoint.BearerTokenEnvVar,
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	return RunAttachment{Services: []ServiceRef{{
		ID:        toolruntime.ServerKey,
		Name:      toolruntime.ServerKey,
		URL:       endpoint.URL,
		Status:    driver.RuntimeServiceRunning,
		Lifecycle: driver.RuntimeLifecycleShared,
		ReuseKey:  p.fingerprint,
		// A live listener is observable, but no MCP initialize/list probe has
		// happened yet. Do not report an unobserved health check as success.
		Health: driver.RuntimeHealthUnknown,
		MCP:    &mcpServer,
		SecretEnv: []driver.EnvBinding{{
			Name:  endpoint.BearerTokenEnvVar,
			Value: token,
		}},
	}}}, nil
}

func (p *hostedToolProvider) bearerTokenEnvVar() string {
	if p == nil || p.runtime == nil {
		return ""
	}
	return p.runtime.BearerTokenEnvVar()
}

func (a *Agent) validateHostedToolsPreflight(eff *RunSettings, caps driver.MCPCapability) error {
	if a == nil || a.toolProvider == nil || eff == nil {
		return nil
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil || provider.bearerTokenEnvVar() == "" {
		return fmt.Errorf("host-defined Tool runtime has no credential carrier")
	}
	server := driver.MCPServerSpec{
		Key:               toolruntime.ServerKey,
		Transport:         driver.MCPTransportHTTP,
		URL:               "http://127.0.0.1:1/mcp",
		BearerTokenEnvVar: provider.bearerTokenEnvVar(),
		Required:          true,
		RequiredReason:    toolidentity.RequiredReason,
	}
	payload, err := engine.ResolveMCPPayloadWithRuntime(
		eff.engineMCPConfig(),
		nil,
		[]driver.RuntimeServiceRef{{ID: toolruntime.ServerKey, Name: toolruntime.ServerKey, MCP: &server}},
		caps,
	)
	if err != nil {
		return err
	}
	return a.validateHostedToolMCPAuthIsolation(payload)
}

// validateHostedToolMCPAuthIsolation prevents any other MCP endpoint from
// naming the private environment variable that carries this Agent's hosted
// Tool bearer token. It runs both in preflight and after runtime providers have
// attached, covering explicit WithMCP and typed runtime-service declarations.
func (a *Agent) validateHostedToolMCPAuthIsolation(payload driver.MCPPayload) error {
	if a == nil || a.toolProvider == nil {
		return nil
	}
	provider, ok := a.toolProvider.(*hostedToolProvider)
	if !ok || provider == nil {
		return nil
	}
	ownedEnv := provider.bearerTokenEnvVar()
	for _, server := range payload.Servers {
		if server.BearerTokenEnvVar == ownedEnv && server.Key != toolruntime.ServerKey {
			return fmt.Errorf("%w: MCP server %q aliases the Agent-owned hosted Tool bearer environment variable", engine.ErrInvalidMCPConfig, server.Key)
		}
	}
	return nil
}

func (*hostedToolProvider) DetachRun(context.Context, string) error { return nil }

var (
	_ RunServiceProvider = (*hostedToolProvider)(nil)
	_ ownedToolRuntime   = (*toolruntime.Runtime)(nil)
)
