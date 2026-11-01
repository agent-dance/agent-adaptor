package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestFakeScenario executes the same fifteen-step host scenario as live mode.
// Contract failures remain failures; only the known loopback sandbox denial
// skips the two Team subtests, without marking them passed in the JSON report.
func TestFakeScenario(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := runScenario(ctx, settings{agent: "fake", effort: "low", timeout: 2 * time.Second})
	if len(r.Steps) != 15 {
		t.Fatalf("steps=%d; want 15", len(r.Steps))
	}
	for i, s := range r.Steps {
		t.Run(fmt.Sprintf("step_%02d", s.Number), func(t *testing.T) {
			if s.Number != i+1 {
				t.Fatalf("step order: got %d at index %d", s.Number, i)
			}
			if s.Started.IsZero() || s.Ended.Before(s.Started) || s.Section == "" || len(s.Checks) == 0 {
				t.Fatalf("incomplete report: %+v", s)
			}
			if s.EnvironmentError != "" {
				if s.Number < 14 {
					t.Fatal("unexpected environment exclusion:", s.EnvironmentError)
				}
				t.Skip("team.New requires loopback denied in this sandbox: " + s.EnvironmentError)
			}
			for _, c := range s.Checks {
				if !c.Passed {
					t.Errorf("%s: expected %v; observed %v", c.Name, c.Expected, c.Observed)
				}
			}
			if !s.Passed {
				t.Error("step marked failed")
			}
		})
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var decoded report
	if err = json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Steps) != 15 || decoded.Ended.Before(decoded.Started) {
		t.Fatal("report round trip lost required fields")
	}
	for _, s := range decoded.Steps {
		for _, call := range s.Calls {
			if call.At.IsZero() || call.Method == "" || call.ErrorClass == "" {
				t.Fatal("incomplete host call", call)
			}
		}
	}
}

func TestReportRetainsFailure(t *testing.T) {
	s := &step{Started: time.Now()}
	s.equal("contract", true, false)
	s.finish()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Passed || !strings.Contains(string(b), `"observed":false`) {
		t.Fatal("failed observation was hidden", string(b))
	}
}

// TestCodexConstruction checks the optional-interface forwarding used by live
// tool-count auditing. Construction and Close perform no provider inference.
func TestCodexConstruction(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	h := &scenario{cfg: settings{agent: "codex", effort: "low"}, report: &report{}}
	s := h.step(1, "construction", "§6.2")
	f, err := h.newFixture(s)
	if err != nil {
		t.Fatal(err)
	}
	defer f.cleanup(s)
	if f.newAgent("leader", false, s) == nil {
		t.Fatal("nil leader")
	}
}
