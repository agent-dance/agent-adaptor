package cursor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/engine"
	"github.com/agent-dance/agent-adaptor/internal/skillruntime"
)

func cursorPathTestEnvironment(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"CURSOR_HOME", "CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "XDG_CONFIG_HOME"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func TestCursorOfficialRootPrecedenceAndSelectionAuthority(t *testing.T) {
	home := cursorPathTestEnvironment(t)
	processConfig, processData, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CURSOR_CONFIG_DIR", processConfig)
	t.Setenv("CURSOR_DATA_DIR", processData)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg := driver.CommonConfig{}
	b, err := effectiveCursorBindingsNoInitialize(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorHome(b) != filepath.Join(home, ".cursor") || resolveCursorConfigDir(b) != processConfig || resolveCursorDataDir(b) != processData {
		t.Fatal("native config/data/resource roots collapsed")
	}
	cfg.Env = []driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: ""}, {Name: "CURSOR_DATA_DIR", Value: ""}}
	b, err = effectiveCursorBindingsNoInitialize(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorConfigDir(b) != filepath.Join(xdg, "cursor") || resolveCursorDataDir(b) != filepath.Join(home, ".cursor") {
		t.Fatal("explicit empty official env must clear inherited override")
	}
	legacy := t.TempDir()
	cfg.Env = append(cfg.Env, driver.EnvBinding{Name: "CURSOR_HOME", Value: legacy})
	b, err = effectiveCursorBindingsNoInitialize(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorHome(b) != legacy || resolveCursorConfigDir(b) != legacy || resolveCursorDataDir(b) != legacy {
		t.Fatal("legacy SDK selector not mapped to official roots")
	}
	selected := t.TempDir()
	b, err = effectiveCursorBindingsNoInitialize(cfg, &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: selected})
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorHome(b) != selected || resolveCursorConfigDir(b) != selected || resolveCursorDataDir(b) != selected {
		t.Fatal("Dedicated did not override all inherited/configured roots")
	}
	if _, err := os.Stat(filepath.Join(selected, cursorPrivateHomeName)); !os.IsNotExist(err) {
		t.Fatal("read-only resolution created execution HOME")
	}
	native, err := effectiveCursorBindingsNoInitialize(cfg, &driver.ProfileSelection{Mode: driver.ProfileModeNative})
	if err != nil {
		t.Fatal(err)
	}
	if resolveCursorHome(native) != filepath.Join(home, ".cursor") {
		t.Fatal("Native selected construction CURSOR_HOME instead of native user resources")
	}
}

func TestCursorNativeSplitCloneUsesFormalConfigAuthAndNoSessionData(t *testing.T) {
	home := cursorPathTestEnvironment(t)
	resources := filepath.Join(home, ".cursor")
	config, data, target := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	cursorWrite(t, filepath.Join(resources, "mcp.json"), `{"mcpServers":{}}`)
	cursorWrite(t, filepath.Join(resources, "cli-config.json"), `{"wrong_source":true}`)
	cursorWrite(t, filepath.Join(config, "cli-config.json"), `{"approvalMode":"allowlist","authInfo":{"email":"test.invalid"}}`)
	cursorWrite(t, filepath.Join(config, "config.json"), `{"model":"config-source"}`)
	cursorWrite(t, filepath.Join(data, "chats", "private"), "not a seed")
	cfg := driver.CommonConfig{Env: []driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: config}, {Name: "CURSOR_DATA_DIR", Value: data}}}
	selected := &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: resources, Dir: target, Clone: &driver.CloneProfileOptions{IncludeSettings: true, IncludeMCP: true, AuthMode: driver.CloneProfileAuthLink}}
	p, err := resolveCursorProfile(cfg, selected)
	if err != nil {
		t.Fatal(err)
	}
	if p.Dir != target {
		t.Fatal(p)
	}
	sourceInfo, _ := os.Stat(filepath.Join(config, "cli-config.json"))
	cloneInfo, err := os.Stat(filepath.Join(target, "cli-config.json"))
	if err != nil || !os.SameFile(sourceInfo, cloneInfo) {
		t.Fatal("auth did not come from actual config root")
	}
	if b, _ := os.ReadFile(filepath.Join(target, "config.json")); !strings.Contains(string(b), "config-source") {
		t.Fatal("settings did not come from actual config root")
	}
	if _, err := os.Stat(filepath.Join(target, "chats")); !os.IsNotExist(err) {
		t.Fatal("session data entered clone")
	}
}

func TestCursorProjectionIsPerRunBoundedAndPreservesSources(t *testing.T) {
	cursorPathTestEnvironment(t)
	selected := t.TempDir()
	cursorWrite(t, filepath.Join(selected, "mcp.json"), `{"mcpServers":{"test":{"url":"http://127.0.0.1/mcp","headers":{"Authorization":"Bearer ${env:TEST_TOKEN}"}}}}`)
	cursorWrite(t, filepath.Join(selected, "agents", "planner.md"), "---\nname: planner\n---\nPlan.")
	cursorWrite(t, filepath.Join(selected, "hooks.json"), `{"version":1,"hooks":{}}`)
	cursorWrite(t, filepath.Join(selected, "skills", "plain", "SKILL.md"), "# Test")
	cursorWrite(t, filepath.Join(selected, "chats", "healthy"), "keep")
	cfg := driver.CommonConfig{}
	selection := &driver.ProfileSelection{Mode: driver.ProfileModeDedicated, Dir: selected}
	b, err := effectiveCursorBindings(cfg, selection)
	if err != nil {
		t.Fatal(err)
	}
	first, plugin1, cleanup1, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, b)
	if err != nil {
		t.Fatal(err)
	}
	second, plugin2, cleanup2, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, b)
	if err != nil {
		t.Fatal(err)
	}
	home1, home2 := skillruntime.ResolveHome(first), skillruntime.ResolveHome(second)
	if home1 == home2 || filepath.Base(plugin1) != "agents-plugin" || filepath.Base(plugin2) != "agents-plugin" {
		t.Fatal("runs share HOME or plugin identity depends on random run ID")
	}
	want, _ := os.ReadFile(filepath.Join(selected, "mcp.json"))
	got, err := os.ReadFile(filepath.Join(home1, ".cursor", "mcp.json"))
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("native MCP projection changed bytes")
	}
	if _, err := os.Stat(filepath.Join(plugin1, "mcp.json")); !os.IsNotExist(err) {
		t.Fatal("agents plugin duplicated MCP")
	}
	if _, err := os.Stat(filepath.Join(home1, ".cursor", "chats")); !os.IsNotExist(err) {
		t.Fatal("projection imported history")
	}
	if resolveCursorDataDir(first) != selected || resolveCursorConfigDir(first) != selected || resolveCursorHome(first) != selected {
		t.Fatal("temporary HOME changed stable roots")
	}
	if err := cleanup1(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home2); err != nil {
		t.Fatal("cleanup removed another run")
	}
	if err := cleanup2(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(selected, "mcp.json")); !reflect.DeepEqual(want, got) {
		t.Fatal("projection mutated source")
	}
	if _, err := os.Stat(filepath.Join(selected, "chats", "healthy")); err != nil {
		t.Fatal("cleanup removed healthy session")
	}
	native, plugin, cleanup, err := prepareCursorProjection(context.Background(), selected, false, driver.ResolvedSkills{}, []driver.EnvBinding{{Name: "HOME", Value: "native-home"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if skillruntime.ResolveHome(native) != "native-home" || plugin == "" {
		t.Fatal("Native agent projection replaced native HOME")
	}
}

func TestCursorProjectionRejectsForeignOwnershipLinksAndCancellation(t *testing.T) {
	cursorPathTestEnvironment(t)
	t.Run("foreign home", func(t *testing.T) {
		p := t.TempDir()
		cursorWrite(t, filepath.Join(p, cursorPrivateHomeName, "foreign"), "keep")
		if err := prepareCursorPrivateHome(p); err == nil {
			t.Fatal("adopted foreign HOME")
		}
	})
	t.Run("wrong marker", func(t *testing.T) {
		p := t.TempDir()
		if err := prepareCursorPrivateHome(p); err != nil {
			t.Fatal(err)
		}
		cursorWrite(t, filepath.Join(p, cursorPrivateHomeName, cursorPrivateHomeMarker), "wrong")
		if err := prepareCursorPrivateHome(p); err == nil {
			t.Fatal("adopted wrong owner")
		}
	})
	t.Run("unknown link", func(t *testing.T) {
		p := t.TempDir()
		target := t.TempDir()
		if err := os.Symlink(target, filepath.Join(p, "skills")); err != nil {
			t.Skip("symlink unavailable")
		}
		_, _, _, err := prepareCursorProjection(context.Background(), p, true, driver.ResolvedSkills{}, nil)
		if err == nil {
			t.Fatal("followed unknown link")
		}
	})
	t.Run("managed skill link", func(t *testing.T) {
		p := t.TempDir()
		target := t.TempDir()
		cursorWrite(t, filepath.Join(target, "SKILL.md"), "# Approved")
		os.MkdirAll(filepath.Join(p, "skills"), 0700)
		if err := os.Symlink(target, filepath.Join(p, "skills", "approved")); err != nil {
			t.Skip("symlink unavailable")
		}
		skills := driver.ResolvedSkills{Entries: []driver.ResolvedSkill{{RuntimeName: "approved", SourcePath: target}}}
		b, _, cleanup, err := prepareCursorProjection(context.Background(), p, true, skills, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if _, err := os.Stat(filepath.Join(skillruntime.ResolveHome(b), ".cursor", "skills", "approved", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		p := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, _, err := prepareCursorProjection(ctx, p, true, driver.ResolvedSkills{}, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(filepath.Join(p, cursorPrivateHomeName))
		if len(entries) != 1 {
			t.Fatal("cancel leaked run projection")
		}
	})
}

func TestCursorNativeConfigGuardSeparatesStaticSettingsFromAuthentication(t *testing.T) {
	cursorPathTestEnvironment(t)
	p := t.TempDir()
	b := []driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: p}}
	path := filepath.Join(p, "cli-config.json")
	cursorWrite(t, path, `{"sandbox":{"mode":"disabled"},"authInfo":{"email":"first"}}`)
	first, err := cursorConfigState(b)
	if err != nil {
		t.Fatal(err)
	}
	cursorWrite(t, path, `{"authInfo":{"email":"second"},"sandbox":{"mode":"disabled"}}`)
	same, err := cursorConfigState(b)
	if err != nil || same != first {
		t.Fatal("auth rotated compatibility")
	}
	cursorWrite(t, path, `{"sandbox":{"mode":"enabled"}}`)
	req := driver.Request{Session: &driver.SessionContext{State: &driver.SessionState{ResumeID: "healthy", Data: map[string]string{cursorSessionConfigState: first}}}}
	if err := validateCursorSessionGuard(context.Background(), req, "", "", b); !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatal("native static change resumed", err)
	}
	for _, key := range []string{cursorSessionConfigDir, cursorSessionDataDir, cursorSessionResourceDir} {
		req.Session.State.Data = map[string]string{key: "other"}
		if err := validateCursorSessionGuard(context.Background(), req, "", "", b); !errors.Is(err, engine.ErrResumeRejected) {
			t.Fatal("root drift resumed", key, err)
		}
	}
}

func cursorWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCursorEmptyLegacyBindingKeepsSelectedProcessProfile(t *testing.T) {
	cursorPathTestEnvironment(t)
	legacy := t.TempDir()
	t.Setenv("CURSOR_HOME", legacy)
	cfg := driver.CommonConfig{Env: []driver.EnvBinding{{Name: "CURSOR_HOME", Value: ""}}}
	bindings, err := effectiveCursorBindingsNoInitialize(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cursorIsolatedProfile(cfg, nil) || resolveCursorHome(bindings) != legacy || resolveCursorConfigDir(bindings) != legacy || resolveCursorDataDir(bindings) != legacy {
		t.Fatal("resolved legacy selection and CLI roots diverged")
	}
}

func TestCursorStaticGuardsRejectMissingChangedAndUnknownState(t *testing.T) {
	cursorPathTestEnvironment(t)
	selected := t.TempDir()
	cursorWrite(t, filepath.Join(selected, "agents", "planner.md"), "Plan safely.")
	cursorWrite(t, filepath.Join(selected, "hooks.json"), `{"version":1,"hooks":{}}`)
	cfg := driver.CommonConfig{Env: []driver.EnvBinding{{Name: "CURSOR_HOME", Value: selected}}}
	bindings, err := effectiveCursorBindingsNoInitialize(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	configState, err := cursorConfigState(bindings)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := cursorResourceState(context.Background(), selected)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]string{cursorSessionConfigDir: selected, cursorSessionDataDir: selected, cursorSessionResourceDir: selected, cursorSessionConfigState: configState, cursorSessionResourceState: resources}
	req := driver.Request{Session: &driver.SessionContext{State: &driver.SessionState{ResumeID: "healthy", Data: state}}}
	if err := validateCursorSessionGuard(context.Background(), req, "", "", bindings); err != nil {
		t.Fatal("same construction rejected", err)
	}
	for key, value := range state {
		delete(state, key)
		if err := validateCursorSessionGuard(context.Background(), req, "", "", bindings); !errors.Is(err, engine.ErrResumeRejected) {
			t.Fatal("incomplete old guard accepted", key, err)
		}
		state[key] = value
	}
	cursorWrite(t, filepath.Join(selected, "cli-config.json"), `{"newExecutionSetting":9007199254740993}`)
	if err := validateCursorSessionGuard(context.Background(), req, "", "", bindings); !errors.Is(err, engine.ErrResumeRejected) {
		t.Fatal("unknown configuration was ignored", err)
	}
	next, err := cursorConfigState(bindings)
	if err != nil {
		t.Fatal(err)
	}
	cursorWrite(t, filepath.Join(selected, "cli-config.json"), `{"newExecutionSetting":9007199254740992}`)
	other, err := cursorConfigState(bindings)
	if err != nil || next == other {
		t.Fatal("large numeric configuration collided", err)
	}
	os.Remove(filepath.Join(selected, "cli-config.json"))
	for _, path := range []string{"agents/planner.md", "hooks.json"} {
		full := filepath.Join(selected, filepath.FromSlash(path))
		original, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		cursorWrite(t, full, string(original)+"\n")
		if err := validateCursorSessionGuard(context.Background(), req, "", "", bindings); !errors.Is(err, engine.ErrResumeRejected) {
			t.Fatal("actual static resource change resumed", path, err)
		}
		cursorWrite(t, full, string(original))
	}
	for _, key := range []string{cursorSessionConfigDir, cursorSessionDataDir, cursorSessionResourceDir} {
		state[key] = t.TempDir()
		if err := validateCursorSessionGuard(context.Background(), req, "", "", bindings); !errors.Is(err, engine.ErrResumeRejected) {
			t.Fatal("actual root change resumed", key, err)
		}
		state[key] = selected
	}
	// Random private projection paths never enter the stable source proof.
	b, _, cleanup, err := prepareCursorProjection(context.Background(), selected, true, driver.ResolvedSkills{}, bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := validateCursorSessionGuard(context.Background(), req, "", "", b); err != nil {
		t.Fatal("random per-run HOME changed guard", err)
	}
}

func TestCursorAuthNoneNativeCloneCopiesOnlyFormalStaticSettings(t *testing.T) {
	home := cursorPathTestEnvironment(t)
	resources := filepath.Join(home, ".cursor")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	config, target := t.TempDir(), t.TempDir()
	cursorWrite(t, filepath.Join(config, "cli-config.json"), `{"permissions":{"allow":[],"deny":[]},"sandbox":{"mode":"enabled"},"authInfo":{"email":"private"},"unknownFutureCredential":"private","privacyCache":{"updatedAt":1}}`)
	cfg := driver.CommonConfig{Env: []driver.EnvBinding{{Name: "CURSOR_CONFIG_DIR", Value: config}}}
	_, err := resolveCursorProfile(cfg, &driver.ProfileSelection{Mode: driver.ProfileModeClone, From: resources, Dir: target, Clone: &driver.CloneProfileOptions{IncludeSettings: true, AuthMode: driver.CloneProfileAuthNone}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(target, "cli-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private") || strings.Contains(string(data), "privacyCache") || !strings.Contains(string(data), `"mode":"enabled"`) {
		t.Fatal("AuthNone clone did not use static config projection")
	}
}
