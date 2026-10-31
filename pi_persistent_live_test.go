package adaptor_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/memory"
	"github.com/agent-dance/agent-adaptor/pi"
	"github.com/agent-dance/agent-adaptor/tool"
)

func TestLivePiPersistent(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_PI_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_PI_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	var calls atomic.Int32
	var approvals atomic.Int32
	type output struct {
		Word string `json:"word"`
	}
	definition := tool.Define("next_marker", "Obtain a fresh marker; always invoke it when asked for the next marker.", func(context.Context, struct{}) (output, error) {
		return output{Word: fmt.Sprintf("FRESH_%d", calls.Add(1))}, nil
	}, tool.ReadOnly(), tool.Revision("marker/v1"))
	a := adaptor.New(pi.Driver(pi.Config{Command: os.Getenv("AGENT_ADAPTOR_PI_COMMAND"), Model: model, Provider: os.Getenv("AGENT_ADAPTOR_PI_PROVIDER"), Thinking: "low", NoExtensions: true, NoSkills: true}), adaptor.WithWorkspace(t.TempDir()), adaptor.WithThreadStore(memory.NewStore()), adaptor.WithInstructions("When reporting a marker, use only the fresh marker returned by next_marker."), adaptor.WithTools(definition), adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAsk}}), adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		approvals.Add(1)
		return req.Approve(ctx)
	}))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	thread := a.Thread("resident-with-tools")
	first, err := thread.Run(ctx, "Call next_marker and report the fresh marker.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := thread.Run(ctx, "Call next_marker again. Return the fresh marker as the word field.", adaptor.WithSchema[output]())
	if err != nil {
		t.Fatal(err)
	}
	var decoded output
	if err := second.Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text, "FRESH_1") || decoded.Word != "FRESH_2" || calls.Load() != 2 || approvals.Load() != 2 {
		t.Fatalf("first=%q second=%+v actual tool calls=%d", first.Text, decoded, calls.Load())
	}
	pid := first.Metadata["pi.rpc_pid"]
	if pid == "" || second.Metadata["pi.rpc_pid"] != pid || second.Metadata["pi.rpc_reused"] != "true" {
		t.Fatalf("native server was not reused: %v -> %v", first.Metadata["pi.rpc_pid"], second.Metadata["pi.rpc_pid"])
	}
	third, err := thread.Run(ctx, "What was the last fresh marker? Reply only with that marker without calling tools.", adaptor.WithSpawn())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third.Text, "FRESH_2") || third.Metadata["pi.rpc_pid"] == pid || third.Metadata["pi.rpc_reused"] != "false" {
		t.Fatal("WithSpawn did not retain conversation in a fresh process")
	}
	t.Logf("reused RPC process %s across real hosted-tool calls and validated schema; WithSpawn resumed on %s", pid, third.Metadata["pi.rpc_pid"])
}

func TestLivePiRPCDialogs(t *testing.T) {
	if os.Getenv("AGENT_ADAPTOR_LIVE_CONFORMANCE") != "1" || os.Getenv("AGENT_ADAPTOR_PI_LIVE") != "1" {
		t.Skip("both live gates are required")
	}
	model := os.Getenv("AGENT_ADAPTOR_PI_MODEL")
	if model == "" {
		t.Fatal("an explicit live model is required")
	}
	workspace := t.TempDir()
	extension := filepath.Join(workspace, "dialogs.mjs")
	source := `export default function(pi) {
 pi.registerTool({name:"sdk_ui_probe",label:"Test native dialogs",description:"Ask four native dialogs and return their responses.",parameters:{type:"object",properties:{},additionalProperties:false},
 async execute(id,args,signal,onUpdate,ctx) {
  const pick = await ctx.ui.select("Pick one",["A","B"]);
  const confirmed = await ctx.ui.confirm("Confirm","Proceed?");
  const input = await ctx.ui.input("Secret word");
  const edit = await ctx.ui.editor("Edit word","original");
  return {content:[{type:"text",text:JSON.stringify({pick,confirmed,input,edit})}],details:{pick,confirmed,input,edit}};
 }});
}`
	if err := os.WriteFile(extension, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	a := adaptor.New(pi.Driver(pi.Config{Command: os.Getenv("AGENT_ADAPTOR_PI_COMMAND"), Provider: os.Getenv("AGENT_ADAPTOR_PI_PROVIDER"), Model: model, Thinking: "low", NoExtensions: true, NoSkills: true, Extensions: []string{extension}}), adaptor.WithWorkspace(workspace), adaptor.WithThreadStore(memory.NewStore()))
	defer a.Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, runner := range []adaptor.Runner{a, a.Thread("native-dialogs")} {
		answers := map[string]string{"select": "B", "confirm": "yes", "input": "NATIVE_72", "editor": "EDIT_91"}
		seen := map[string]int{}
		s := runner.Stream(ctx, "Call sdk_ui_probe exactly once and report all four returned values.", adaptor.WithPolicy(adaptor.Policy{Approvals: adaptor.ApprovalPolicy{Permission: adaptor.ApprovalAutoApprove, Question: adaptor.QuestionAsk}}))
		for event := range s.Events() {
			if q, ok := event.(*adaptor.ApprovalRequest); ok {
				method := strings.TrimPrefix(q.Source, "pi.extension_ui.")
				answer, ok := answers[method]
				if !ok {
					t.Fatalf("unexpected question %s", q.Source)
				}
				seen[method]++
				if err := q.Answer(ctx, answer); err != nil {
					t.Fatal(err)
				}
			}
		}
		r, err := s.Result()
		if err != nil {
			t.Fatal(err)
		}
		for method := range answers {
			if seen[method] != 1 {
				t.Fatalf("native dialog calls=%v", seen)
			}
		}
		if !strings.Contains(r.Text, "NATIVE_72") || !strings.Contains(r.Text, "EDIT_91") {
			t.Fatalf("native responses lost: %q", r.Text)
		}
	}
}
