package adaptor_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/mcp"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/tool"
)

const hostedToolServerKey = "agent-adaptor-tools"

type hostedToolInput struct {
	Value string `json:"value" jsonschema:"required"`
}

type hostedToolOutput struct {
	Value string `json:"value"`
}

func hostedToolDefinition(name string, opts ...tool.Option) tool.Definition {
	return tool.Define(name, "Echo a value for the hosted Tool contract test.",
		func(_ context.Context, input hostedToolInput) (hostedToolOutput, error) {
			return hostedToolOutput{Value: input.Value}, nil
		}, opts...)
}

func toolCapableFake() *fakeDriver {
	fake := newFakeDriver()
	descriptor := fake.Descriptor()
	descriptor.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	fake.descriptor = &descriptor
	return fake
}

func TestWithToolsUsesStableExistingRuntimeMCPPipeline(t *testing.T) {
	fake := toolCapableFake()
	agent := adaptor.New(fake, adaptor.WithTools(
		hostedToolDefinition("echo", tool.ReadOnly(), tool.Revision("echo/v1")),
	))
	t.Cleanup(func() {
		if err := agent.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	for run := 0; run < 2; run++ {
		if _, err := agent.Run(context.Background(), "use echo"); err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
	}
	first := fake.request(t, 0)
	second := fake.request(t, 1)
	for index, request := range []driver.Request{first, second} {
		if len(request.MCP.Servers) != 1 {
			t.Fatalf("request %d MCP servers = %+v, want one hosted server", index+1, request.MCP.Servers)
		}
		server := request.MCP.Servers[0]
		if server.Key != hostedToolServerKey || server.Transport != driver.MCPTransportHTTP {
			t.Errorf("request %d server = %+v, want hosted HTTP server", index+1, server)
		}
		if !strings.HasPrefix(server.URL, "http://127.0.0.1:") || server.BearerTokenEnvVar == "" {
			t.Errorf("request %d endpoint/auth = %+v, want authenticated numeric loopback", index+1, server)
		}
		if len(request.Runtime.Ensured) != 1 || request.Runtime.Ensured[0].ReuseKey == "" {
			t.Errorf("request %d runtime = %+v, want catalog fingerprint in ReuseKey", index+1, request.Runtime)
		}
		if len(request.Runtime.SecretEnv) != 1 || request.Runtime.SecretEnv[0].Value == "" {
			t.Errorf("request %d secret env bindings = %d, want one non-empty private bearer binding", index+1, len(request.Runtime.SecretEnv))
		}
	}
	if first.MCP.Servers[0].URL != second.MCP.Servers[0].URL ||
		first.Runtime.Ensured[0].ReuseKey != second.Runtime.Ensured[0].ReuseKey ||
		first.Runtime.SecretEnv[0] != second.Runtime.SecretEnv[0] ||
		first.Runtime.Fingerprint == "" || first.Runtime.Fingerprint != second.Runtime.Fingerprint {
		t.Fatal("hosted Tool runtime identity changed between Agent runs")
	}
}

func TestWithToolsInvalidAndDuplicateDefinitionsFailBeforeDriver(t *testing.T) {
	tests := []struct {
		name        string
		definitions []tool.Definition
	}{
		{name: "invalid", definitions: []tool.Definition{hostedToolDefinition("")}},
		{name: "duplicate", definitions: []tool.Definition{
			hostedToolDefinition("echo"),
			hostedToolDefinition("echo"),
		}},
		{name: "nil", definitions: []tool.Definition{nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := toolCapableFake()
			agent := adaptor.New(fake, adaptor.WithTools(test.definitions...))
			t.Cleanup(func() { _ = agent.Close(context.Background()) })
			if _, err := agent.Run(context.Background(), "must not launch"); !errors.Is(err, tool.ErrInvalidDefinition) {
				t.Fatalf("Run error = %v, want tool.ErrInvalidDefinition", err)
			}
			if fake.runCount() != 0 {
				t.Fatalf("driver runs = %d, want 0", fake.runCount())
			}
		})
	}

	t.Run("later empty declaration clears earlier set", func(t *testing.T) {
		fake := toolCapableFake()
		agent := adaptor.New(fake,
			adaptor.WithTools(hostedToolDefinition("")),
			adaptor.WithTools(),
		)
		t.Cleanup(func() { _ = agent.Close(context.Background()) })
		if _, err := agent.Run(context.Background(), "launch without hosted tools"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := fake.request(t, 0).MCP.Servers; len(got) != 0 {
			t.Fatalf("MCP servers = %+v, want explicit WithTools clear", got)
		}
	})
}

func TestWithToolsIsIndependentOfPerCallWithMCPClearAndDetectsCollision(t *testing.T) {
	fake := toolCapableFake()
	agent := adaptor.New(fake, adaptor.WithTools(
		hostedToolDefinition("echo", tool.Revision("echo/v1")),
	))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })

	if _, err := agent.Run(context.Background(), "still has tools", adaptor.WithMCP()); err != nil {
		t.Fatalf("WithMCP clear run: %v", err)
	}
	if got := fake.request(t, 0).MCP.Servers; len(got) != 1 || got[0].Key != hostedToolServerKey {
		t.Fatalf("MCP servers after per-call clear = %+v, want hosted Tools server", got)
	}

	_, err := agent.Run(context.Background(), "collision",
		adaptor.WithMCP(mcp.HTTP(hostedToolServerKey, "https://example.com/mcp")),
	)
	if !errors.Is(err, adaptor.ErrInvalidMCPConfig) {
		t.Fatalf("collision error = %v, want ErrInvalidMCPConfig", err)
	}
	if fake.runCount() != 1 {
		t.Fatalf("driver runs = %d, want collision rejected before second launch", fake.runCount())
	}
}

func TestWithToolsRejectsBearerEnvAliasingFromMCPAndRunServices(t *testing.T) {
	fake := toolCapableFake()
	agent := adaptor.New(fake, adaptor.WithTools(
		hostedToolDefinition("echo", tool.Revision("echo/v1")),
	))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })

	if _, err := agent.Run(context.Background(), "learn resolved transport"); err != nil {
		t.Fatalf("initial Run: %v", err)
	}
	ownedEnv := fake.request(t, 0).MCP.Servers[0].BearerTokenEnvVar
	if ownedEnv == "" {
		t.Fatal("hosted Tool request has no bearer environment variable")
	}

	_, err := agent.Run(context.Background(), "must reject explicit alias",
		adaptor.WithMCP(mcp.HTTP("external", "https://example.com/mcp", mcp.WithBearerTokenEnv(ownedEnv))),
	)
	if !errors.Is(err, adaptor.ErrInvalidMCPConfig) {
		t.Fatalf("explicit MCP alias error = %v, want ErrInvalidMCPConfig", err)
	}

	provider := &fakeProvider{
		name: "external",
		log:  &callLog{},
		attachment: adaptor.RunAttachment{Services: []adaptor.ServiceRef{{
			ID:   "external",
			Name: "external",
			URL:  "https://example.com/mcp",
			MCP: &driver.MCPServerSpec{
				Key:               "external-runtime",
				Transport:         driver.MCPTransportHTTP,
				URL:               "https://example.com/mcp",
				BearerTokenEnvVar: ownedEnv,
			},
		}}},
	}
	_, err = agent.Run(context.Background(), "must reject runtime alias", adaptor.WithRunServices(provider))
	if !errors.Is(err, adaptor.ErrInvalidMCPConfig) {
		t.Fatalf("runtime MCP alias error = %v, want ErrInvalidMCPConfig", err)
	}
	if fake.runCount() != 1 {
		t.Fatalf("driver runs = %d, want only initial run", fake.runCount())
	}
}

func TestWithToolsRequiresDriverHTTPMCPSupportBeforeLaunch(t *testing.T) {
	fake := newFakeDriver()
	descriptor := fake.Descriptor()
	descriptor.MCP = driver.MCPCapability{Supported: true, Stdio: true}
	fake.descriptor = &descriptor
	agent := adaptor.New(fake, adaptor.WithTools(
		hostedToolDefinition("echo", tool.Revision("echo/v1")),
	))
	t.Cleanup(func() { _ = agent.Close(context.Background()) })
	if _, err := agent.Run(context.Background(), "must not launch"); !errors.Is(err, adaptor.ErrMCPTransportUnsupported) {
		t.Fatalf("Run error = %v, want ErrMCPTransportUnsupported", err)
	}
	if fake.runCount() != 0 {
		t.Fatalf("driver runs = %d, want 0", fake.runCount())
	}
}

func TestThreadWithToolsRequiresRevisionAndReusesStableRuntime(t *testing.T) {
	t.Run("missing revision fails closed", func(t *testing.T) {
		fake := toolCapableFake()
		agent := adaptor.New(fake,
			adaptor.WithThreadStore(memory.NewStore()),
			adaptor.WithTools(hostedToolDefinition("echo")),
		)
		t.Cleanup(func() { _ = agent.Close(context.Background()) })
		if _, err := agent.Thread("thread").Run(context.Background(), "must not launch"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
			t.Fatalf("Thread.Run error = %v, want ErrThreadIncompatible", err)
		}
		if fake.runCount() != 0 {
			t.Fatalf("driver runs = %d, want 0", fake.runCount())
		}
	})

	t.Run("stable revision resumes", func(t *testing.T) {
		fake := newSessionFake("tools")
		descriptor := fake.Descriptor()
		descriptor.MCP = driver.MCPCapability{Supported: true, HTTP: true}
		fake.descriptor = &descriptor
		agent := adaptor.New(fake,
			adaptor.WithThreadStore(memory.NewStore()),
			adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("echo/v1"))),
		)
		t.Cleanup(func() { _ = agent.Close(context.Background()) })
		thread := agent.Thread("thread")
		if _, err := thread.Run(context.Background(), "first"); err != nil {
			t.Fatalf("first: %v", err)
		}
		if _, err := thread.Run(context.Background(), "second"); err != nil {
			t.Fatalf("second: %v", err)
		}
		first := fake.request(t, 0)
		second := fake.request(t, 1)
		if second.Session == nil || second.Session.State == nil || second.Session.State.ResumeID == "" {
			t.Fatalf("second request did not resume: %+v", second.Session)
		}
		if first.MCP.Fingerprint != second.MCP.Fingerprint ||
			first.Runtime.Ensured[0].ReuseKey != second.Runtime.Ensured[0].ReuseKey {
			t.Fatal("Tool runtime compatibility identity changed between Thread turns")
		}
	})
}

func TestToolCatalogRevisionChangesThreadFingerprintAtStableEndpoint(t *testing.T) {
	fake := newSessionFake("tools-revision")
	descriptor := fake.Descriptor()
	descriptor.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	fake.descriptor = &descriptor
	store := memory.NewStore()

	firstAgent := adaptor.New(fake,
		adaptor.WithThreadStore(store),
		adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("echo/v1"))),
	)
	defer func() { _ = firstAgent.Close(context.Background()) }()
	if _, err := firstAgent.Thread("thread").Run(context.Background(), "first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := fake.request(t, 0)

	secondAgent := adaptor.New(fake,
		adaptor.WithThreadStore(store),
		adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("echo/v2"))),
	)
	defer func() { _ = secondAgent.Close(context.Background()) }()
	if _, err := secondAgent.Thread("thread").Run(context.Background(), "changed revision"); err != nil {
		t.Fatalf("changed revision: %v", err)
	}
	second := fake.request(t, 1)

	if first.MCP.Servers[0].URL != second.MCP.Servers[0].URL {
		t.Fatal("process-wide hosted Tool URL changed; test requires one shared gateway")
	}
	if first.MCP.Servers[0].BearerTokenEnvVar == second.MCP.Servers[0].BearerTokenEnvVar || first.MCP.Fingerprint == second.MCP.Fingerprint {
		t.Fatal("distinct Agents reused concrete hosted Tool credential transport identity")
	}
	if first.Runtime.Ensured[0].ReuseKey == second.Runtime.Ensured[0].ReuseKey {
		t.Fatal("catalog revision did not change the deterministic runtime compatibility identity")
	}
	if first.Runtime.Fingerprint == second.Runtime.Fingerprint {
		t.Fatal("final driver RuntimePayload fingerprint ignored the changed Tool attachment")
	}
	if first.ProfilePayload.SessionFingerprint() == second.ProfilePayload.SessionFingerprint() {
		t.Fatal("catalog revision did not change the session compatibility fingerprint")
	}
	if second.Session == nil || second.Session.State != nil {
		t.Fatalf("changed catalog resumed old provider state: %+v", second.Session)
	}
}

func TestToolCatalogResumesThreadAcrossAgentRestartAndEphemeralPortChange(t *testing.T) {
	stableAttachment := func() *fakeProvider {
		refs := make([]adaptor.ServiceRef, 0, 64)
		for index := 0; index < 64; index++ {
			refs = append(refs, adaptor.ServiceRef{
				ID:        fmt.Sprintf("stable-%02d", index),
				Name:      fmt.Sprintf("stable-%02d", index),
				URL:       fmt.Sprintf("https://runtime-%02d.example.test", index),
				Lifecycle: driver.RuntimeLifecycleShared,
				ReuseKey:  fmt.Sprintf("stable/v%d", index),
			})
		}
		return &fakeProvider{
			name:       "stable-services",
			attachment: adaptor.RunAttachment{Services: refs},
			log:        &callLog{},
		}
	}
	store := memory.NewStore()
	firstDriver := newSessionFake("tools-restart")
	firstDescriptor := firstDriver.Descriptor()
	firstDescriptor.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	firstDriver.descriptor = &firstDescriptor
	firstAgent := adaptor.New(firstDriver,
		adaptor.WithThreadStore(store),
		adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("echo/v1"))),
		adaptor.WithRunServices(stableAttachment()),
	)
	if _, err := firstAgent.Thread("thread").Run(context.Background(), "first"); err != nil {
		t.Fatalf("first: %v", err)
	}
	first := firstDriver.request(t, 0)
	if err := firstAgent.Close(context.Background()); err != nil {
		t.Fatalf("close first Agent: %v", err)
	}

	parsed, err := url.Parse(first.MCP.Servers[0].URL)
	if err != nil {
		t.Fatalf("parse first endpoint: %v", err)
	}
	portGuard, err := net.Listen("tcp4", parsed.Host)
	if err != nil {
		t.Fatalf("reserve former endpoint %q: %v", parsed.Host, err)
	}
	defer portGuard.Close()

	secondDriver := newSessionFake("tools-restart")
	secondDescriptor := secondDriver.Descriptor()
	secondDescriptor.MCP = driver.MCPCapability{Supported: true, HTTP: true}
	secondDriver.descriptor = &secondDescriptor
	secondAgent := adaptor.New(secondDriver,
		adaptor.WithThreadStore(store),
		adaptor.WithTools(hostedToolDefinition("echo", tool.Revision("echo/v1"))),
		adaptor.WithRunServices(stableAttachment()),
	)
	defer func() { _ = secondAgent.Close(context.Background()) }()
	if _, err := secondAgent.Thread("thread").Run(context.Background(), "second"); err != nil {
		t.Fatalf("second: %v", err)
	}
	second := secondDriver.request(t, 0)
	if first.MCP.Servers[0].URL == second.MCP.Servers[0].URL {
		t.Fatal("test did not force a new loopback endpoint")
	}
	if first.MCP.Fingerprint == second.MCP.Fingerprint {
		t.Fatal("concrete MCP materialization fingerprint ignored the new endpoint")
	}
	if first.Runtime.Fingerprint == second.Runtime.Fingerprint {
		t.Fatal("driver runtime fingerprint ignored the concrete endpoint change")
	}
	if first.ProfilePayload.Fingerprint == second.ProfilePayload.Fingerprint {
		t.Fatal("concrete ProfilePayload fingerprint ignored the new endpoint or credential carrier")
	}
	if first.ProfilePayload.SessionFingerprint() != second.ProfilePayload.SessionFingerprint() {
		t.Fatal("session Tool profile compatibility changed with only ephemeral transport allocation")
	}
	if second.Session == nil || second.Session.State == nil || second.Session.State.ResumeID == "" {
		t.Fatalf("second request did not resume the stored provider session: %+v", second.Session)
	}
}
