// Package hostedprofile owns private, persistent WithTools execution directories.
// It provides local OS ownership; a released kernel lock does not certify that a
// crashed owner's provider children exited. An active disk generation fails closed.
package hostedprofile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/profile"
)

type Spec struct {
	DriverType string
	SourceDir  string
	Identity   driver.AgentIdentity
}
type Claim struct {
	mu                  sync.Mutex
	spec                Spec
	owner               ownerRecord
	root                *os.Root
	parent              *os.Root
	namespace           *os.Root
	version             *os.Root
	lock                *os.File
	dir                 string
	state               stateRecord
	begun               bool
	initializeAttempted bool
	initializeErr       error
	activationComplete  bool
	released            bool
	readyWritten        bool
	unlocked            bool
	closing             bool
	beforeLockClose     func() error
	beforeRootClose     func() error
	seed                seedRecord
	// Per-claim fault boundaries support deterministic shutdown retry fixtures.
	beforeStateWrite func() error
	beforeUnlock     func() error
}

func Acquire(ctx context.Context, spec Spec) (*Claim, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, id, err := CanonicalSource(spec.SourceDir)
	if err != nil {
		return nil, err
	}
	spec.SourceDir = source
	identity := spec.Identity
	key := hashFields("adaptor/hosted-profile-key/v1", spec.DriverType, source, identity.ID, identity.TenantID, identity.ProfileID, identity.Name)
	owner := ownerRecord{Format: "agent-adaptor/hosted-profile-owner", Version: 1, KeyHash: key, DriverType: spec.DriverType, SourceDir: source, SourceID: id, IdentityHash: hashFields(identity.ID, identity.TenantID, identity.ProfileID, identity.Name)}
	ns := namespaceRecord{Format: "agent-adaptor/hosted-profile-namespace", Version: 1, SourceDir: source, SourceID: id}
	parent, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return nil, err
	}
	c := &Claim{spec: spec, owner: owner, parent: parent, dir: filepath.Join(source+".hosted-tools", "v1", key, "profile")}
	success := false
	defer func() {
		if !success {
			c.closeHandles()
		}
	}()
	name := filepath.Base(source) + ".hosted-tools"
	if err := publishDir(parent, name, func(r *os.Root) error {
		if err := writeRecord(r, "namespace.json", ns); err != nil {
			return err
		}
		return makePrivateDir(r, "v1")
	}); err != nil {
		return nil, err
	}
	c.namespace, err = checkedRoot(parent, name)
	if err != nil {
		return nil, err
	}
	var actualNS namespaceRecord
	if err := readRecord(c.namespace, "namespace.json", &actualNS); err != nil {
		return nil, err
	}
	if actualNS != ns {
		return nil, unsafe("namespace identity mismatch", nil)
	}
	c.version, err = checkedRoot(c.namespace, "v1")
	if err != nil {
		return nil, err
	}
	if err := publishDir(c.version, key, func(r *os.Root) error {
		if err := writeRecord(r, "owner.json", owner); err != nil {
			return err
		}
		if err := writeRecord(r, "state.json", stateRecord{Version: 1, Phase: "unseeded"}); err != nil {
			return err
		}
		return writeNew(r, "owner.lock", nil)
	}); err != nil {
		return nil, err
	}
	c.root, err = checkedRoot(c.version, key)
	if err != nil {
		return nil, err
	}
	var actualOwner ownerRecord
	if err := readRecord(c.root, "owner.json", &actualOwner); err != nil {
		return nil, err
	}
	if actualOwner != owner {
		return nil, unsafe("owner identity mismatch", nil)
	}
	c.lock, err = openOwnershipLock(c.root)
	if err != nil {
		return nil, err
	}
	if err := lockFile(c.lock); err != nil {
		return nil, err
	}
	if err := readRecord(c.root, "state.json", &c.state); err != nil {
		return nil, err
	}
	if !c.state.valid() {
		return nil, unsafe("invalid generation state", nil)
	}
	if c.state.Phase == "active" {
		return nil, profile.ErrRecoveryRequired
	}
	if c.state.Phase == "ready" {
		if err := c.validateReady(); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	success = true
	return c, nil
}
func (c *Claim) Dir() string {
	if c == nil {
		return ""
	}
	return c.dir
}
func (c *Claim) SourceDir() string {
	if c == nil {
		return ""
	}
	return c.spec.SourceDir
}
func (c *Claim) KeyHash() string {
	if c == nil {
		return ""
	}
	return c.owner.KeyHash
}
func (c *Claim) Validate(ctx context.Context) error {
	if c == nil {
		return unsafe("nil claim", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.root == nil || c.lock == nil || c.released || c.unlocked {
		return unsafe("released claim", nil)
	}
	for _, p := range []struct {
		parent *os.Root
		name   string
		held   *os.Root
	}{{c.parent, filepath.Base(c.spec.SourceDir) + ".hosted-tools", c.namespace}, {c.namespace, "v1", c.version}, {c.version, c.owner.KeyHash, c.root}} {
		f, err := openChecked(p.parent, p.name, true)
		if err != nil {
			return err
		}
		actual, _ := f.Stat()
		expected, err := p.held.Stat(".")
		f.Close()
		if err != nil || !os.SameFile(actual, expected) {
			return unsafe("owned directory identity changed", err)
		}
	}
	var ns namespaceRecord
	if err := readRecord(c.namespace, "namespace.json", &ns); err != nil {
		return err
	}
	if ns != (namespaceRecord{Format: "agent-adaptor/hosted-profile-namespace", Version: 1, SourceDir: c.owner.SourceDir, SourceID: c.owner.SourceID}) {
		return unsafe("namespace identity mismatch", nil)
	}
	var diskState stateRecord
	if err := readRecord(c.root, "state.json", &diskState); err != nil {
		return err
	}
	if !diskState.valid() {
		return unsafe("invalid generation state", nil)
	}
	if (!c.begun || c.activationComplete) && diskState != c.state {
		return unsafe("owned generation changed", nil)
	}
	var owner ownerRecord
	if err := readRecord(c.root, "owner.json", &owner); err != nil {
		return err
	}
	if owner != c.owner {
		return unsafe("owner identity mismatch", nil)
	}
	f, err := openChecked(c.root, "owner.lock", false)
	if err != nil {
		return err
	}
	defer f.Close()
	a, _ := f.Stat()
	b, _ := c.lock.Stat()
	if !os.SameFile(a, b) {
		return unsafe("lock identity changed", nil)
	}
	_, id, err := CanonicalSource(c.spec.SourceDir)
	if err != nil {
		return err
	}
	if id != c.owner.SourceID {
		return unsafe("source identity changed", nil)
	}
	if c.state.Phase != "unseeded" {
		return c.validateReady()
	}
	return nil
}
func (c *Claim) BeginUse(ctx context.Context) error {
	if c == nil {
		return unsafe("nil claim", nil)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.root == nil || c.lock == nil || c.released || c.unlocked || c.closing || c.state.Phase == "unseeded" {
		return unsafe("claim not ready", nil)
	}
	if c.activationComplete {
		return nil
	}
	if c.begun {
		if err := c.writeState(); err != nil {
			return err
		}
		c.activationComplete = true
		return nil
	}
	g, err := generation()
	if err != nil {
		return err
	}
	c.begun = true
	c.state = stateRecord{Version: 1, Phase: "active", Generation: g}
	if err := c.writeState(); err != nil {
		return err
	}
	c.activationComplete = true
	return nil
}
func (c *Claim) writeState() error {
	if c.beforeStateWrite != nil {
		if err := c.beforeStateWrite(); err != nil {
			return err
		}
	}
	return writeRecord(c.root, "state.json", c.state)
}

// ReleaseUnused is nil-safe and only releases a generation never made active.
func (c *Claim) ReleaseUnused(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.begun && !c.released {
		return unsafe("active claim requires clean shutdown", nil)
	}
	return c.release(ctx, false)
}

// ReleaseClean requires the Agent to have drained runs, stopped every writer,
// removed its projections, and successfully closed its gateway first.
func (c *Claim) ReleaseClean(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.release(ctx, true)
}
func (c *Claim) release(ctx context.Context, clean bool) error {
	if c.released {
		return nil
	}
	if !c.unlocked && (c.root == nil || c.lock == nil) {
		return unsafe("invalid claim", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.closing = true
	// Once unlocked, another Agent may already have published a new generation.
	// Retrying any later close must never read or overwrite its state.
	if !c.unlocked {
		if clean && c.begun && !c.readyWritten {
			old := c.state
			c.state = stateRecord{Version: 1, Phase: "ready"}
			if err := c.writeState(); err != nil {
				c.state = old
				return err
			}
			c.readyWritten = true
		}
		if c.beforeUnlock != nil {
			if err := c.beforeUnlock(); err != nil {
				return err
			}
		}
		if err := unlockFile(c.lock); err != nil {
			return err
		}
		c.unlocked = true
	}
	if c.lock != nil {
		if c.beforeLockClose != nil {
			if err := c.beforeLockClose(); err != nil {
				return err
			}
		}
		if err := c.lock.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			return err
		}
		c.lock = nil
	}
	if c.beforeRootClose != nil {
		if err := c.beforeRootClose(); err != nil {
			return err
		}
	}
	for _, ptr := range []**os.Root{&c.root, &c.version, &c.namespace, &c.parent} {
		if *ptr != nil {
			if err := (*ptr).Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				return err
			}
			*ptr = nil
		}
	}
	c.released = true
	return nil
}
func (c *Claim) closeHandles() error {
	var err error
	if c.lock != nil {
		err = errors.Join(err, c.lock.Close())
		c.lock = nil
	}
	for _, r := range []*os.Root{c.root, c.version, c.namespace, c.parent} {
		if r != nil {
			err = errors.Join(err, r.Close())
		}
	}
	return err
}
