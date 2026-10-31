package adaptor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
	"github.com/agent-dance/agent-adaptor/internal/testutil"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/opencode"
	"github.com/agent-dance/agent-adaptor/pi"
)

func TestProtocolProcess(t *testing.T) { testutil.ProtocolProcess(t) }

func TestNewProviderResultAndThreadContracts(t *testing.T) {
	for _, name := range []string{"pi", "opencode"} {
		t.Run(name, func(t *testing.T) {
			fixture := "happy.jsonl"
			if name == "opencode" {
				fixture = "server.jsonl"
			}
			raw, err := os.ReadFile(filepath.Join(name, "testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			command, capture := testutil.ProtocolCommand(t, string(raw), "audit stderr", 0)
			var d driver.Driver
			if name == "pi" {
				d = pi.Driver(pi.Config{Command: command, Env: []driver.EnvBinding{{Name: "PI_CODING_AGENT_DIR", Value: t.TempDir()}}})
			} else {
				d = opencode.Driver(opencode.Config{Command: command, Model: "test/model", Env: []driver.EnvBinding{{Name: "OPENCODE_CONFIG_DIR", Value: t.TempDir()}}})
			}
			a := adaptor.New(d, adaptor.WithThreadStore(memory.NewStore()))
			defer a.Close(context.Background())
			ctx := context.Background()
			run, err := a.Run(ctx, "hello")
			if err != nil {
				t.Fatal(err)
			}
			s := a.Stream(ctx, "hello")
			if s.RunID() == "" {
				t.Fatal("missing RunID")
			}
			for range s.Events() {
			}
			stream, err := s.Result()
			if err != nil {
				t.Fatal(err)
			}
			if run.Text != stream.Text || run.Summary != stream.Summary || run.Model != stream.Model || run.Provider != stream.Provider || !reflect.DeepEqual(run.Usage, stream.Usage) || !reflect.DeepEqual(run.Raw().Terminal, stream.Raw().Terminal) || run.Raw().Stderr != stream.Raw().Stderr || !reflect.DeepEqual(run.Transcript(), stream.Transcript()) || !reflect.DeepEqual(run.Services(), stream.Services()) {
				t.Fatal("Run and Stream.Result disagree")
			}
			if name == "pi" && run.Raw().Stdout != stream.Raw().Stdout {
				t.Fatal("raw stdout differs")
			}
			if run.Raw().Stdout == "" || stream.Raw().Stdout == "" {
				t.Fatal("process audit output lost")
			}
			th := a.Thread("key/with:opaque\x00parts")
			if _, err := th.Run(ctx, "first"); err != nil {
				t.Fatal(err)
			}
			before, err := th.Checkpoint(ctx)
			if err != nil || before == nil {
				t.Fatalf("checkpoint=%+v err=%v", before, err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(capture), "stdout"), append([]byte("{malformed\n"), raw...), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = th.Run(ctx, "second")
			var runError *adaptor.RunError
			if !errors.As(err, &runError) || runError.Result == nil || (runError.Result.Raw().Stdout == "" && runError.Result.Metadata["opencode.event_stream"] == "") {
				t.Fatalf("err=%v", err)
			}
			after, err := th.Checkpoint(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("healthy checkpoint overwritten: before=%+v after=%+v err=%v", before, after, err)
			}
			if err := a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := th.Run(ctx, "closed"); !errors.Is(err, adaptor.ErrAgentClosed) {
				t.Fatalf("after Close: %v", err)
			}
		})
	}
}
