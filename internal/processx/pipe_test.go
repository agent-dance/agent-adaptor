package processx

import (
	"errors"
	"os"
	"sync"
	"testing"
)

func TestPipeFileClosesOnce(t *testing.T) {
	var mu sync.Mutex
	closes := make(map[*os.File]int)
	r, w, err := NewPipe(func(f *os.File, err error) {
		mu.Lock()
		defer mu.Unlock()
		closes[f]++
		if err != nil {
			t.Errorf("owner Close: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Read(make([]byte, 1))
		done <- err
	}()
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := r.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
	wg.Wait()
	if err := <-done; !errors.Is(err, os.ErrClosed) {
		t.Fatalf("blocked Read after Close: %v", err)
	}
	for range 2 {
		if err := w.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
	// Check the actual os.Files, not state kept only by the wrapper.
	for _, f := range []*os.File{r.File, w.File} {
		if _, err := f.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Errorf("Read after Close: %v", err)
		}
		if closes[f] != 1 {
			t.Errorf("File.Close called %d times", closes[f])
		}
	}
}
