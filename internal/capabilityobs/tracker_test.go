package capabilityobs

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/capability"
)

func fact(id string) capability.Invocation {
	return capability.Invocation{InvocationID: id, Ref: capability.Ref{Kind: capability.MCP, Key: "知识._", Operation: "_search"}, Phase: capability.Started, Evidence: capability.ProviderProtocol, Source: capability.Provider, OccurredAt: time.Now().UTC()}
}
func TestCatalogExactAmbiguous(t *testing.T) {
	for _, entries := range [][]Entry{{{capability.MCP, "name", "a"}, {capability.MCP, "name", "b"}, {capability.MCP, "name", "a"}}, {{capability.MCP, "name", "b"}, {capability.MCP, "name", "a"}}} {
		c, e := NewCatalog(entries)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Lookup(capability.MCP, "name"); !errors.Is(e, ErrAmbiguous) {
			t.Fatal(e)
		}
	}
	c, e := NewCatalog([]Entry{{capability.MCP, "知识._", "key"}, {capability.MCP, "知识._", "key"}})
	if e != nil {
		t.Fatal(e)
	}
	if k, e := c.Lookup(capability.MCP, "知识._"); e != nil || k != "key" {
		t.Fatal(k, e)
	}
	if _, e := c.Lookup(capability.MCP, "知识__"); !errors.Is(e, ErrUnknown) {
		t.Fatal("helper invented provider alias")
	}
	for _, e := range []Entry{{"", "a", "a"}, {capability.Skill, "a", ""}, {capability.Skill, "a", "bad\x00"}} {
		if _, err := NewCatalog([]Entry{e}); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}
func TestTrackerExactlyOnceAndCloseOrder(t *testing.T) {
	tr := NewTracker()
	v := fact("a")
	out, e := tr.Start(v)
	if e != nil || out == nil {
		t.Fatal(e)
	}
	if out, e = tr.Start(v); e != nil || out != nil {
		t.Fatal("replay emitted")
	}
	other := v
	other.Ref.Key = "other"
	if _, e = tr.Start(other); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	_, _ = tr.Start(fact("b"))
	z := time.Duration(0)
	out, e = tr.Terminal(Key{"", "a"}, capability.Completed, "", time.Now().UTC(), &z)
	if e != nil || out.Duration == nil {
		t.Fatal(e)
	}
	*out.Duration = 4
	if out, e = tr.Terminal(Key{"", "a"}, capability.Completed, "", time.Now().UTC(), &z); e != nil || out != nil {
		t.Fatal("terminal copy/replay", e)
	}
	_, _ = tr.Start(fact("c"))
	closed, e := tr.Close(capability.Interrupted, capability.RunInterrupted, time.Now().UTC())
	if e != nil || len(closed) != 2 || closed[0].InvocationID != "b" || closed[1].InvocationID != "c" {
		t.Fatal(closed, e)
	}
	if _, e = tr.Start(fact("d")); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if v, e := tr.Close(capability.Interrupted, "", time.Now().UTC()); e != nil || len(v) != 0 {
		t.Fatal(v, e)
	}
}
func TestTrackerConcurrentReplay(t *testing.T) {
	tr := NewTracker()
	var wg sync.WaitGroup
	var count int
	var mu sync.Mutex
	v := fact("one")
	for range 24 {
		wg.Go(func() {
			out, e := tr.Start(v)
			if e != nil {
				t.Error(e)
			}
			if out != nil {
				mu.Lock()
				count++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if count != 1 {
		t.Fatal(count)
	}
}
func TestTrackerInvalidAtomic(t *testing.T) {
	tr := NewTracker()
	v := fact("a")
	v.Evidence = capability.HostLifecycle
	if _, e := tr.Start(v); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := tr.Terminal(Key{"", "a"}, capability.Completed, "", time.Now().UTC(), nil); !errors.Is(e, ErrUnknown) {
		t.Fatal(e)
	}
	v = fact("a")
	_, _ = tr.Start(v)
	if _, e := tr.Terminal(Key{"", "a"}, capability.Completed, capability.ToolFailed, time.Now().UTC(), nil); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if out, e := tr.Terminal(Key{"", "a"}, capability.Failed, capability.ToolFailed, time.Now().UTC(), nil); e != nil || out == nil {
		t.Fatal(e)
	}
}
