package adaptor_test

import (
	"context"
	"errors"
	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/threadstore"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This fixture is executable on the unmodified baseline: the proposed channel
// must exist independently of Prompt and Instructions before any provider can use it.
func TestAlignmentPromptInitialContract(t *testing.T) {
	if _, ok := reflect.TypeFor[driver.Request]().FieldByName("AppendSystemPrompt"); !ok {
		t.Fatal("resolved Request has no native append channel")
	}
	if _, ok := reflect.TypeFor[*adaptor.RunSettings]().MethodByName("SetAppendSystemPrompt"); !ok {
		t.Fatal("append cannot be configured through the shared scope contract")
	}
}

type alignmentPromptProbe struct {
	*fakeDriver
	probes   int
	captured string
}

func (d *alignmentPromptProbe) CheckEnvironment(_ context.Context, _ any) (driver.EnvironmentReport, error) {
	d.probes++
	return driver.EnvironmentReport{DriverType: d.captured}, nil
}
func alignmentPromptCapable(d *fakeDriver) {
	desc := d.Descriptor()
	desc.SystemPrompt.Append = true
	desc.Instructions.Supported = true
	d.descriptor = &desc
}
func TestAlignmentPromptScopeAndInspect(t *testing.T) {
	d := newFakeDriver()
	alignmentPromptCapable(d)
	probe := &alignmentPromptProbe{fakeDriver: d, captured: "configured-model"}
	a := adaptor.New(probe, adaptor.WithAppendSystemPrompt("default\n甲"), adaptor.WithInstructions("independent"))
	report, err := a.Inspect().Environment(context.Background())
	if err != nil || report.DriverType != "configured-model" || probe.probes != 1 || d.runCount() != 0 {
		t.Fatalf("Inspect=%#v, %v", report, err)
	}
	for _, tc := range []struct {
		name, want string
		opts       []adaptor.CallOption
	}{
		{"default", "default\n甲", nil}, {"override", "甲\n\"乙\"", []adaptor.CallOption{adaptor.WithAppendSystemPrompt("first"), adaptor.WithAppendSystemPrompt("甲\n\"乙\"")}},
		{"clear", "", []adaptor.CallOption{adaptor.WithAppendSystemPrompt("")}}, {"spaces", " \n\t ", []adaptor.CallOption{adaptor.WithAppendSystemPrompt(" \n\t ")}},
		{"large", strings.Repeat("字", 15000), []adaptor.CallOption{adaptor.WithAppendSystemPrompt(strings.Repeat("字", 15000))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				res, err := alignmentCall(t, a, context.Background(), streaming, tc.opts...)
				if err != nil || res.Text != "ok" {
					t.Fatal(res, err)
				}
				req := d.lastRequest(t)
				if req.AppendSystemPrompt != tc.want || req.Prompt != "work" || req.Instructions.Content != "independent" {
					t.Fatalf("channels were rewritten: %#v", req)
				}
				if len(req.ProfilePayload.Config.Patches) != 0 {
					t.Fatal("append became profile state")
				}
			}
		})
	}
	if _, err := a.Run(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	if d.lastRequest(t).AppendSystemPrompt != "default\n甲" {
		t.Fatal("call changed construction defaults")
	}
}

type alignmentPromptStore struct {
	threadstore.Store
	acquires int
}

func (s *alignmentPromptStore) AcquireLease(ctx context.Context, resource, owner string, ttl time.Duration) (threadstore.Lease, error) {
	s.acquires++
	return s.Store.AcquireLease(ctx, resource, owner, ttl)
}
func TestAlignmentPromptPreflight(t *testing.T) {
	for _, tc := range []struct{ text, reason string }{{"requested", "unsupported_driver"}, {string([]byte{0xff, 0}), "invalid_utf8"}, {"bad\x00text", "nul_byte"}} {
		t.Run(tc.reason, func(t *testing.T) {
			d := newFakeDriver()
			probe := &alignmentPromptProbe{fakeDriver: d}
			store := &alignmentPromptStore{Store: memory.NewStore()}
			acquired := 0
			service := &alignmentPromptService{attach: func() { acquired++ }}
			a := adaptor.New(probe, adaptor.WithThreadStore(store), adaptor.WithRunServices(service), adaptor.WithAppendSystemPrompt(tc.text))
			for _, runner := range []adaptor.Runner{a, a.Thread("opaque:key")} {
				for _, streaming := range []bool{false, true} {
					_, err := alignmentCall(t, runner, context.Background(), streaming)
					var typed *adaptor.SystemPromptUnsupportedError
					if !errors.Is(err, adaptor.ErrSystemPromptUnsupported) || !errors.As(err, &typed) || typed.Reason != tc.reason {
						t.Fatalf("preflight=%v", err)
					}
				}
			}
			if _, err := a.Inspect().Environment(context.Background()); !errors.Is(err, adaptor.ErrSystemPromptUnsupported) {
				t.Fatal(err)
			}
			if acquired != 0 || store.acquires != 0 || probe.probes != 0 || d.runCount() != 0 {
				t.Fatal("preflight acquired resources or invoked driver")
			}
			if _, err := a.Run(context.Background(), "work", adaptor.WithAppendSystemPrompt("")); err != nil {
				t.Fatal("empty clear still needs append capability", err)
			}
		})
	}
}

type alignmentPromptService struct{ attach func() }

func (s *alignmentPromptService) AttachRun(context.Context, string) (adaptor.RunAttachment, error) {
	s.attach()
	return adaptor.RunAttachment{}, nil
}
func (s *alignmentPromptService) DetachRun(context.Context, string) error { return nil }
func TestAlignmentPromptThreadFingerprint(t *testing.T) {
	ctx := context.Background()
	d := newSessionFake("append")
	alignmentPromptCapable(d.fakeDriver)
	store := memory.NewStore()
	a := adaptor.New(d, adaptor.WithThreadStore(store))
	if _, err := a.Thread("parent").Run(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	empty := *activeRecord(t, store, "parent")
	if _, err := a.Thread("parent", adaptor.ResumeOnly()).Run(ctx, "same", adaptor.WithAppendSystemPrompt("")); err != nil {
		t.Fatal("unset history invalidated", err)
	}
	if activeRecord(t, store, "parent").ID != empty.ID {
		t.Fatal("empty append rebound old record")
	}
	before := *activeRecord(t, store, "parent")
	calls := d.runCount()
	if _, err := a.Thread("parent", adaptor.ResumeOnly()).Run(ctx, "changed", adaptor.WithAppendSystemPrompt("甲")); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatal(err)
	}
	if d.runCount() != calls || !reflect.DeepEqual(before, *activeRecord(t, store, "parent")) {
		t.Fatal("incompatible resume changed old record")
	}
	if _, err := a.Thread("parent").Fork("child").Run(ctx, "fork", adaptor.WithAppendSystemPrompt("甲")); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatal("incompatible fork accepted", err)
	}
	if rec, err := store.Resolve(ctx, threadstore.Query{Key: "child"}); err != nil || rec != nil {
		t.Fatal("orphan fork", rec, err)
	}
	if _, err := a.Thread("parent").Run(ctx, "change", adaptor.WithAppendSystemPrompt("甲")); err != nil {
		t.Fatal(err)
	}
	appended := *activeRecord(t, store, "parent")
	if appended.ID == empty.ID || appended.Fingerprint == empty.Fingerprint {
		t.Fatal("append did not affect durable compatibility")
	}
	if _, err := a.Thread("parent", adaptor.ResumeOnly()).Run(ctx, "same", adaptor.WithAppendSystemPrompt("甲")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Thread("parent", adaptor.ResumeOnly()).Run(ctx, "clear"); !errors.Is(err, adaptor.ErrThreadIncompatible) {
		t.Fatal("clear resumed old append context", err)
	}
	if _, err := a.Thread("parent").Run(ctx, "clear"); err != nil {
		t.Fatal(err)
	}
	if activeRecord(t, store, "parent").ID == appended.ID {
		t.Fatal("clear did not rebuild")
	}
}
