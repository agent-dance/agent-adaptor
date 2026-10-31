package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// OpenCodeServerProcess is a test-only implementation of the official HTTP/SSE
// envelopes, shared by Driver and public Runner contract tests. Fixtures are
// supplied by opencode/testdata; this is never a runtime protocol helper.
func OpenCodeServerProcess(dir string) {
	pid := os.Getpid()
	if os.Getenv("AGENT_ADAPTOR_OPENCODE_EXCLUSIVE_WRITER") == "1" {
		raw, _ := os.ReadFile(filepath.Join(dir, "spawns.log"))
		for _, line := range strings.Fields(string(raw)) {
			old, _ := strconv.Atoi(line)
			if old != pid && ProcessAlive(old) {
				_ = os.WriteFile(filepath.Join(dir, "overlap"), []byte("old writer still running"), 0600)
			}
		}
	}
	if f, err := os.OpenFile(filepath.Join(dir, "spawns.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		fmt.Fprintln(f, pid)
		f.Close()
	}
	if os.Getenv("AGENT_ADAPTOR_OPENCODE_FAIL_START_ONCE") == "1" {
		if f, err := os.OpenFile(filepath.Join(dir, "failed-start"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
			f.Close()
			fmt.Fprintln(os.Stderr, "injected pre-delivery failure")
			os.Exit(7)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(93)
	}
	defer listener.Close()
	var mu sync.Mutex
	var terminal = map[string]any{}
	var eventWriter http.ResponseWriter
	mcpInstalled := map[string]any{}
	replies := make(chan json.RawMessage, 4)
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "opencode" || password == "" || password != os.Getenv("OPENCODE_SERVER_PASSWORD") {
			http.Error(w, "auth", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/mcp":
			mu.Lock()
			defer mu.Unlock()
			var config struct {
				MCP map[string]map[string]any `json:"mcp"`
			}
			_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &config)
			status := map[string]any{}
			for key, entry := range config.MCP {
				state := "connected"
				if entry["enabled"] == false {
					state = "disabled"
				}
				status[key] = map[string]string{"status": state}
			}
			for key, value := range mcpInstalled {
				status[key] = value
			}
			if r.Method == "POST" {
				if raw, err := os.ReadFile(filepath.Join(dir, "mcp-install-response.json")); err == nil {
					_, _ = w.Write(raw)
					return
				}
				raw, _ := io.ReadAll(r.Body)
				var input struct {
					Name   string         `json:"name"`
					Config map[string]any `json:"config"`
				}
				if json.Unmarshal(raw, &input) != nil || input.Name == "" || input.Config["enabled"] != true {
					http.Error(w, "invalid MCP installation", 400)
					return
				}
				if f, err := os.OpenFile(filepath.Join(dir, "mcp-install.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
					_, _ = f.Write(append(raw, '\n'))
					_ = f.Close()
				}
				mcpInstalled[input.Name] = map[string]string{"status": "connected"}
				status[input.Name] = mcpInstalled[input.Name]
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "mcp-status.json")); err == nil {
				_ = json.Unmarshal(raw, &status)
			}
			_ = json.NewEncoder(w).Encode(status)
		case r.URL.Path == "/config":
			config := map[string]any{}
			if raw, err := os.ReadFile(filepath.Join(dir, "native-config.json")); err == nil {
				_ = json.Unmarshal(raw, &config)
			}
			var overlay map[string]any
			_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &overlay)
			mcp, _ := config["mcp"].(map[string]any)
			if mcp == nil {
				mcp = map[string]any{}
			}
			overlayMCP, _ := overlay["mcp"].(map[string]any)
			for key, value := range overlayMCP {
				mcp[key] = value
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "managed-mcp.json")); err == nil {
				var managed map[string]any
				_ = json.Unmarshal(raw, &managed)
				for key, value := range managed {
					mcp[key] = value
				}
			}
			config["mcp"] = mcp
			// ConfigPermission uses property insertion order. Keep native and
			// overlay agent bytes intact instead of sorting nested maps in Go.
			agents := map[string]json.RawMessage{}
			var nativeAgents struct {
				Agent map[string]json.RawMessage `json:"agent"`
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "native-config.json")); err == nil {
				_ = json.Unmarshal(raw, &nativeAgents)
				for key, value := range nativeAgents.Agent {
					agents[key] = value
				}
			}
			var overlayAgents struct {
				Agent map[string]json.RawMessage `json:"agent"`
			}
			_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &overlayAgents)
			for key, value := range overlayAgents.Agent {
				agents[key] = value
			}
			if raw, err := os.ReadFile(filepath.Join(dir, "managed-agents.json")); err == nil {
				var managed map[string]json.RawMessage
				_ = json.Unmarshal(raw, &managed)
				for key, value := range managed {
					agents[key] = value
				}
			}
			config["agent"] = agents
			_ = json.NewEncoder(w).Encode(config)
		case r.URL.Path == "/agent":
			agentsRaw := []byte(`[{"name":"build","mode":"primary","permission":[{"permission":"*","pattern":"*","action":"allow"},{"permission":"read","pattern":"*.env","action":"deny"}]}]`)
			if raw, err := os.ReadFile(filepath.Join(dir, "native-agents.json")); err == nil {
				agentsRaw = raw
			}
			var agents []map[string]any
			_ = json.Unmarshal(agentsRaw, &agents)
			var config struct {
				Agent map[string]json.RawMessage `json:"agent"`
			}
			_ = json.Unmarshal([]byte(os.Getenv("OPENCODE_CONFIG_CONTENT")), &config)
			byName := map[string]map[string]any{}
			for _, agent := range agents {
				byName[agent["name"].(string)] = agent
			}
			for name, raw := range config.Agent {
				var patch map[string]json.RawMessage
				_ = json.Unmarshal(raw, &patch)
				if string(patch["disable"]) == "true" {
					delete(byName, name)
					continue
				}
				agent := byName[name]
				if agent == nil {
					agent = map[string]any{"name": name, "mode": "all"}
					byName[name] = agent
				}
				for key, raw := range patch {
					if key == "disable" {
						continue
					}
					if key == "prompt" && os.Getenv("AGENT_ADAPTOR_OPENCODE_IGNORE_AGENT_PROMPTS") == "1" {
						continue
					}
					if key == "permission" {
						if os.Getenv("AGENT_ADAPTOR_OPENCODE_IGNORE_AGENT_PERMISSIONS") == "1" {
							continue
						}
						prior, _ := agent["permission"].([]any)
						agent["permission"] = append(prior, fixturePermissionRules(raw)...)
						continue
					}
					var value any
					_ = json.Unmarshal(raw, &value)
					if key == "model" {
						if model, ok := value.(string); ok {
							provider, id, _ := strings.Cut(model, "/")
							value = map[string]any{"providerID": provider, "modelID": id}
						}
					}
					agent[key] = value
				}
			}
			agents = nil
			for _, agent := range byName {
				agents = append(agents, agent)
			}
			_ = json.NewEncoder(w).Encode(agents)
		case r.URL.Path == "/global/health":
			io.WriteString(w, `{"healthy":true}`)
		case r.URL.Path == "/project/current":
			io.WriteString(w, `{"id":"global"}`)
		case r.URL.Path == "/permission":
			_ = os.WriteFile(filepath.Join(dir, "permission-cache-loaded"), []byte("loaded"), 0600)
			io.WriteString(w, `[]`)
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			mu.Lock()
			eventWriter = w
			fmt.Fprint(w, "data: {\"type\":\"server.connected\",\"properties\":{}}\n\n")
			w.(http.Flusher).Flush()
			mu.Unlock()
			<-r.Context().Done()
			mu.Lock()
			if eventWriter == w {
				eventWriter = nil
			}
			mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/prompt_async"):
			var input map[string]any
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				http.Error(w, "bad input", 400)
				return
			}
			cwd, _ := os.Getwd()
			capture, _ := json.Marshal(map[string]any{"Args": os.Args, "Input": input, "CWD": cwd, "Config": os.Getenv("OPENCODE_CONFIG_CONTENT")})
			_ = os.WriteFile(filepath.Join(dir, "capture.json"), capture, 0600)
			if f, err := os.OpenFile(filepath.Join(dir, "prompts.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
				fmt.Fprintln(f, pid)
				f.Close()
			}
			if os.Getenv("AGENT_ADAPTOR_OPENCODE_FAIL_AFTER_PROMPT") == "1" {
				os.Exit(7)
			}
			w.WriteHeader(http.StatusNoContent)
			sessionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/prompt_async")
			go func() {
				raw, _ := os.ReadFile(filepath.Join(dir, "stdout"))
				raw = []byte(strings.ReplaceAll(string(raw), "ses_child", sessionID))
				parts := []any{}
				partIndex := map[string]int{}
				scanner := bufio.NewScanner(strings.NewReader(string(raw)))
				for scanner.Scan() {
					line := scanner.Text()
					var event struct {
						Type       string         `json:"type"`
						Properties map[string]any `json:"properties"`
					}
					_ = json.Unmarshal([]byte(line), &event)
					mu.Lock()
					if event.Type == "message.updated" && event.Properties["sessionID"] == sessionID {
						if info, ok := event.Properties["info"].(map[string]any); ok && info["role"] == "assistant" {
							terminal["info"] = info
						}
					}
					if event.Type == "message.part.updated" && event.Properties["sessionID"] == sessionID {
						if p, ok := event.Properties["part"].(map[string]any); ok {
							id, _ := p["id"].(string)
							if i, ok := partIndex[id]; ok {
								parts[i] = p
							} else {
								partIndex[id] = len(parts)
								parts = append(parts, p)
							}
							terminal["parts"] = parts
						}
					}
					if eventWriter != nil {
						fmt.Fprintf(eventWriter, "data: %s\n\n", line)
						eventWriter.(http.Flusher).Flush()
					}
					mu.Unlock()
					if event.Type == "permission.asked" || event.Type == "question.asked" {
						reply := <-replies
						_ = os.WriteFile(filepath.Join(dir, "reply.json"), reply, 0600)
						if strings.Contains(string(reply), `"once"`) {
							_ = os.WriteFile(filepath.Join(cwd, "executed"), []byte("approved"), 0600)
						}
					}
					if event.Type == "test.exit" {
						os.Exit(7)
					}
				}
			}()
		case strings.Contains(r.URL.Path, "/message/"):
			mu.Lock()
			defer mu.Unlock()
			if raw, err := os.ReadFile(filepath.Join(dir, "terminal.json")); err == nil {
				w.Write(raw)
			} else {
				_ = json.NewEncoder(w).Encode(terminal)
			}
		case strings.HasPrefix(r.URL.Path, "/permission/") || strings.HasPrefix(r.URL.Path, "/question/"):
			raw, _ := io.ReadAll(r.Body)
			if len(raw) == 0 {
				raw = []byte(`{"reject":true}`)
			}
			replies <- raw
			io.WriteString(w, "true")
		case strings.HasPrefix(r.URL.Path, "/session"):
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(r.URL.Path, "missing") {
				http.Error(w, "missing", 404)
				return
			}
			id := "ses_child"
			if os.Getenv("AGENT_ADAPTOR_OPENCODE_DYNAMIC_SESSIONS") == "1" {
				id = "ses_" + strconv.Itoa(pid)
			}
			if r.Method == "GET" || r.Method == "PATCH" {
				id = strings.TrimPrefix(r.URL.Path, "/session/")
			}
			permissionsFile := filepath.Join(dir, "session-permissions.json")
			permissions := map[string][]json.RawMessage{}
			if raw, err := os.ReadFile(permissionsFile); err == nil {
				_ = json.Unmarshal(raw, &permissions)
			}
			if strings.HasSuffix(r.URL.Path, "/fork") {
				id = "ses_fork_" + strconv.Itoa(pid)
				parent := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/session/"), "/fork")
				permissions[id] = append([]json.RawMessage{}, permissions[parent]...)
				stored, _ := json.Marshal(permissions)
				_ = os.WriteFile(permissionsFile, stored, 0600)
			}
			info := map[string]any{"id": id}
			if raw, err := os.ReadFile(filepath.Join(dir, "session-info.json")); err == nil {
				var sessions map[string]map[string]any
				_ = json.Unmarshal(raw, &sessions)
				if entry := sessions[id]; entry != nil {
					info = entry
				}
			}
			if _, exists := permissions[id]; !exists {
				raw, _ := json.Marshal(info["permission"])
				var initial []json.RawMessage
				_ = json.Unmarshal(raw, &initial)
				permissions[id] = initial
			}
			if r.Method == "PATCH" {
				raw, _ := io.ReadAll(r.Body)
				_ = os.WriteFile(filepath.Join(dir, "session-permission.json"), raw, 0600)
				var patch struct {
					Permission []json.RawMessage `json:"permission"`
				}
				if err := json.Unmarshal(raw, &patch); err != nil {
					http.Error(w, "bad permission patch", 400)
					return
				}
				if os.Getenv("AGENT_ADAPTOR_OPENCODE_IGNORE_SESSION_PERMISSIONS") != "1" {
					permissions[id] = append(permissions[id], patch.Permission...)
					stored, _ := json.Marshal(permissions)
					_ = os.WriteFile(permissionsFile, stored, 0600)
				}
			}
			info["permission"] = permissions[id]
			_ = json.NewEncoder(w).Encode(info)
		default:
			http.NotFound(w, r)
		}
	})
	fmt.Printf("opencode server listening on http://%s\n", listener.Addr())
	stderr, _ := os.ReadFile(filepath.Join(dir, "stderr"))
	os.Stderr.Write(stderr)
	_ = http.Serve(listener, mux)
	os.Exit(0)
}

func fixturePermissionRules(raw []byte) []any {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	_, _ = d.Token()
	var out []any
	for d.More() {
		key, err := d.Token()
		if err != nil {
			break
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			break
		}
		var action string
		if json.Unmarshal(value, &action) == nil {
			out = append(out, map[string]any{"permission": key, "pattern": "*", "action": action})
			continue
		}
		patterns := json.NewDecoder(strings.NewReader(string(value)))
		_, _ = patterns.Token()
		for patterns.More() {
			pattern, err := patterns.Token()
			if err != nil {
				break
			}
			if patterns.Decode(&action) != nil {
				break
			}
			out = append(out, map[string]any{"permission": key, "pattern": pattern, "action": action})
		}
	}
	return out
}
