// Package toolruntime exposes an immutable provider-neutral Tool catalog over
// a process-wide, authenticated loopback gateway. MCP is deliberately confined
// to this internal package.
package toolruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-dance/agent-adaptor/internal/toolidentity"
	"github.com/agent-dance/agent-adaptor/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// ServerKey is the reserved MCP server key used by every in-process tool
	// catalog. A process-wide gateway lets concurrent Agents safely materialize
	// the same URL into a shared provider profile.
	ServerKey = toolidentity.ServerKey

	defaultMaxRequestBodyBytes = 1 << 20
	defaultMaxResponseBytes    = 4 << 20
	defaultMaxConcurrent       = 32
	defaultHandlerTimeout      = 2 * time.Minute
	defaultReadHeaderTimeout   = 5 * time.Second
	defaultIdleTimeout         = 30 * time.Second
	defaultShutdownTimeout     = 10 * time.Second
	defaultMaxHeaderBytes      = 16 << 10
	serverVersion              = "1"
	fingerprintDomain          = "github.com/agent-dance/agent-adaptor/internal/toolruntime/catalog/v1"
)

var (
	// ErrClosed means a registration can no longer be started or used.
	ErrClosed = errors.New("tool runtime is closed")
	// ErrInvalidCatalog means a Catalog cannot be represented by the internal
	// MCP server. The original SDK panic is deliberately not exposed because it
	// may contain descriptor or schema contents.
	ErrInvalidCatalog = errors.New("invalid tool catalog")
	processGateway    = newGatewayManager(defaultGatewayConfig())
)

// Endpoint contains only non-secret connection metadata.
type Endpoint struct {
	URL               string
	BearerTokenEnvVar string
}

// Runtime is one Agent's immutable catalog registration in the process-wide
// gateway. Start is lazy so listener failures remain pre-launch run errors.
type Runtime struct {
	manager     *gatewayManager
	catalog     *runtimeCatalog
	fingerprint string
	// bearerTokenEnvVar is a non-secret but unpredictable Agent registration
	// name. Keeping it distinct across Agents prevents an unrelated MCP server
	// from aliasing the hosted Tool credential carrier.
	bearerTokenEnvVar string

	mu           sync.Mutex
	started      bool
	closed       bool
	endpoint     Endpoint
	token        string
	registration *registration

	closeStarted bool
	closeDone    chan struct{}
	closeErr     error
}

// String intentionally omits credentials. Runtime occasionally appears in
// diagnostic structs during pre-launch failures, and formatting it must not
// turn an Agent's bearer token into a log value.
func (r *Runtime) String() string {
	if r == nil {
		return "toolruntime.Runtime<nil>"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("toolruntime.Runtime{started:%t, closed:%t, endpoint:%q, fingerprint:%q}",
		r.started, r.closed, r.endpoint.URL, r.fingerprint)
}

// GoString provides the same redaction for %#v formatting.
func (r *Runtime) GoString() string { return r.String() }

// New validates and freezes the definitions' MCP projection without opening a
// socket. The tool package owns typed decoding, JSON Schema validation, and
// handler immutability; this package adds the sorted catalog fingerprint.
func New(definitions []tool.Definition) (*Runtime, error) {
	return newRuntime(processGateway, definitions)
}

func newRuntime(manager *gatewayManager, definitions []tool.Definition) (*Runtime, error) {
	projection, fingerprint, err := newRuntimeCatalog(definitions, manager.config)
	if err != nil {
		return nil, err
	}
	bearerTokenEnvVar, err := newBearerTokenEnvVar()
	if err != nil {
		return nil, fmt.Errorf("create tool runtime credential name: %w", err)
	}
	return &Runtime{
		manager:           manager,
		catalog:           projection,
		fingerprint:       fingerprint,
		bearerTokenEnvVar: bearerTokenEnvVar,
		closeDone:         make(chan struct{}),
	}, nil
}

// Fingerprint returns the provider-neutral catalog compatibility identity.
// It never contains the bearer token or handler/closure addresses.
func (r *Runtime) Fingerprint() string {
	if r == nil {
		return ""
	}
	return r.fingerprint
}

// Start registers the catalog once and returns the process-wide stable
// endpoint. A failed bind does not poison the Runtime, so preparation may be
// retried before a provider prompt is delivered.
func (r *Runtime) Start(ctx context.Context) (Endpoint, error) {
	if r == nil {
		return Endpoint{}, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Endpoint{}, ErrClosed
	}
	if r.started {
		return r.endpoint, nil
	}

	var lastErr error
	for range 3 {
		token, err := newBearerToken()
		if err != nil {
			return Endpoint{}, fmt.Errorf("create tool runtime credential: %w", err)
		}
		endpoint, registration, err := r.manager.register(ctx, token, r.catalog)
		if err == nil {
			endpoint.BearerTokenEnvVar = r.bearerTokenEnvVar
			r.started = true
			r.endpoint = endpoint
			r.token = token
			r.registration = registration
			return endpoint, nil
		}
		if !errors.Is(err, errTokenCollision) {
			return Endpoint{}, err
		}
		lastErr = err
	}
	return Endpoint{}, fmt.Errorf("register tool catalog: %w", lastErr)
}

func newBearerToken() (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(secret[:]), nil
}

func newBearerTokenEnvVar() (string, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return toolidentity.BearerTokenEnvVarPrefix + strings.ToUpper(hex.EncodeToString(suffix[:])), nil
}

// BearerTokenEnvVar returns this Agent registration's unpredictable,
// non-secret credential carrier name. It is available before Start so MCP
// declarations can be checked for aliasing before the listener is opened.
func (r *Runtime) BearerTokenEnvVar() string {
	if r == nil {
		return ""
	}
	return r.bearerTokenEnvVar
}

// Endpoint returns the shared non-secret endpoint after Start.
func (r *Runtime) Endpoint() (Endpoint, bool) {
	if r == nil {
		return Endpoint{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.endpoint, r.started && !r.closed
}

// BearerToken returns this registration's secret. It is intended only for
// subprocess SecretEnv injection and is excluded from Endpoint, errors, logs,
// results, and fingerprints.
func (r *Runtime) BearerToken() (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started || r.closed {
		return "", false
	}
	return r.token, true
}

// Close unregisters this Agent's token, drains its active calls, and shuts
// down the process-wide listener only when it was the final registration.
// It is idempotent and safe for concurrent callers.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.beginClose()
	select {
	case <-r.closeDone:
		return r.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) beginClose() {
	r.mu.Lock()
	if r.closeStarted {
		r.mu.Unlock()
		return
	}
	r.closeStarted = true
	r.closed = true
	registration := r.registration
	token := r.token
	r.token = ""
	r.mu.Unlock()

	if registration == nil {
		close(r.closeDone)
		return
	}
	retirement := r.manager.retire(token, registration)
	go func() {
		err := retirement.cleanup(r.manager.config.shutdownTimeout)
		r.mu.Lock()
		r.closeErr = err
		r.mu.Unlock()
		close(r.closeDone)
	}()
}

type runtimeCatalog struct {
	server *mcp.Server
	byName map[string]tool.Definition
	config gatewayConfig
}

func newRuntimeCatalog(definitions []tool.Definition, config gatewayConfig) (catalog *runtimeCatalog, fingerprint string, err error) {
	descriptors := make([]tool.Descriptor, 0, len(definitions))
	byName := make(map[string]tool.Definition, len(definitions))
	for index, definition := range definitions {
		if definition == nil {
			return nil, "", fmt.Errorf("%w: nil definition at index %d", ErrInvalidCatalog, index)
		}
		descriptor, descriptorErr := definition.Descriptor()
		if descriptorErr != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrInvalidCatalog, descriptorErr)
		}
		if _, exists := byName[descriptor.Name]; exists {
			return nil, "", fmt.Errorf("%w: duplicate tool name %q", ErrInvalidCatalog, descriptor.Name)
		}
		descriptors = append(descriptors, descriptor)
		byName[descriptor.Name] = definition
	}
	if len(descriptors) == 0 {
		return nil, "", fmt.Errorf("%w: catalog is empty", ErrInvalidCatalog)
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].Name < descriptors[j].Name })
	fingerprint, err = catalogFingerprint(descriptors)
	if err != nil {
		return nil, "", fmt.Errorf("%w: fingerprint failed", ErrInvalidCatalog)
	}
	defer func() {
		if recover() != nil {
			catalog = nil
			fingerprint = ""
			err = ErrInvalidCatalog
		}
	}()
	server := mcp.NewServer(
		&mcp.Implementation{Name: ServerKey, Version: serverVersion},
		&mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		},
	)
	catalog = &runtimeCatalog{server: server, byName: byName, config: config}
	for _, descriptor := range descriptors {
		definition := &mcp.Tool{
			Name:         descriptor.Name,
			Title:        descriptor.Title,
			Description:  descriptor.Description,
			InputSchema:  json.RawMessage(slices.Clone(descriptor.InputSchemaJSON)),
			OutputSchema: json.RawMessage(slices.Clone(descriptor.OutputSchemaJSON)),
			Annotations:  mcpAnnotations(descriptor),
		}
		mcp.AddTool[json.RawMessage, any](server, definition, catalog.handler(descriptor.Name))
	}
	return catalog, fingerprint, nil
}

func mcpAnnotations(descriptor tool.Descriptor) *mcp.ToolAnnotations {
	annotations := &mcp.ToolAnnotations{Title: descriptor.Title}
	if descriptor.Annotations.ReadOnly != nil {
		annotations.ReadOnlyHint = *descriptor.Annotations.ReadOnly
	}
	if descriptor.Annotations.Idempotent != nil {
		annotations.IdempotentHint = *descriptor.Annotations.Idempotent
	}
	annotations.DestructiveHint = cloneBool(descriptor.Annotations.Destructive)
	annotations.OpenWorldHint = cloneBool(descriptor.Annotations.OpenWorld)
	return annotations
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func catalogFingerprint(descriptors []tool.Descriptor) (string, error) {
	type annotations struct {
		ReadOnly    *bool `json:"read_only,omitempty"`
		Destructive *bool `json:"destructive,omitempty"`
		Idempotent  *bool `json:"idempotent,omitempty"`
		OpenWorld   *bool `json:"open_world,omitempty"`
	}
	type descriptor struct {
		Name        string          `json:"name"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Revision    string          `json:"revision"`
		Input       json.RawMessage `json:"input_schema"`
		Output      json.RawMessage `json:"output_schema"`
		Annotations annotations     `json:"annotations"`
	}
	entries := make([]descriptor, len(descriptors))
	for index, value := range descriptors {
		entries[index] = descriptor{
			Name:        value.Name,
			Title:       value.Title,
			Description: value.Description,
			Revision:    value.Revision,
			Input:       slices.Clone(value.InputSchemaJSON),
			Output:      slices.Clone(value.OutputSchemaJSON),
			Annotations: annotations{
				ReadOnly:    cloneBool(value.Annotations.ReadOnly),
				Destructive: cloneBool(value.Annotations.Destructive),
				Idempotent:  cloneBool(value.Annotations.Idempotent),
				OpenWorld:   cloneBool(value.Annotations.OpenWorld),
			},
		}
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(fingerprintDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(encoded)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (c *runtimeCatalog) handler(name string) mcp.ToolHandlerFor[json.RawMessage, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input json.RawMessage) (
		result *mcp.CallToolResult, output any, err error,
	) {
		defer func() {
			if recover() != nil {
				result = failureResult("internal_error", "Tool execution failed.")
				output = nil
				err = nil
			}
		}()
		handlerCtx, cancel := context.WithTimeout(ctx, c.config.handlerTimeout)
		defer cancel()
		definition := c.byName[name]
		if definition == nil {
			return failureResult("internal_error", "Tool execution failed."), nil, nil
		}
		out, invokeErr := definition.Invoke(handlerCtx, slices.Clone(input))
		if invokeErr != nil {
			code, message, rejected := tool.AsRejection(invokeErr)
			switch {
			case rejected:
				if validRejection(code, message) {
					return failureResult(code, message), nil, nil
				}
				return failureResult("internal_error", "Tool execution failed."), nil, nil
			case errors.Is(invokeErr, context.DeadlineExceeded), errors.Is(handlerCtx.Err(), context.DeadlineExceeded):
				return failureResult("deadline_exceeded", "Tool execution timed out."), nil, nil
			case errors.Is(invokeErr, context.Canceled), errors.Is(handlerCtx.Err(), context.Canceled):
				return failureResult("canceled", "Tool execution was canceled."), nil, nil
			default:
				return failureResult("internal_error", "Tool execution failed."), nil, nil
			}
		}
		if len(out) == 0 || !json.Valid(out) || len(out) > c.config.maxResponseBytes {
			return failureResult("internal_error", "Tool execution failed."), nil, nil
		}
		return nil, json.RawMessage(slices.Clone(out)), nil
	}
}

func validRejection(code, message string) bool {
	if len(code) == 0 || len(code) > 64 || len(message) == 0 || len(message) > 4096 {
		return false
	}
	for _, char := range code {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func failureResult(code, message string) *mcp.CallToolResult {
	encoded, _ := json.Marshal(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		IsError: true,
	}
}

type gatewayConfig struct {
	maxRequestBodyBytes int64
	maxResponseBytes    int
	maxConcurrent       int
	handlerTimeout      time.Duration
	readHeaderTimeout   time.Duration
	idleTimeout         time.Duration
	shutdownTimeout     time.Duration
	maxHeaderBytes      int
}

func defaultGatewayConfig() gatewayConfig {
	return gatewayConfig{
		maxRequestBodyBytes: defaultMaxRequestBodyBytes,
		maxResponseBytes:    defaultMaxResponseBytes,
		maxConcurrent:       defaultMaxConcurrent,
		handlerTimeout:      defaultHandlerTimeout,
		readHeaderTimeout:   defaultReadHeaderTimeout,
		idleTimeout:         defaultIdleTimeout,
		shutdownTimeout:     defaultShutdownTimeout,
		maxHeaderBytes:      defaultMaxHeaderBytes,
	}
}

type gatewayManager struct {
	mu       sync.Mutex
	config   gatewayConfig
	instance *gateway
}

func newGatewayManager(config gatewayConfig) *gatewayManager {
	return &gatewayManager{config: config}
}

var errTokenCollision = errors.New("tool runtime token collision")

func (m *gatewayManager) register(ctx context.Context, token string, catalog *runtimeCatalog) (Endpoint, *registration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance == nil {
		instance, err := newGateway(ctx, m.config)
		if err != nil {
			return Endpoint{}, nil, err
		}
		m.instance = instance
	}
	registration, err := m.instance.register(token, catalog)
	if err != nil {
		return Endpoint{}, nil, err
	}
	return m.instance.endpoint, registration, nil
}

func (m *gatewayManager) retire(token string, registration *registration) *retirement {
	m.mu.Lock()
	instance := m.instance
	if instance == nil {
		m.mu.Unlock()
		registration.beginClose()
		return &retirement{registration: registration}
	}
	last := instance.unregister(token, registration)
	var stopErr error
	if last {
		m.instance = nil
		// Stop accepting connections before publishing the manager as empty.
		// Existing requests remain alive for the retirement worker to drain,
		// while a later Agent may safely create the next sole listener.
		stopErr = instance.stopAdmission()
	}
	m.mu.Unlock()
	return &retirement{
		registration: registration,
		gateway:      instance,
		last:         last,
		stopErr:      stopErr,
	}
}

type retirement struct {
	registration *registration
	gateway      *gateway
	last         bool
	stopErr      error
}

func (r *retirement) cleanup(timeout time.Duration) error {
	if r == nil || r.registration == nil {
		return nil
	}
	if !r.last || r.gateway == nil {
		<-r.registration.drained
		return r.stopErr
	}
	return errors.Join(r.stopErr, r.gateway.shutdownAndDrain(r.registration, timeout))
}

type gateway struct {
	config       gatewayConfig
	endpoint     Endpoint
	expectedHost string
	listener     net.Listener
	server       *http.Server
	semaphore    chan struct{}

	mu            sync.RWMutex
	registrations map[string]*registration
	serveDone     chan struct{}
	serveErr      error
}

func newGateway(ctx context.Context, config gatewayConfig) (*gateway, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start tool runtime listener: %w", err)
	}
	address := listener.Addr().String()
	gateway := &gateway{
		config:        config,
		endpoint:      Endpoint{URL: "http://" + address + "/mcp"},
		expectedHost:  address,
		listener:      listener,
		semaphore:     make(chan struct{}, config.maxConcurrent),
		registrations: make(map[string]*registration),
		serveDone:     make(chan struct{}),
	}
	protocol := mcp.NewStreamableHTTPHandler(
		func(request *http.Request) *mcp.Server {
			registration, _ := request.Context().Value(registrationContextKey{}).(*registration)
			if registration == nil {
				return nil
			}
			return registration.catalog.server
		},
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			MaxRequestBodyBytes:          config.maxRequestBodyBytes,
			PropagateRequestCancellation: true,
		},
	)
	gateway.server = &http.Server{
		Handler:           gateway.secureHandler(protocol),
		ReadHeaderTimeout: config.readHeaderTimeout,
		ReadTimeout:       config.handlerTimeout,
		WriteTimeout:      config.handlerTimeout + config.readHeaderTimeout,
		IdleTimeout:       config.idleTimeout,
		MaxHeaderBytes:    config.maxHeaderBytes,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go gateway.serve()
	return gateway, nil
}

func (g *gateway) register(token string, catalog *runtimeCatalog) (*registration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.serveErr != nil {
		return nil, g.serveErr
	}
	if _, exists := g.registrations[token]; exists {
		return nil, errTokenCollision
	}
	registration := newRegistration(catalog)
	g.registrations[token] = registration
	return registration, nil
}

func (g *gateway) unregister(token string, expected *registration) bool {
	g.mu.Lock()
	if current := g.registrations[token]; current == expected {
		delete(g.registrations, token)
		expected.beginClose()
	}
	last := len(g.registrations) == 0
	g.mu.Unlock()
	return last
}

type registrationContextKey struct{}

func (g *gateway) secureHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recover() != nil {
				http.Error(response, "Internal Server Error", http.StatusInternalServerError)
			}
		}()
		if request.URL.Path != "/mcp" {
			http.NotFound(response, request)
			return
		}
		if request.Host != g.expectedHost || !validOrigins(request.Header.Values("Origin"), g.expectedHost) {
			http.Error(response, "Forbidden", http.StatusForbidden)
			return
		}
		registration := g.authenticate(request.Header.Values("Authorization"))
		if registration == nil {
			response.Header().Set("WWW-Authenticate", `Bearer realm="agent-adaptor-tools"`)
			http.Error(response, "Unauthorized", http.StatusUnauthorized)
			return
		}
		defer registration.release()
		select {
		case g.semaphore <- struct{}{}:
			defer func() { <-g.semaphore }()
		default:
			response.Header().Set("Retry-After", "1")
			http.Error(response, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		// The Tool handler owns the execution deadline. Applying the same
		// deadline to the carrier HTTP request races result serialization: the
		// handler can turn a timeout into a normal model-visible Tool failure,
		// only for the outer context to close the response first. Keep transport
		// cancellation flowing inward and bound only the actual Tool execution.
		ctx, cancel := context.WithCancel(request.Context())
		stopRegistrationCancel := context.AfterFunc(registration.ctx, cancel)
		defer func() {
			stopRegistrationCancel()
			cancel()
		}()
		ctx = context.WithValue(ctx, registrationContextKey{}, registration)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func (g *gateway) authenticate(values []string) *registration {
	if len(values) != 1 {
		return nil
	}
	presented := []byte(strings.TrimPrefix(values[0], "Bearer "))
	if !strings.HasPrefix(values[0], "Bearer ") {
		// Still perform a same-sized comparison below when registrations exist;
		// this keeps the invalid scheme off the shortcut path.
		presented = []byte(values[0])
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	var selected *registration
	for token, registration := range g.registrations {
		if subtle.ConstantTimeCompare(presented, []byte(token)) == 1 {
			selected = registration
		}
	}
	if selected == nil || !selected.acquire() {
		return nil
	}
	return selected
}

func validOrigins(origins []string, expectedHost string) bool {
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	origin := origins[0]
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && parsed.User == nil &&
		parsed.Host == expectedHost && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func (g *gateway) serve() {
	err := g.server.Serve(g.listener)
	g.mu.Lock()
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !isClosedNetworkError(err) {
		g.serveErr = fmt.Errorf("serve tool runtime: %w", err)
	}
	close(g.serveDone)
	g.mu.Unlock()
}

func (g *gateway) stopAdmission() error {
	err := g.listener.Close()
	// Some Windows net.Listener implementations return the legacy unwrapped
	// poll error whose text is net.ErrClosed instead of participating in
	// errors.Is. Either form proves admission was already fenced.
	if isClosedNetworkError(err) {
		return nil
	}
	return err
}

func isClosedNetworkError(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	// Older poll implementations expose an unwrap chain but use a distinct
	// sentinel value with net.ErrClosed's canonical text.
	for current := err; current != nil; current = errors.Unwrap(current) {
		if current.Error() == net.ErrClosed.Error() {
			return true
		}
	}
	return false
}

func (g *gateway) shutdownAndDrain(registration *registration, timeout time.Duration) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	err := g.server.Shutdown(shutdownCtx)
	cancel()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if err != nil {
		closeErr := g.server.Close()
		if closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			err = errors.Join(err, closeErr)
		}
		// A bounded graceful-shutdown deadline is an internal transition to
		// forced transport close, not a permanent Runtime.Close failure. Keep
		// waiting for the host handler below so a later Close can succeed once
		// the handler actually exits.
		if errors.Is(err, context.DeadlineExceeded) {
			err = closeErr
		}
	}
	<-registration.drained
	<-g.serveDone
	g.mu.RLock()
	serveErr := g.serveErr
	g.mu.RUnlock()
	return errors.Join(err, serveErr)
}

type registration struct {
	catalog *runtimeCatalog
	ctx     context.Context
	cancel  context.CancelFunc

	mu        sync.Mutex
	closing   bool
	active    int
	drained   chan struct{}
	drainOnce sync.Once
}

func newRegistration(catalog *runtimeCatalog) *registration {
	ctx, cancel := context.WithCancel(context.Background())
	return &registration{catalog: catalog, ctx: ctx, cancel: cancel, drained: make(chan struct{})}
}

func (r *registration) acquire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return false
	}
	r.active++
	return true
}

func (r *registration) release() {
	r.mu.Lock()
	r.active--
	if r.closing && r.active == 0 {
		r.drainOnce.Do(func() { close(r.drained) })
	}
	r.mu.Unlock()
}

func (r *registration) beginClose() {
	r.mu.Lock()
	r.closing = true
	r.cancel()
	if r.active == 0 {
		r.drainOnce.Do(func() { close(r.drained) })
	}
	r.mu.Unlock()
}
