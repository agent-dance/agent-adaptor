package cliversion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/driver"
	"github.com/agent-dance/agent-adaptor/v2/internal/testutil"
)

func testProbe() *Probe {
	return &Probe{Pattern: regexp.MustCompile(`(?m)^([0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.-]+)?(?:\+[a-z0-9.-]+)?)\r?$`)}
}

func TestComparison(t *testing.T) {
	for _, tc := range []struct {
		version, minimum string
		date, below      bool
	}{
		{"0.99.0", "0.100.0", false, true},
		{"0.100.0", "0.100.0", false, false},
		{"0.100.0-alpha.1", "0.100.0", false, true},
		{"0.100.0+build", "0.100.0", false, false},
		{"0.166.0", "0.100.0", false, false},
		{"2.1.9", "2.1.110", false, true},
		{"2.1.110", "2.1.110", false, false},
		{"2.2.0", "2.1.110", false, false},
		{"2026.9.30-abc", "2026.10.14", true, true},
		{"2026.10.14-abc", "2026.10.14", true, false},
		{"2026.10.15-def", "2026.10.14", true, false},
		{"garbage", "2.1.110", false, false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if got := parse(tc.version, tc.date).Below(tc.minimum); got != tc.below {
				t.Fatalf("Below(%q, %q)=%v", tc.version, tc.minimum, got)
			}
		})
	}
	if parse("2026.02.30", true).Text != "" {
		t.Fatal("invalid calendar date accepted")
	}
}

func TestCacheConcurrentAndExecutableReplacement(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "probes")
	command := testutil.WriteCommand(t, dir, "cli",
		"#!/bin/sh\necho probe >> \"$COUNT\"\necho 1.2.3\n",
		"@echo off\r\necho probe>>\"%COUNT%\"\r\necho 1.2.3\r\n")
	cfg := driver.CommonConfig{Command: command, Env: []driver.EnvBinding{{Name: "COUNT", Value: count}}}
	p := testProbe()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if v := p.Detect(context.Background(), cfg, "unused"); v.Text != "1.2.3" {
				t.Errorf("version=%+v", v)
			}
		})
	}
	wg.Wait()
	raw, err := os.ReadFile(count)
	if err != nil || strings.Count(string(raw), "probe") != 1 {
		t.Fatalf("probe count: %q, %v", raw, err)
	}
	info, err := os.Stat(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(command)
	if err != nil {
		t.Fatal(err)
	}
	// Replacement identity must invalidate even if size and mtime are equal.
	replacement := command + ".replacement"
	if err := os.WriteFile(replacement, []byte(strings.ReplaceAll(string(raw), "1.2.3", "1.2.4")), info.Mode()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, command); err != nil {
		t.Fatal(err)
	}
	if v := p.Detect(context.Background(), cfg, "unused"); v.Text != "1.2.4" {
		t.Fatalf("replacement version=%+v", v)
	}
	// An in-place size/mtime change also invalidates the cache.
	f, err := os.OpenFile(command, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString("\n")
	if err := errors.Join(writeErr, f.Close()); err != nil {
		t.Fatal(err)
	}
	if v := p.Detect(context.Background(), cfg, "unused"); v.Text != "1.2.4" {
		t.Fatalf("modified version=%+v", v)
	}
	raw, err = os.ReadFile(count)
	if err != nil || strings.Count(string(raw), "probe") != 3 {
		t.Fatalf("invalidated probe count: %q, %v", raw, err)
	}
}

func TestUnknownAndTimeout(t *testing.T) {
	for _, tc := range []struct{ name, posix, windows string }{
		{"unparseable", "echo nonsense", "echo nonsense"},
		{"nonzero", "echo 1.2.3; exit 1", "echo 1.2.3\r\nexit /b 1"},
		{"timeout", "sleep 30", "ping -n 30 127.0.0.1 >nul"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProbe()
			p.timeout = 50 * time.Millisecond
			command := testutil.WriteCommand(t, t.TempDir(), "cli", "#!/bin/sh\n"+tc.posix+"\n", "@echo off\r\n"+tc.windows+"\r\n")
			v := p.Detect(context.Background(), driver.CommonConfig{Command: command}, "unused")
			if v.Text != "" || v.Detail == "" {
				t.Fatalf("version=%+v", v)
			}
			if tc.name == "timeout" && !strings.Contains(v.Detail, "timed out") {
				t.Fatalf("timeout=%+v", v)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v := testProbe().Detect(ctx, driver.CommonConfig{}, "unused"); v.Text != "" || v.Detail != context.Canceled.Error() {
		t.Fatalf("cancelled probe=%+v", v)
	}
}

func TestRunNoticeAndPortIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v := parse("1.0.0", false)
	caps := driver.MessageCapability{MinVersion: "2.0.0"}
	sink := &testutil.EventRecorder{}
	port := &unusablePort{}
	req := driver.Request{RunID: "multi-dispatch", Messages: port}
	for range 3 {
		got, err := v.PrepareRun(ctx, req, sink, "fixture", caps)
		if err != nil || got.Messages != nil || req.Messages != port {
			t.Fatalf("PrepareRun: port=%v, error=%v", got.Messages, err)
		}
	}
	events := sink.Snapshot()
	if len(events) != 1 || events[0].Metadata["code"] != "fixture_version_unsupported" {
		t.Fatalf("notices=%+v", events)
	}
	otherCtx, stop := context.WithCancel(context.Background())
	defer stop()
	if _, err := v.PrepareRun(otherCtx, req, sink, "fixture", caps); err != nil || len(sink.Snapshot()) != 2 {
		t.Fatalf("distinct execution lost notice: %v", err)
	}
}

type unusablePort struct{}

func (*unusablePort) Window(bool)                                { panic("version-gated port used") }
func (*unusablePort) Signal() <-chan struct{}                    { panic("version-gated port used") }
func (*unusablePort) Claim(driver.Boundary) []driver.MessageItem { panic("version-gated port used") }
func (*unusablePort) Ack(string, driver.MessageAck)              { panic("version-gated port used") }
