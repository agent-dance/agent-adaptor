// Package livetest supplies explicit, isolated inputs to Codex tests only.
// Production packages must not import it. It never locates a native profile.
package livetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const RouteEnv = "AGENT_ADAPTOR_CODEX_LIVE_ROUTE_FILE"
const maxSeedBytes = 64 << 10

// Profile contains only the private test roots and the validated official argv.
type Profile struct {
	Home, Directory, Workspace string
	ExtraArgs                  []string
}

// Prepare validates routing before reading authentication or creating a profile.
// The seed must contain only the authorized API key; all other native state stays
// outside the fresh test profile. No caller-supplied argv is accepted.
func Prepare(seed, routeFile, home string) (Profile, error) {
	route, err := readSeed(routeFile)
	if err != nil {
		return Profile{}, errors.New("codex live route file unavailable")
	}
	args, err := RouteArgs(route)
	if err != nil {
		return Profile{}, err
	}
	if seed == "" {
		return Profile{}, errors.New("codex live auth seed unavailable")
	}
	auth, err := readSeed(filepath.Join(seed, "auth.json"))
	if err != nil {
		return Profile{}, errors.New("codex live auth seed unavailable")
	}
	fields, err := object(auth, "OPENAI_API_KEY")
	if err != nil {
		return Profile{}, errors.New("codex live auth seed must contain only OPENAI_API_KEY")
	}
	var key string
	if json.Unmarshal(fields["OPENAI_API_KEY"], &key) != nil || strings.TrimSpace(key) == "" {
		return Profile{}, errors.New("codex live auth seed requires an API key")
	}
	auth, _ = json.Marshal(map[string]string{"OPENAI_API_KEY": key})
	p := Profile{Home: home, Directory: filepath.Join(home, "profile"), Workspace: filepath.Join(home, "workspace"), ExtraArgs: args}
	for _, path := range []string{p.Directory, p.Workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			return Profile{}, errors.New("cannot create isolated live directory")
		}
	}
	if err := os.WriteFile(filepath.Join(p.Directory, "auth.json"), auth, 0600); err != nil {
		return Profile{}, errors.New("cannot create isolated live auth")
	}
	// Exec requires a trusted repository; initialize the same private workspace
	// used by both transports instead of passing an exec-only bypass flag.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "init", "--quiet", "--template=", p.Workspace)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USERPROFILE=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig")}
	if value := os.Getenv("SystemRoot"); value != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+value)
	}
	if cmd.Run() != nil {
		return Profile{}, errors.New("cannot initialize isolated live workspace")
	}
	return p, nil
}

func readSeed(path string) ([]byte, error) {
	f, err := os.Lstat(path)
	if err != nil || !f.Mode().IsRegular() || f.Size() > maxSeedBytes {
		return nil, errors.New("invalid seed file")
	}
	return os.ReadFile(path)
}

// RouteArgs accepts one versioned, closed set of routing fields. Empty/default
// routing is never inferred from auth, environment, or the operator's config.
func RouteArgs(data []byte) ([]string, error) {
	fail := func() ([]string, error) { return nil, errors.New("invalid or unsupported codex live route") }
	fields, err := object(data, "version", "model_provider", "name", "base_url", "wire_api", "requires_openai_auth")
	if err != nil {
		return fail()
	}
	var route struct {
		Version  int    `json:"version"`
		Provider string `json:"model_provider"`
		Name     string `json:"name"`
		BaseURL  string `json:"base_url"`
		WireAPI  string `json:"wire_api"`
		Auth     *bool  `json:"requires_openai_auth"`
	}
	if json.Unmarshal(data, &route) != nil || len(fields) != 6 || route.Version != 1 || route.Auth == nil || !*route.Auth || route.WireAPI != "responses" {
		return fail()
	}
	for _, value := range []string{route.Provider, route.Name, route.BaseURL} {
		if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fail()
		}
	}
	endpoint, err := url.Parse(route.BaseURL)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || endpoint.Opaque != "" {
		return fail()
	}
	if endpoint.Scheme != "https" {
		ip := net.ParseIP(endpoint.Hostname())
		if endpoint.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return fail()
		}
	}
	// JSON string syntax is also valid TOML basic-string syntax. Marshal escapes
	// control/quote/backslash/Unicode without permitting key or value injection.
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	args := []string{"-c", "model_provider=" + quote(route.Provider)}
	if route.Provider == "openai" {
		if route.Name != "OpenAI" {
			return fail()
		}
		return append(args, "-c", "openai_base_url="+quote(route.BaseURL)), nil
	}
	// Keep the provider key inside a TOML value: CLI override dotted-key
	// parsing must not split a provider identifier containing a literal dot.
	value := "model_providers={" + quote(route.Provider) + "={name=" + quote(route.Name) + ",base_url=" + quote(route.BaseURL) + ",wire_api=\"responses\",requires_openai_auth=true}}"
	return append(args, "-c", value), nil
}

func object(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	if len(data) > maxSeedBytes {
		return nil, errors.New("seed too large")
	}
	allowed := make(map[string]bool, len(keys))
	for _, key := range keys {
		allowed[key] = true
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("object required")
	}
	out := make(map[string]json.RawMessage)
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, errors.New("invalid object")
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || out[key] != nil {
			return nil, errors.New("unknown or duplicate field")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errors.New("invalid field")
		}
		out[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, errors.New("invalid object")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing input")
	}
	return out, nil
}
