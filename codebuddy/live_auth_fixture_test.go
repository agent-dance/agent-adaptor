package codebuddy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

const codeBuddySyntheticNativeSession = "{\n  \"auth\": {\"domain\": \"synthetic.invalid\", \"token\": \"not-a-credential\"},\n  \"unknown\": [1, {\"keep\": \"原字节\\n\"}]\n}\n"

type codeBuddyAuthCapture struct {
	Home, Profile            string
	NativeExact, HomeMatches bool
}

// This test binary only sees generated fixture bytes, never the real CLI or
// operator credentials. Keep the path oracle independent of the seed helper.
func runCodeBuddyLiveAuthFixture() int {
	home := os.Getenv("HOME")
	parts := []string{".local", "share", "CodeBuddyExtension"}
	if runtime.GOOS == "darwin" {
		parts = []string{"Library", "Application Support", "CodeBuddyExtension"}
	} else if runtime.GOOS == "windows" {
		parts = []string{"AppData", "Local", "CodeBuddyExtension"}
	}
	parts = append([]string{home}, append(parts, "Data", "Public", "auth", "Tencent-Cloud.coding-copilot.info")...)
	raw, err := os.ReadFile(filepath.Join(parts...))
	capture := codeBuddyAuthCapture{Home: home, Profile: os.Getenv("CODEBUDDY_CONFIG_DIR"), NativeExact: err == nil && string(raw) == codeBuddySyntheticNativeSession, HomeMatches: home == os.Getenv("USERPROFILE")}
	data, _ := json.Marshal(capture)
	if os.WriteFile(os.Getenv("CODEBUDDY_AUTH_FIXTURE_CAPTURE"), data, 0600) != nil {
		return 84
	}
	if !capture.NativeExact || !capture.HomeMatches {
		return 83
	}
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"session_id":"auth-fixture-session","result":"NATIVE_SEED_EXACT"}`)
	return 0
}

// codeBuddyLiveHome owns one test-only HOME, not a provider profile cache.
// Agent.Close is registered after this fixture, so testing cleanup closes the
// provider before removing credentials and any run-created files.
type codeBuddyLiveHome struct {
	apiKeyDisabled string
	home, profile  string
	parent, root   *os.Root
	directory      *os.File
	identity       os.FileInfo
	directories    []*os.File
	native         bool
	closed         bool
}

type codeBuddyLiveAuthSeed struct {
	apiKeyDisabled        string
	nativeFile, configDir string
	environment           bool
}

var codeBuddyNativeAuthConflicts = []string{
	"CODEBUDDY_AUTH_TOKEN", "CODEBUDDY_BASE_URL", "CODEBUDDY_CUSTOM_HEADERS",
	"CODEBUDDY_INTERNET_ENVIRONMENT", "CODEBUDDY_INTERNET_ENVIROMENT",
	"ACC_PRODUCT_CONFIG_PATH", "ACC_PRODUCT_CONFIG_V2", "ACC_PRODUCT_CONFIG_V3",
}

func codeBuddyLiveSeedFromEnv(getenv func(string) string) (codeBuddyLiveAuthSeed, error) {
	seed := codeBuddyLiveAuthSeed{nativeFile: getenv("CODEBUDDY_NATIVE_AUTH_FILE_SOURCE"), configDir: getenv("CODEBUDDY_CONFIG_DIR_SOURCE")}
	seed.apiKeyDisabled = getenv("CODEBUDDY_API_KEY_DISABLED")
	seed.environment = getenv("CODEBUDDY_API_KEY") != "" && seed.apiKeyDisabled == "" || getenv("CODEBUDDY_AUTH_TOKEN") != ""
	if seed.nativeFile != "" {
		if getenv("CODEBUDDY_API_KEY") != "" && seed.apiKeyDisabled == "" {
			return seed, fmt.Errorf("native authentication fixture conflicts with active CODEBUDDY_API_KEY")
		}
		for _, name := range codeBuddyNativeAuthConflicts {
			if getenv(name) != "" {
				return seed, fmt.Errorf("native authentication fixture conflicts with %s", name)
			}
		}
		if getenv("CODEBUDDY_CREDENTIALS_IN_MEMORY") == "1" {
			return seed, fmt.Errorf("native authentication fixture requires file-backed storage")
		}

	}
	if seed.nativeFile == "" && seed.configDir == "" && !seed.environment {
		return seed, fmt.Errorf("live authentication requires an explicit native seed, credentials source or environment token")
	}
	return seed, nil
}

func codeBuddyNativeAuthPath(goos string) (string, error) {
	var parts []string
	switch goos {
	case "darwin":
		parts = []string{"Library", "Application Support", "CodeBuddyExtension"}
	case "windows":
		parts = []string{"AppData", "Local", "CodeBuddyExtension"}
	case "linux":
		parts = []string{".local", "share", "CodeBuddyExtension"}
	default:
		return "", fmt.Errorf("native live authentication fixture is unsupported on this platform")
	}
	return filepath.Join(append(parts, "Data", "Public", "auth", "Tencent-Cloud.coding-copilot.info")...), nil
}

func newCodeBuddyLiveHome(t *testing.T) *codeBuddyLiveHome {
	t.Helper()
	f, err := makeCodeBuddyLiveHome(t.TempDir())
	if err != nil {
		t.Fatalf("create private live HOME: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("remove private live HOME: %v", err)
		}
	})
	return f
}

func makeCodeBuddyLiveHome(parent string) (_ *codeBuddyLiveHome, resultErr error) {
	f := &codeBuddyLiveHome{home: filepath.Join(parent, "home")}
	f.profile = filepath.Join(f.home, "profile")
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, f.Close())
		}
	}()
	var err error
	f.parent, err = os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	f.directory, err = createCodeBuddyPrivateObject(f.parent, "home", true)
	if err != nil {
		return nil, err
	}
	f.identity, err = f.directory.Stat()
	if err != nil {
		return nil, err
	}
	f.root, err = f.parent.OpenRoot("home")
	if err != nil {
		return nil, err
	}
	for _, path := range []string{"profile", "tmp", "AppData/Local", "AppData/Roaming", ".config", ".local/share"} {
		if err := f.mkdir(filepath.FromSlash(path)); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *codeBuddyLiveHome) mkdir(path string) error {
	current := ""
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return os.ErrInvalid
		}
		previous := current
		current = filepath.Join(current, part)
		info, err := f.root.Lstat(current)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return os.ErrPermission
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if previous == "" {
			previous = "."
		}
		parent, err := f.root.OpenRoot(previous)
		if err != nil {
			return err
		}
		dir, createErr := createCodeBuddyPrivateObject(parent, part, true)
		closeErr := parent.Close()
		if createErr != nil {
			return errors.Join(createErr, closeErr)
		}
		f.directories = append(f.directories, dir)
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (f *codeBuddyLiveHome) write(path string, data []byte) (resultErr error) {
	if err := f.mkdir(filepath.Dir(path)); err != nil {
		return err
	}
	parent, err := f.root.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, parent.Close()) }()
	file, err := createCodeBuddyPrivateObject(parent, filepath.Base(path), false)
	if err != nil {
		return err
	}
	// The file handle is deliberately closed before spawning the CLI: native
	// credential migration/refresh may legitimately replace this private file.
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

// Walk from the volume root, retaining every parent handle. OpenRoot on the
// entire parent path alone would silently follow an intermediate symlink.
func openCodeBuddyLiveSeedParent(path string) (_ *os.Root, finish func() error, resultErr error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, fmt.Errorf("authentication seed path must be absolute and clean")
	}
	volumeRoot := filepath.VolumeName(path) + string(filepath.Separator)
	root, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, nil, err
	}
	roots := []*os.Root{root}
	var names []string
	var identities []os.FileInfo
	finish = func() error {
		var err error
		for i, name := range names {
			info, statErr := roots[i].Lstat(name)
			if statErr != nil || unsafeCodeBuddyLiveInfo(info) || !os.SameFile(info, identities[i]) {
				err = errors.Join(err, os.ErrPermission, statErr)
			}
		}
		for i := len(roots) - 1; i >= 0; i-- {
			err = errors.Join(err, roots[i].Close())
		}
		return err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, finish())
			finish = nil
		}
	}()
	parent := strings.TrimPrefix(filepath.Dir(path), volumeRoot)
	if parent != "" && parent != "." {
		for _, part := range strings.Split(parent, string(filepath.Separator)) {
			info, err := root.Lstat(part)
			if err != nil {
				return nil, finish, err
			}
			if !info.IsDir() || unsafeCodeBuddyLiveInfo(info) {
				return nil, finish, os.ErrPermission
			}
			child, err := root.OpenRoot(part)
			if err != nil {
				return nil, finish, err
			}
			roots = append(roots, child)
			names = append(names, part)
			identities = append(identities, info)
			opened, err := child.Stat(".")
			if err != nil || !os.SameFile(info, opened) {
				return nil, finish, errors.Join(os.ErrPermission, err)
			}
			root = child
		}
	}
	return root, finish, nil
}

func readCodeBuddyLiveSeed(path string, native bool) (_ []byte, resultErr error) {
	parent, finish, err := openCodeBuddyLiveSeedParent(path)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, finish()) }()
	name := filepath.Base(path)
	checkLogout := func() error {
		if !native {
			return nil
		}
		if _, err := parent.Lstat(name + ".logged-out"); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(fmt.Errorf("native authentication seed has a logout marker or unreadable marker state"), err)
		}
		return nil
	}
	if err := checkLogout(); err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, checkLogout()) }()
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || unsafeCodeBuddyLiveInfo(before) {
		return nil, fmt.Errorf("authentication seed must be a regular non-link file")
	}
	file, err := openCodeBuddyLiveSeed(parent, name)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, before) || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
		return nil, os.ErrPermission
	}
	const maxSize = 8 << 20
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, err
	}
	after, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, errors.Join(os.ErrPermission, err)
	}
	if len(data) == 0 || len(data) > maxSize {
		return nil, fmt.Errorf("authentication seed is empty or exceeds 8 MiB")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil || len(object) == 0 {
		return nil, fmt.Errorf("authentication seed must be a nonempty JSON object")
	}
	return data, nil
}

// Both preflight and materialization use this reader. Preflight performs no
// spawn or writes; execution rereads the seed rather than trusting old bytes.
func codeBuddyLiveAuthFiles(seed codeBuddyLiveAuthSeed) (map[string][]byte, error) {
	files := map[string][]byte{}
	if seed.nativeFile != "" {
		path, err := codeBuddyNativeAuthPath(runtime.GOOS)
		if err != nil {
			return nil, err
		}
		data, err := readCodeBuddyLiveSeed(seed.nativeFile, true)
		if err != nil {
			return nil, fmt.Errorf("read explicit native authentication seed: %w", err)
		}
		files[path] = data
		return files, nil
	}
	if seed.configDir != "" {
		if !filepath.IsAbs(seed.configDir) {
			return nil, fmt.Errorf("credentials source must be absolute")
		}
		info, err := os.Lstat(seed.configDir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || unsafeCodeBuddyLiveInfo(info) {
			return nil, fmt.Errorf("credentials source must be a non-link directory")
		}
		for _, name := range []string{".credentials.json", "credentials.json"} {
			data, err := readCodeBuddyLiveSeed(filepath.Join(seed.configDir, name), false)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read explicit authentication fixture: %w", err)
			}
			files[filepath.Join("profile", name)] = data
		}
	}
	if len(files) == 0 && !seed.environment {
		return nil, fmt.Errorf("explicit authentication fixture contains no login material")
	}
	return files, nil
}

func (f *codeBuddyLiveHome) seed(seed codeBuddyLiveAuthSeed) error {
	files, err := codeBuddyLiveAuthFiles(seed)
	if err != nil {
		return err
	}
	for path, data := range files {
		if err := f.write(path, data); err != nil {
			return err
		}
	}
	f.native = seed.nativeFile != ""
	f.apiKeyDisabled = seed.apiKeyDisabled
	if f.native && f.apiKeyDisabled == "" {
		f.apiKeyDisabled = "1"
	}
	return nil
}

func (f *codeBuddyLiveHome) bindings() []driver.EnvBinding {
	out := []driver.EnvBinding{
		{Name: "HOME", Value: f.home}, {Name: "USERPROFILE", Value: f.home},
		{Name: "CODEBUDDY_CONFIG_DIR", Value: f.profile},
		{Name: "APPDATA", Value: filepath.Join(f.home, "AppData", "Roaming")},
		{Name: "LOCALAPPDATA", Value: filepath.Join(f.home, "AppData", "Local")},
		{Name: "XDG_CONFIG_HOME", Value: filepath.Join(f.home, ".config")},
		{Name: "XDG_DATA_HOME", Value: filepath.Join(f.home, ".local", "share")},
		{Name: "TMPDIR", Value: filepath.Join(f.home, "tmp")},
		{Name: "TMP", Value: filepath.Join(f.home, "tmp")}, {Name: "TEMP", Value: filepath.Join(f.home, "tmp")},
	}
	if f.native {
		out = append(out, driver.EnvBinding{Name: "CODEBUDDY_API_KEY_DISABLED", Value: f.apiKeyDisabled})
	}
	return out
}

func (f *codeBuddyLiveHome) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	var err error
	for i := len(f.directories) - 1; i >= 0; i-- {
		err = errors.Join(err, f.directories[i].Close())
	}
	if f.root != nil {
		// Remove children through the held root. RemoveAll does not follow a
		// replacement symlink; the root pathname is never recursively removed.
		directory, openErr := f.root.Open(".")
		err = errors.Join(err, openErr)
		if openErr == nil {
			names, readErr := directory.Readdirnames(-1)
			err = errors.Join(err, readErr, directory.Close())
			for _, name := range names {
				err = errors.Join(err, f.root.RemoveAll(name))
			}
		}
		err = errors.Join(err, f.root.Close())
	}
	if f.directory != nil {
		err = errors.Join(err, f.directory.Close())
	}
	if f.parent != nil {
		if f.identity != nil {
			info, statErr := f.parent.Lstat("home")
			if statErr != nil || !os.SameFile(info, f.identity) {
				err = errors.Join(err, os.ErrPermission, statErr)
			} else {
				err = errors.Join(err, f.parent.Remove("home"))
			}
		}
		err = errors.Join(err, f.parent.Close())
	}
	return err
}
