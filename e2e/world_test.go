//go:build e2e

package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/claude"
	"github.com/agent-dance/agent-adaptor/codebuddy"
	"github.com/agent-dance/agent-adaptor/codex"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/profile"
	"github.com/cucumber/godog"
)

type providerName string

const (
	providerClaude    providerName = "claude"
	providerCodex     providerName = "codex"
	providerCodeBuddy providerName = "codebuddy"
)

type cliInfo struct {
	realPath string
	version  string
}

type turnObservation struct {
	result    *adaptor.Result
	events    []adaptor.Event
	spawnPIDs []int
	resumeID  string
}

type scenarioWorld struct {
	root      string
	workspace string
	provider  providerName
	cli       cliInfo
	store     *memory.Store
	agent     *adaptor.Agent
	thread    *adaptor.Thread
	threadKey string
	tokenA    string
	spawn     bool
	turns     []turnObservation
	allPIDs   map[int]struct{}
	closed    bool
}

type worldContextKey struct{}

func newScenarioWorld() (*scenarioWorld, error) {
	root, err := os.MkdirTemp("", "agent-adaptor-real-cli-e2e-")
	if err != nil {
		return nil, err
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &scenarioWorld{
		root: root, workspace: workspace, store: memory.NewStore(),
		threadKey: "persistent/" + randomHex(8),
		tokenA:    "AAE2E-" + strings.ToUpper(randomHex(12)),
		allPIDs:   map[int]struct{}{},
	}, nil
}

func worldFrom(ctx context.Context) (*scenarioWorld, error) {
	world, ok := ctx.Value(worldContextKey{}).(*scenarioWorld)
	if !ok || world == nil {
		return nil, errors.New("BDD scenario world is not initialized")
	}
	return world, nil
}

func (w *scenarioWorld) selectProvider(name string) error {
	provider := providerName(strings.ToLower(strings.TrimSpace(name)))
	switch provider {
	case providerClaude, providerCodex, providerCodeBuddy:
	default:
		return fmt.Errorf("unsupported provider %q", name)
	}
	if w.provider != "" && w.provider != provider {
		return fmt.Errorf("provider already set to %q", w.provider)
	}
	w.provider = provider
	return nil
}

func (w *scenarioWorld) resolveRealCLI(ctx context.Context) error {
	if w.cli.realPath != "" {
		return nil
	}
	if w.provider == "" {
		return errors.New("provider must be selected first")
	}
	path, err := exec.LookPath(string(w.provider))
	if err != nil {
		return environmentUnavailable(ctx, w.provider, "CLI is not present in PATH")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	realPath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 || isForbiddenTestCommand(w.root, realPath) {
		return fmt.Errorf("resolved command is not an allowed real CLI: %s", realPath)
	}
	versionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	versionBytes, err := exec.CommandContext(versionCtx, realPath, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("read %s version: %w", w.provider, err)
	}
	version := firstNonEmptyLine(string(versionBytes))
	if version == "" {
		return fmt.Errorf("%s --version returned empty output", w.provider)
	}
	w.cli = cliInfo{realPath: realPath, version: version}
	godog.Logf(ctx, "real CLI: driver=%s path=%s version=%q", w.provider, realPath, version)
	return nil
}

func isForbiddenTestCommand(scenarioRoot, command string) bool {
	clean, root := filepath.Clean(command), filepath.Clean(scenarioRoot)
	if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return true
	}
	base := strings.ToLower(filepath.Base(clean))
	if strings.HasSuffix(base, ".test") {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".sh", ".bash", ".zsh", ".fish", ".cmd", ".bat", ".ps1":
		return true
	default:
		return false
	}
}

func (w *scenarioWorld) ensureAgent(ctx context.Context) error {
	if w.agent != nil {
		return nil
	}
	if err := w.resolveRealCLI(ctx); err != nil {
		return err
	}
	if runtime.GOOS == "windows" && !w.spawn {
		return environmentUnavailable(ctx, w.provider, "persistent real-CLI smoke is POSIX-only")
	}
	common := driver.CommonConfig{
		Command: w.cli.realPath, CWD: w.workspace, GracePeriod: 5 * time.Second,
	}
	approvals := adaptor.ApprovalPolicy{
		Permission: adaptor.ApprovalAutoApprove,
		PlanReview: adaptor.ApprovalAutoApprove,
		Question:   adaptor.QuestionAutoDeny,
	}
	var d driver.Driver
	var policy adaptor.Policy
	switch w.provider {
	case providerClaude:
		d = claude.Driver(claude.Config{
			CommonConfig: common,
			Model:        envOr("AGENT_ADAPTOR_E2E_CLAUDE_MODEL", "claude-haiku-4"),
		})
		policy = adaptor.Policy{Approvals: approvals}
	case providerCodeBuddy:
		configDir, err := w.cloneCodeBuddyAuth()
		if err != nil {
			return err
		}
		common.Env = append(common.Env, driver.EnvBinding{Name: "CODEBUDDY_CONFIG_DIR", Value: configDir})
		d = codebuddy.Driver(codebuddy.Config{
			CommonConfig: common,
			Model:        envOr("AGENT_ADAPTOR_E2E_CODEBUDDY_MODEL", "glm-5.2-ioa"),
		})
		policy = adaptor.Policy{Approvals: approvals}
	case providerCodex:
		d = codex.Driver(codexE2EConfig(common))
		policy = adaptor.Policy{
			Sandbox: adaptor.ReadOnly,
			Approvals: adaptor.ApprovalPolicy{
				Permission: adaptor.ApprovalAutoApprove,
			},
		}
	}
	opts := w.agentOptions(policy)
	w.agent = adaptor.New(d, opts...)
	w.thread = w.agent.Thread(w.threadKey)
	report, err := w.agent.Inspect().Environment(ctx)
	if err != nil {
		return fmt.Errorf("%s environment check: %w", w.provider, err)
	}
	if !report.Healthy {
		return environmentUnavailable(ctx, w.provider, "driver environment check failed")
	}
	return nil
}

func codexE2EConfig(common driver.CommonConfig) codex.Config {
	return codex.Config{
		CommonConfig: common,
		// An empty override deliberately leaves model selection to the native
		// Codex profile. Custom providers frequently expose a different model
		// catalogue; AGENT_ADAPTOR_E2E_CODEX_MODEL remains the explicit escape
		// hatch when the acceptance run needs to pin one.
		Model: strings.TrimSpace(os.Getenv("AGENT_ADAPTOR_E2E_CODEX_MODEL")),
	}
}

func (w *scenarioWorld) agentOptions(policy adaptor.Policy) []adaptor.Option {
	opts := []adaptor.Option{
		adaptor.WithWorkspace(w.workspace),
		adaptor.WithThreadStore(w.store),
		adaptor.WithPolicy(policy),
	}
	if w.provider == providerCodex {
		// Auth and provider routing are one profile contract. Clone settings so
		// model_provider/base_url stay paired with the credential, but keep all
		// mutable Codex state under the scenario root. LinkAuth avoids copying a
		// refreshable login into an independently diverging credential file.
		opts = append(opts, adaptor.WithProfile(profile.CloneNative(
			filepath.Join(w.root, "codex-profile"),
			profile.CopySettings(),
			profile.LinkAuth(),
		)))
	}
	if w.spawn {
		opts = append(opts, adaptor.WithSpawn())
	}
	return opts
}

func (w *scenarioWorld) cloneCodeBuddyAuth() (string, error) {
	destination := filepath.Join(w.root, "codebuddy-config")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	source := envOr("CODEBUDDY_CONFIG_DIR_SOURCE", filepath.Join(home, ".codebuddy"))
	for _, name := range []string{".credentials.json", "credentials.json", "settings.json"} {
		data, readErr := os.ReadFile(filepath.Join(source, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if readErr != nil {
			return "", readErr
		}
		if err := os.WriteFile(filepath.Join(destination, name), data, 0o600); err != nil {
			return "", err
		}
	}
	return destination, nil
}

func (w *scenarioWorld) runTurn(ctx context.Context, prompt string) error {
	if err := w.ensureAgent(ctx); err != nil {
		return err
	}
	turnCtx, cancel := context.WithTimeout(ctx, e2eTurnTimeout())
	defer cancel()
	stream := w.thread.Stream(turnCtx, prompt)
	observation := turnObservation{}
	var eventMu sync.Mutex
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for event := range stream.Events() {
			eventMu.Lock()
			observation.events = append(observation.events, event)
			if process, ok := event.(adaptor.ProcessInfo); ok && process.Kind == adaptor.ProcessSpawn {
				if pid := valuePID(process.Data["pid"]); pid > 0 {
					observation.spawnPIDs = append(observation.spawnPIDs, pid)
				}
			}
			eventMu.Unlock()
		}
	}()
	result, runErr := stream.Result()
	<-drained
	observation.result = result
	if result != nil {
		checkpoint, checkpointErr := w.thread.Checkpoint(ctx)
		if checkpointErr == nil && checkpoint != nil && checkpoint.State != nil {
			observation.resumeID = checkpoint.State.ResumeID
		}
	}
	for _, pid := range observation.spawnPIDs {
		w.allPIDs[pid] = struct{}{}
	}
	w.turns = append(w.turns, observation)
	if runErr != nil {
		if isEnvironmentFailure(runErr, result) {
			return environmentUnavailable(ctx, w.provider, classifyEnvironmentFailure(runErr, result))
		}
		return fmt.Errorf("%s turn failed: %w", w.provider, runErr)
	}
	if result == nil || result.RunID == "" || observation.resumeID == "" {
		return fmt.Errorf("%s turn returned incomplete Result/checkpoint", w.provider)
	}
	godog.Logf(ctx, "turn complete: driver=%s run_id=%s resume=%s pids=%v text=%q",
		w.provider, result.RunID, observation.resumeID, observation.spawnPIDs, truncate(result.Text, 180))
	return nil
}

func (w *scenarioWorld) closeAndVerify(ctx context.Context) error {
	if w.closed {
		return nil
	}
	w.closed = true
	var closeErr error
	if w.agent != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), e2eCloseTimeout())
		closeErr = w.agent.Close(closeCtx)
		cancel()
	}
	deadline := time.Now().Add(e2eCloseTimeout())
	for pid := range w.allPIDs {
		for processAlive(pid) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if processAlive(pid) {
			closeErr = errors.Join(closeErr, fmt.Errorf("real CLI pid %d remains alive after Agent.Close", pid))
		}
	}
	if err := os.RemoveAll(w.root); err != nil {
		closeErr = errors.Join(closeErr, err)
	}
	if closeErr == nil {
		godog.Logf(ctx, "scenario cleanup complete: driver=%s pids=%d", w.provider, len(w.allPIDs))
	}
	return closeErr
}

func valuePID(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

func isEnvironmentFailure(err error, result *adaptor.Result) bool {
	text := strings.ToLower(environmentFailureText(err, result))
	for _, marker := range []string{
		"401", "403", "unauthorized", "forbidden", "authentication", "not logged in",
		"credential", "api key", "model not found", "unknown model", "does not have access",
		"connection refused", "network is unreachable", "could not resolve host",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func classifyEnvironmentFailure(err error, result *adaptor.Result) string {
	text := strings.ToLower(environmentFailureText(err, result))
	switch {
	case strings.Contains(text, "401"), strings.Contains(text, "unauthorized"), strings.Contains(text, "not logged in"):
		return "provider authentication rejected the local credential"
	case strings.Contains(text, "403"), strings.Contains(text, "forbidden"), strings.Contains(text, "does not have access"):
		return "provider credential lacks access"
	case strings.Contains(text, "model"):
		return "configured smoke model is unavailable"
	default:
		return "provider endpoint is unavailable"
	}
}

func environmentFailureText(err error, result *adaptor.Result) string {
	parts := []string{}
	if err != nil {
		parts = append(parts, err.Error())
	}
	if result != nil {
		raw := result.Raw()
		parts = append(parts, raw.Stderr, raw.Stdout)
	}
	return strings.Join(parts, "\n")
}

func environmentUnavailable(ctx context.Context, provider providerName, reason string) error {
	godog.Logf(ctx, "environment unavailable: driver=%s reason=%s; fake CLI fallback is forbidden", provider, reason)
	return godog.ErrSkip
}

func e2eTurnTimeout() time.Duration {
	return durationEnv("AGENT_ADAPTOR_E2E_TURN_TIMEOUT", 4*time.Minute)
}

func e2eCloseTimeout() time.Duration {
	return durationEnv("AGENT_ADAPTOR_E2E_CLOSE_TIMEOUT", 20*time.Second)
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name)))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func randomHex(byteCount int) string {
	buffer := make([]byte, byteCount)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}

func firstNonEmptyLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
