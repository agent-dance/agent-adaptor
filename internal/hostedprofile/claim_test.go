package hostedprofile

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

func specFor(t *testing.T) Spec {
	t.Helper()
	src := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	src, _, err := CanonicalSource(src)
	if err != nil {
		t.Fatal(err)
	}
	return Spec{DriverType: "claude", SourceDir: src}
}
func acquireTest(t *testing.T, s Spec) *Claim {
	t.Helper()
	c, err := Acquire(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.ReleaseClean(context.Background()) })
	return c
}
func seedTest(ctx context.Context, path string) error {
	return os.WriteFile(filepath.Join(path, "settings.json"), []byte(`{"test":1}`), 0600)
}
func readyTest(t *testing.T, c *Claim) {
	t.Helper()
	if err := c.Initialize(context.Background(), seedTest); err != nil {
		t.Fatal(err)
	}
}
func TestKeyEncoding(t *testing.T) {
	if got := hashFields("adaptor/hosted-profile-key/v1", "claude", "/profiles/source", "id\x00中文", "tenant", "profile", "Name"); got != "2c62769dc1be9502c6c7cdc6f908ec92a70763dc93416dd3be9a0fe8d65c4671" {
		t.Fatalf("framed key golden changed: %s", got)
	}
	if got := hashFields("ab", "c"); got == hashFields("a", "bc") {
		t.Fatal("ambiguous framing")
	}
	if got := hashFields("", ""); got == hashFields("") {
		t.Fatal("missing empty field")
	}
	seen := map[string]bool{}
	for _, s := range []string{"", "a", " a", "a\x00b", "a/b", "中文", "é", "e\u0301"} {
		h := hashFields(s, "tail")
		if seen[h] {
			t.Fatal("key collision")
		}
		seen[h] = true
	}
	s := specFor(t)
	c := acquireTest(t, s)
	base := c.KeyHash()
	if len(base) != 64 || !validHex(base, 64) {
		t.Fatal(base)
	}
	for _, id := range []driver.AgentIdentity{{ID: "x"}, {TenantID: "x"}, {ProfileID: "x"}, {Name: "x"}} {
		s.Identity = id
		other := acquireTest(t, s)
		if other.KeyHash() == base {
			t.Fatal("identity field omitted")
		}
	}
	alias := filepath.Join(filepath.Dir(s.SourceDir), "alias")
	if err := os.Symlink(s.SourceDir, alias); err != nil {
		t.Fatal(err)
	}
	s.Identity = driver.AgentIdentity{}
	s.SourceDir = alias
	if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrInUse) {
		t.Fatalf("alias did not conflict: %v", err)
	}
}
func TestSeedOnlyOnceAndSourceReadOnly(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	calls := 0
	seed := func(ctx context.Context, p string) error { calls++; return seedTest(ctx, p) }
	if err := c.Initialize(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(c.Dir(), "projects", "session")
	if err := os.Mkdir(filepath.Dir(session), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("unguessable provider state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.BeginUse(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseUnused(context.Background()); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal(err)
	}
	if err := c.ReleaseClean(context.Background()); err != nil {
		t.Fatal(err)
	}
	c2 := acquireTest(t, s)
	if err := c2.Initialize(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("reseeded")
	}
	if raw, err := os.ReadFile(session); err != nil || string(raw) != "unguessable provider state" {
		t.Fatal("session changed")
	}
	if _, err := os.Stat(filepath.Join(s.SourceDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("source mutated")
	}
}
func TestUnsafeOwnership(t *testing.T) {
	for _, name := range []string{"missing-owner", "unknown-version", "duplicate-key", "unknown-field", "missing-field", "identity", "hardlink", "lock-symlink", "profile-symlink", "world-directory", "world-marker", "source-replaced", "unknown-stage"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && (name == "world-directory" || name == "world-marker") {
				t.Skip("Unix modes; Windows DACL fixture runs separately")
			}
			s := specFor(t)
			c := acquireTest(t, s)
			if name != "unknown-stage" {
				readyTest(t, c)
			}
			keyDir := filepath.Dir(c.Dir())
			if err := c.ReleaseUnused(context.Background()); err != nil {
				t.Fatal(err)
			}
			owner := filepath.Join(keyDir, "owner.json")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("do not alter"), 0600); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing-owner":
				os.Remove(owner)
			case "unknown-version", "duplicate-key", "unknown-field", "missing-field", "identity":
				raw, _ := os.ReadFile(owner)
				text := string(raw)
				switch name {
				case "unknown-version":
					text = strings.Replace(text, `"version":1`, `"version":9`, 1)
				case "duplicate-key":
					text = strings.Replace(text, `"version":1`, `"version":1,"version":1`, 1)
				case "unknown-field":
					text = strings.Replace(text, `"version":1`, `"version":1,"unknown":0`, 1)
				case "missing-field":
					text = strings.Replace(text, `"version":1,`, "", 1)
				case "identity":
					text = strings.Replace(text, `"identity_hash":"`, `"identity_hash":"f`, 1)
				}
				os.WriteFile(owner, []byte(text), 0600)
			case "hardlink":
				os.Remove(owner)
				os.Link(outside, owner)
			case "lock-symlink":
				os.Remove(filepath.Join(keyDir, "owner.lock"))
				os.Symlink(outside, filepath.Join(keyDir, "owner.lock"))
			case "profile-symlink":
				os.Rename(c.Dir(), c.Dir()+"-saved")
				os.Symlink(filepath.Dir(outside), c.Dir())
			case "world-directory":
				os.Chmod(keyDir, 0755)
			case "world-marker":
				os.Chmod(owner, 0644)
			case "source-replaced":
				os.Rename(s.SourceDir, s.SourceDir+"-saved")
				os.Mkdir(s.SourceDir, 0700)
			case "unknown-stage":
				os.Mkdir(filepath.Join(keyDir, ".seed-"+strings.Repeat("a", 32)), 0700)
			}
			next, err := Acquire(context.Background(), s)
			if err == nil && name == "unknown-stage" {
				err = next.Initialize(context.Background(), seedTest)
				_ = next.ReleaseUnused(context.Background())
			}
			if !errors.Is(err, profile.ErrUnsafe) && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsafe profile accepted: %v", err)
			}
			raw, _ := os.ReadFile(outside)
			if string(raw) != "do not alter" {
				t.Fatal("external target changed")
			}
		})
	}
}
func TestReleaseRetries(t *testing.T) {
	for _, stage := range []string{"state", "unlock"} {
		t.Run(stage, func(t *testing.T) {
			s := specFor(t)
			c := acquireTest(t, s)
			readyTest(t, c)
			if err := c.BeginUse(context.Background()); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected release failure")
			if stage == "state" {
				c.beforeStateWrite = func() error { return failure }
			} else {
				c.beforeUnlock = func() error { return failure }
			}
			if err := c.ReleaseClean(context.Background()); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrInUse) {
				t.Fatalf("failed release lost claim: %v", err)
			}
			c.beforeStateWrite = nil
			c.beforeUnlock = nil
			if err := c.ReleaseClean(context.Background()); err != nil {
				t.Fatal(err)
			}
			acquireTest(t, s)
		})
	}
}
func TestInitializeFailureAndCanceled(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	failure := errors.New("seed failed")
	if err := c.Initialize(context.Background(), func(context.Context, string) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.Dir()); !os.IsNotExist(err) {
		t.Fatal("published partial profile")
	}
	if err := c.Initialize(context.Background(), seedTest); !errors.Is(err, failure) {
		t.Fatal("failed claim seeded twice")
	}
	if err := c.ReleaseUnused(context.Background()); err != nil {
		t.Fatal(err)
	}
	c = acquireTest(t, s)
	readyTest(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.BeginUse(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, s); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestBeginUseFailureRetainsClaim(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	readyTest(t, c)
	failure := errors.New("state sync failed")
	c.beforeStateWrite = func() error { return failure }
	if err := c.BeginUse(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := c.ReleaseUnused(context.Background()); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrInUse) {
		t.Fatal(err)
	}
	c.beforeStateWrite = nil
	if err := c.ReleaseClean(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestMissingSourceDoesNotCreate(t *testing.T) {
	s := specFor(t)
	s.SourceDir = filepath.Join(s.SourceDir, "missing")
	if _, err := Acquire(context.Background(), s); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.SourceDir); !os.IsNotExist(err) {
		t.Fatal("created source")
	}
}
func TestClaimSubprocessHelper(t *testing.T) {
	mode := os.Getenv("ALIGNMENT_PROFILE_CHILD")
	if mode == "" {
		t.Skip("helper invoked only by subprocess fixtures")
	}
	c, err := Acquire(context.Background(), Spec{DriverType: "claude", SourceDir: os.Getenv("ALIGNMENT_PROFILE_SOURCE")})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "unseeded" {
		_ = c.Initialize(context.Background(), func(context.Context, string) error {
			fmt.Println("PROFILE_READY")
			bufio.NewScanner(os.Stdin).Scan()
			os.Exit(0)
			return nil
		})
		t.Fatal("child did not exit inside seed")
	}
	if err := c.Initialize(context.Background(), seedTest); err != nil {
		t.Fatal(err)
	}
	if mode == "active" {
		if err := c.BeginUse(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("PROFILE_READY")
	bufio.NewScanner(os.Stdin).Scan()
	if mode == "clean" {
		if err := c.ReleaseClean(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0)
}
func TestCrossProcessOwnershipAndCrash(t *testing.T) {
	for _, mode := range []string{"clean", "ready", "active", "unseeded"} {
		t.Run(mode, func(t *testing.T) {
			s := specFor(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaimSubprocessHelper$")
			cmd.Env = append(os.Environ(), "ALIGNMENT_PROFILE_CHILD="+mode, "ALIGNMENT_PROFILE_SOURCE="+s.SourceDir)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = in.Close()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			})
			scan := bufio.NewScanner(out)
			if !scan.Scan() || scan.Text() != "PROFILE_READY" {
				t.Fatal("child did not acquire")
			}
			if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrInUse) {
				t.Fatalf("cross process conflict=%v", err)
			}
			in.Write([]byte("exit\n"))
			in.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			next, err := Acquire(context.Background(), s)
			if mode == "active" {
				if !errors.Is(err, profile.ErrRecoveryRequired) {
					t.Fatalf("dirty generation accepted: %v", err)
				}
				key := hashFields("adaptor/hosted-profile-key/v1", "claude", s.SourceDir, "", "", "", "")
				raw, err := os.ReadFile(filepath.Join(s.SourceDir+".hosted-tools", "v1", key, "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				var state stateRecord
				json.Unmarshal(raw, &state)
				if state.Phase != "active" {
					t.Fatal("recovery refusal changed generation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unseeded" {
					readyTest(t, next)
					entries, err := os.ReadDir(filepath.Dir(next.Dir()))
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range entries {
						if strings.HasPrefix(e.Name(), ".seed-") {
							t.Fatal("crashed owned seed staging remained")
						}
					}
				}
				if err := next.ReleaseUnused(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestStrictMarkers(t *testing.T) {
	for _, raw := range []string{`{"version":1,"phase":"ready","generation":""} {}`, `{"version":1,"phase":"ready","generation":"","phase":"ready"}`, `{"version":1,"phase":"ready"}`, "{\"version\":1,\"phase\":\"\xff\",\"generation\":\"\"}"} {
		var s stateRecord
		if err := DecodeJSON([]byte(raw), &s, true); !errors.Is(err, profile.ErrUnsafe) {
			t.Fatalf("accepted %q: %v", raw, err)
		}
	}
}

func TestAcquireDoesNotAdoptUnmarkedDirectories(t *testing.T) {
	s := specFor(t)
	if err := os.Mkdir(s.SourceDir+".hosted-tools", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatalf("unmarked namespace adopted: %v", err)
	}
	entries, err := os.ReadDir(s.SourceDir + ".hosted-tools")
	if err != nil || len(entries) != 0 {
		t.Fatal("unmarked namespace changed")
	}
}
func TestValidateDetectsCachedDirectoryAndGenerationTampering(t *testing.T) {
	for _, kind := range []string{"state", "namespace", "lock"} {
		t.Run(kind, func(t *testing.T) {
			s := specFor(t)
			c := acquireTest(t, s)
			readyTest(t, c)
			if err := c.BeginUse(context.Background()); err != nil {
				t.Fatal(err)
			}
			var path string
			switch kind {
			case "state":
				path = filepath.Join(filepath.Dir(c.Dir()), "state.json")
			case "namespace":
				path = filepath.Join(s.SourceDir+".hosted-tools", "namespace.json")
			case "lock":
				path = filepath.Join(filepath.Dir(c.Dir()), "owner.lock")
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "lock" && runtime.GOOS == "windows" {
				if err := os.Rename(path, path+"-old"); err == nil {
					t.Fatal("Windows allowed owned lock replacement")
				}
				return
			}
			if kind == "lock" {
				if err := os.Rename(path, path+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte(`{"version":1,"phase":"ready","generation":""}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Validate(context.Background()); !errors.Is(err, profile.ErrUnsafe) {
				t.Fatalf("tampering accepted: %v", err)
			}
			if kind == "lock" {
				os.Remove(path)
				os.Rename(path+"-old", path)
			} else {
				os.WriteFile(path, original, 0600)
			}
		})
	}
}
func TestAuthLinkIdentity(t *testing.T) {
	s := specFor(t)
	auth := filepath.Join(s.SourceDir, ".credentials.json")
	if err := os.WriteFile(auth, []byte("auth-before"), 0600); err != nil {
		t.Fatal(err)
	}
	c := acquireTest(t, s)
	if err := c.Initialize(context.Background(), func(_ context.Context, dir string) error {
		return os.Symlink(auth, filepath.Join(dir, ".credentials.json"))
	}); err != nil {
		t.Fatal(err)
	}
	seedBefore, err := os.ReadFile(filepath.Join(filepath.Dir(c.Dir()), "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auth, []byte("auth-after"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	seedAfter, _ := os.ReadFile(filepath.Join(filepath.Dir(c.Dir()), "seed.json"))
	if string(seedBefore) != string(seedAfter) || strings.Contains(string(seedAfter), "auth-before") {
		t.Fatal("auth contents entered seed")
	}
	linked := filepath.Join(c.Dir(), ".credentials.json")
	os.Remove(linked)
	os.Symlink(filepath.Join(t.TempDir(), "external"), linked)
	if err := c.Validate(context.Background()); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal("different auth target accepted")
	}
	os.Remove(linked)
	os.Symlink(auth, linked)
}

func TestBeginUseRetryMustPersistActive(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	readyTest(t, c)
	failure := errors.New("write failed")
	c.beforeStateWrite = func() error { return failure }
	if err := c.BeginUse(context.Background()); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := c.BeginUse(context.Background()); !errors.Is(err, failure) {
		t.Fatal("retry reported success without durable active")
	}
	c.beforeStateWrite = nil
	if err := c.BeginUse(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state stateRecord
	if err := readRecord(c.root, "state.json", &state); err != nil {
		t.Fatal(err)
	}
	if state.Phase != "active" || state.Generation != c.state.Generation {
		t.Fatal("active generation not durable")
	}
}

func TestReleaseAfterUnlockFailureDoesNotTouchSuccessor(t *testing.T) {
	for _, stage := range []string{"lock-close", "root-close"} {
		t.Run(stage, func(t *testing.T) {
			s := specFor(t)
			c := acquireTest(t, s)
			readyTest(t, c)
			if err := c.BeginUse(context.Background()); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("post-unlock close failure")
			if stage == "lock-close" {
				c.beforeLockClose = func() error { return failure }
			} else {
				c.beforeRootClose = func() error { return failure }
			}
			if err := c.ReleaseClean(context.Background()); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			next := acquireTest(t, s)
			readyTest(t, next)
			if err := next.BeginUse(context.Background()); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(filepath.Dir(next.Dir()), "state.json")
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			c.beforeLockClose = nil
			c.beforeRootClose = nil
			if err := c.ReleaseClean(context.Background()); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(statePath)
			if err != nil || string(before) != string(after) {
				t.Fatal("old release overwrote successor generation")
			}
			if err := next.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrInUse) {
				t.Fatalf("old cleanup disturbed successor lock: %v", err)
			}
		})
	}
}

func TestOversizedControlMarkerIsRejected(t *testing.T) {
	s := specFor(t)
	c := acquireTest(t, s)
	readyTest(t, c)
	state := filepath.Join(filepath.Dir(c.Dir()), "state.json")
	if err := c.ReleaseUnused(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(strings.Repeat(" ", markerLimit+1)+`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(context.Background(), s); !errors.Is(err, profile.ErrUnsafe) {
		t.Fatal(err)
	}
}
