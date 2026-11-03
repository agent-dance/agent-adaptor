//go:build windows

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-dance/agent-adaptor/v2/threadstore"
)

func TestWindowsCheckpointReplacementAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := openDiskStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lease, err := s.AcquireLease(ctx, "k", "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		req := threadstore.FinalizeRequest{Key: "k", HeldLeases: []threadstore.Lease{lease}, Record: threadstore.Record{ID: id, Key: "k", Status: threadstore.StatusActive}}
		if id == "second" {
			req.ArchiveOld = true
			req.PreviousID = "first"
		}
		if err := s.Finalize(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := openDiskStore(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reopened.Resolve(ctx, threadstore.Query{Key: "k"})
	if err != nil || r == nil || r.ID != "second" {
		t.Fatalf("%v %v", r, err)
	}
}
