package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agent-dance/agent-adaptor/v2/internal/processx"
)

type artifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

var projectFiles = []string{"acceptance_test.go", "go.mod", "router.go"}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Reuse the repository's process-tree shutdown helper. This is ordinary host
// subprocess management; every model execution still uses the public SDK.
func configureCommand(command *exec.Cmd) {
	processx.ConfigureCancellation(command)
	command.WaitDelay = 2 * time.Second
}

// Candidate identity covers every byte of every allowed file, including the
// host-owned acceptance suite. A manifest supplied by a model is only a claim.
func manifestHash(files []artifact) string { data, _ := json.Marshal(files); return digest(data) }

func projectManifest(root string) ([]artifact, error) {
	var files []artifact
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("project root is a symlink")
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() || !slices.Contains(projectFiles, rel) {
			return fmt.Errorf("unexpected project path %q", rel)
		}
		data, err := safeRead(root, rel)
		if err != nil {
			return err
		}
		files = append(files, artifact{rel, digest(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if len(files) != len(projectFiles) {
		return nil, errors.New("manifest does not cover every required project file")
	}
	return files, nil
}

func safeRead(root, name string) ([]byte, error) {
	if !utf8.ValidString(name) || name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\:") || !slices.Contains(projectFiles, name) {
		return nil, fmt.Errorf("path is outside the artifact allowlist: %q", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("artifact traversal")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid project root")
	}
	path := filepath.Join(root, clean)
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 256<<10 {
		return nil, fmt.Errorf("artifact is not a bounded regular file: %q", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > 256<<10 || !utf8.Valid(data) {
		return nil, fmt.Errorf("artifact is not bounded UTF-8: %q", name)
	}
	return data, nil
}

func verifyManifest(root string, expected []artifact) error {
	actual, err := projectManifest(root)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, expected) {
		return errors.New("candidate hash drift or incomplete/forged manifest")
	}
	return nil
}

func copyCandidate(src, dst string, expected []artifact) error {
	if err := verifyManifest(src, expected); err != nil {
		return err
	}
	if err := os.Mkdir(dst, 0700); err != nil {
		return err
	}
	for _, item := range expected {
		data, err := safeRead(src, item.Path)
		if err != nil {
			return err
		}
		if digest(data) != item.SHA256 {
			return errors.New("candidate changed during snapshot")
		}
		if err := os.WriteFile(filepath.Join(dst, item.Path), data, 0600); err != nil {
			return err
		}
	}
	return verifyManifest(dst, expected)
}

type checkReceipt struct {
	ID        string    `json:"id"`
	Task      string    `json:"task"`
	Candidate string    `json:"candidate_hash"`
	Check     string    `json:"check"`
	Argv      []string  `json:"argv"`
	ExitCode  int       `json:"exit_code"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
	Log       string    `json:"log"`
	LogSHA256 string    `json:"log_sha256"`
}

var checkCommands = map[string][]string{"test": {"test", "-json", "-timeout=60s", "-count=1", "./..."}, "vet": {"vet", "./..."}}

// These are deliberately narrow task constraints, not a general Go sandbox.
// They prevent package initialization from skipping or impersonating tests.
func validateRouter(root string) error {
	data, err := safeRead(root, "router.go")
	if err != nil {
		return err
	}
	file, err := parser.ParseFile(token.NewFileSet(), "router.go", data, parser.ParseComments)
	if err != nil {
		return err
	}
	if file.Name.Name != "mentionroute" {
		return errors.New("wrong implementation package")
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "go:") || strings.HasPrefix(comment.Text, "//line ") {
				return errors.New("compiler directives are outside this task")
			}
		}
	}
	imports := []string{"bytes", "errors", "fmt", "slices", "sort", "strconv", "strings", "unicode", "unicode/utf8"}
	for _, item := range file.Imports {
		path, err := strconv.Unquote(item.Path.Value)
		if err != nil || !slices.Contains(imports, path) || (item.Name != nil && (item.Name.Name == "." || item.Name.Name == "_")) {
			return errors.New("implementation import is outside the fixed task allowlist")
		}
	}
	for _, decl := range file.Decls {
		switch item := decl.(type) {
		case *ast.FuncDecl:
			if item.Name.Name == "init" || item.Name.Name == "TestMain" {
				return errors.New("package initialization and TestMain are forbidden")
			}
		case *ast.GenDecl:
			if item.Tok == token.VAR {
				for _, spec := range item.Specs {
					value := spec.(*ast.ValueSpec)
					for _, expr := range value.Values {
						called := false
						ast.Inspect(expr, func(n ast.Node) bool {
							if _, ok := n.(*ast.CallExpr); ok {
								called = true
							}
							return true
						})
						if called {
							return errors.New("package variable initialization cannot call functions")
						}
					}
				}
			}
		}
	}
	return nil
}

var requiredTests = []string{"TestAcceptanceR1LiteralUTF8", "TestAcceptanceR2BoundariesAndLongest", "TestAcceptanceR3DeduplicationOrder", "TestAcceptanceR4InlineCode", "TestAcceptanceR5FencedCode", "TestAcceptanceR6QuotesEmailEscapes", "TestAcceptanceR7HumanRouting", "TestAcceptanceR8AgentAuthority", "TestAcceptanceConcurrentIndependentCalls"}

func acceptanceCompleted(data []byte) error {
	run, passed := map[string]bool{}, map[string]bool{}
	packagePassed := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var event struct{ Action, Package, Test string }
		if json.Unmarshal(line, &event) != nil || event.Package != "example.test/mentionroute" {
			continue
		}
		if event.Action == "fail" {
			return errors.New("acceptance test emitted a failure")
		}
		if event.Test == "" && event.Action == "pass" {
			packagePassed = true
		}
		if event.Action == "run" {
			run[event.Test] = true
		}
		if event.Action == "pass" && run[event.Test] {
			passed[event.Test] = true
		}
	}
	for _, name := range requiredTests {
		if !run[name] || !passed[name] {
			return fmt.Errorf("host acceptance did not actually complete %s", name)
		}
	}
	if !packagePassed {
		return errors.New("missing final acceptance package pass")
	}
	return nil
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := (2 << 20) - b.Len()
	if len(p) > room {
		b.exceeded = true
		p = p[:max(0, room)]
	}
	_, err := b.Buffer.Write(p)
	return n, err
}

func runCheck(ctx context.Context, root, output, task, candidate, name string, ordinal int) (checkReceipt, error) {
	args, ok := checkCommands[name]
	if !ok {
		return checkReceipt{}, errors.New("check is not allowlisted")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		return checkReceipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, goPath, args...)
	command.Dir = root
	configureCommand(command)
	// Tests receive a small build environment, never the provider's credentials.
	for _, key := range []string{"PATH", "Path", "SystemRoot", "WINDIR", "TEMP", "TMP", "HOME", "USERPROFILE", "LOCALAPPDATA", "GOCACHE", "GOPATH", "GOROOT"} {
		if value, exists := os.LookupEnv(key); exists {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Env = append(command.Env, "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	var log boundedBuffer
	command.Stdout = &log
	command.Stderr = &log
	receipt := checkReceipt{ID: fmt.Sprintf("check-%03d", ordinal), Task: task, Candidate: candidate, Check: name, Argv: append([]string{goPath}, args...), Started: time.Now().UTC(), ExitCode: 0}
	runErr := command.Run()
	receipt.Finished = time.Now().UTC()
	if runErr != nil {
		receipt.ExitCode = -1
		var exit *exec.ExitError
		if errors.As(runErr, &exit) {
			receipt.ExitCode = exit.ExitCode()
		}
	}
	if ctx.Err() != nil {
		_, _ = fmt.Fprintln(&log, ctx.Err())
	}
	if log.exceeded {
		receipt.ExitCode = -1
		_, _ = fmt.Fprintln(&log, "host output limit exceeded")
	}
	if name == "test" && receipt.ExitCode == 0 {
		if err := acceptanceCompleted(log.Bytes()); err != nil {
			receipt.ExitCode = -1
			_, _ = fmt.Fprintln(&log, err)
		}
	}
	data := []byte(redactText(log.String()))
	receipt.Log = filepath.ToSlash(filepath.Join("checks", receipt.ID+".log"))
	receipt.LogSHA256 = digest(data)
	if err := os.WriteFile(filepath.Join(output, filepath.FromSlash(receipt.Log)), data, 0600); err != nil {
		return receipt, err
	}
	return receipt, nil
}

var secretField = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api_key|apikey|authorization|password|secret|auth)"\s*:\s*")[^"]*(")`)
var bearerToken = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`)
var environmentSecret = regexp.MustCompile(`(?i)(?:TOKEN|SECRET|PASSWORD|API_KEY|AUTH)`)
var secretKey = regexp.MustCompile(`(?i)^(?:access_token|refresh_token|id_token|token|api_key|apikey|authorization|password|secret|auth|credentials)$`)

func redactText(value string) string {
	for _, pair := range os.Environ() {
		key, secret, ok := strings.Cut(pair, "=")
		if ok && len(secret) >= 8 && environmentSecret.MatchString(key) {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	value = secretField.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	return bearerToken.ReplaceAllString(value, "Bearer [REDACTED]")
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// Walk strings instead of replacing serialized JSON: escaping must remain valid.
	var decoded any
	if err = json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var sanitize func(any) any
	sanitize = func(v any) any {
		switch x := v.(type) {
		case string:
			return redactText(x)
		case map[string]any:
			for key, item := range x {
				if secretKey.MatchString(key) {
					x[key] = "[REDACTED]"
				} else {
					x[key] = sanitize(item)
				}
			}
		case []any:
			for i := range x {
				x[i] = sanitize(x[i])
			}
		}
		return v
	}
	data, err = json.MarshalIndent(sanitize(decoded), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}
