package adaptor_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/driver"
)

// TestInvocationCoordinatorOwnsDriverDispatchAndPersistence is a source-level
// architecture gate. It walks production Go syntax (never tests), permits the
// public generic Runner helper's final r.Run call, and rejects every other
// execution or persistence call outside invocation.go.
func TestInvocationCoordinatorOwnsDriverDispatchAndPersistence(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var driverRuns, persists, structuredFinalizers, resultFinalizers []token.Position
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			position := fset.Position(call.Pos())
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "finalizeRun" {
				resultFinalizers = append(resultFinalizers, position)
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "Run":
				// RunAs is itself public API and must invoke its Runner. Every
				// other production .Run call is a potential second dispatch.
				if filepath.Base(position.Filename) == "structured.go" && receiverIdent(sel.X) == "r" {
					return true
				}
				driverRuns = append(driverRuns, position)
			case "Persist":
				persists = append(persists, position)
			case "FinalizeStructuredOutput":
				structuredFinalizers = append(structuredFinalizers, position)
			}
			return true
		})
	}
	if len(driverRuns) != 1 || filepath.Base(driverRuns[0].Filename) != "invocation.go" {
		t.Fatalf("production Driver.Run call sites = %v, want exactly invocation.go", driverRuns)
	}
	if len(persists) != 1 || filepath.Base(persists[0].Filename) != "invocation.go" {
		t.Fatalf("production thread Persist call sites = %v, want exactly invocation.go", persists)
	}
	if len(structuredFinalizers) != 1 || filepath.Base(structuredFinalizers[0].Filename) != "invocation.go" {
		t.Fatalf("structured finalization call sites = %v, want exactly invocation.go", structuredFinalizers)
	}
	if len(resultFinalizers) != 1 || filepath.Base(resultFinalizers[0].Filename) != "invocation.go" {
		t.Fatalf("Result finalization call sites = %v, want exactly invocation.go", resultFinalizers)
	}
}

func receiverIdent(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// TestPublicSkillBoundaryDoesNotExposeInternalEngine locks the public skill
// contracts to skill.Provider/Materializer and inspects every exported
// skill-related signature for direct internal-engine selectors.
func TestPublicSkillBoundaryDoesNotExposeInternalEngine(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "options.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	wantAliases := map[string]string{
		"SkillProvider":     "skill.Provider",
		"SkillMaterializer": "skill.Materializer",
	}
	for _, decl := range file.Decls {
		switch decl := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if ast.IsExported(typeSpec.Name.Name) && strings.Contains(typeSpec.Name.Name, "Skill") {
					assertNoEngineSelector(t, fset, typeSpec.Type)
				}
				if want, tracked := wantAliases[typeSpec.Name.Name]; tracked {
					if got := selectorName(typeSpec.Type); got != want {
						t.Errorf("%s aliases %s, want %s", typeSpec.Name.Name, got, want)
					}
					delete(wantAliases, typeSpec.Name.Name)
				}
			}
		case *ast.FuncDecl:
			if !ast.IsExported(decl.Name.Name) || !strings.Contains(decl.Name.Name, "Skill") {
				continue
			}
			assertNoEngineSelector(t, fset, decl.Type)
		}
	}
	if len(wantAliases) != 0 {
		t.Fatalf("missing public skill aliases: %v", wantAliases)
	}
}

func selectorName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + selector.Sel.Name
}

func assertNoEngineSelector(t *testing.T, fset *token.FileSet, node ast.Node) {
	t.Helper()
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := selector.X.(*ast.Ident); ok && pkg.Name == "engine" {
			t.Errorf("exported skill signature leaks internal engine at %s", fset.Position(selector.Pos()))
		}
		return true
	})
}

func TestRunAndStreamHaveFullResultParity(t *testing.T) {
	terminal := &driver.TerminalPayload{Event: "result", JSON: []byte(`{"type":"result","ok":true}`)}
	fake := newFakeDriver()
	fake.streamCaps = driver.StreamCapability{Native: true, TokenLevel: true}
	fake.descriptor = &driver.Descriptor{
		Type: "parity-fake",
		StructuredOutput: driver.StructuredOutputCapability{
			JSONSchemaNative:   true,
			WorksWithRun:       true,
			WorksWithStreaming: true,
		},
	}
	fake.response = driver.Response{
		Output:     `{"answer":42}`,
		Summary:    "answer generated",
		Provider:   "fake-provider",
		Model:      "fake-model",
		Usage:      &driver.Usage{InputTokens: 3, OutputTokens: 4, CachedInputTokens: 2},
		RawStreams: &driver.RawStreams{Stdout: "raw out", Stderr: "raw err", Terminal: terminal},
		Transcript: []driver.TranscriptItem{{Kind: driver.TranscriptAssistant, Text: "semantic", Metadata: map[string]string{"source": "official"}}},
		Metadata:   map[string]string{"trace": "abc"},
		StructuredOutput: &driver.StructuredOutput{
			Format: driver.OutputFormatJSONSchema, Source: driver.StructuredOutputSourceNative,
			RawJSON: []byte(`{"answer":42}`), Valid: true,
		},
		RuntimeServices: []driver.RuntimeServiceReport{{ID: "svc-1", Name: "worker", Metadata: map[string]string{"observed": "yes"}}},
	}
	agent := adaptor.New(fake)
	option := adaptor.WithSchemaJSON([]byte(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`))

	runResult, err := agent.Run(context.Background(), "answer", option)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	stream := agent.Stream(context.Background(), "answer", option)
	for range stream.Events() {
	}
	streamResult, err := stream.Result()
	if err != nil {
		t.Fatalf("Stream.Result: %v", err)
	}

	if runResult.Text != streamResult.Text || runResult.Summary != streamResult.Summary ||
		runResult.Model != streamResult.Model || runResult.Provider != streamResult.Provider ||
		!reflect.DeepEqual(runResult.Usage, streamResult.Usage) ||
		!reflect.DeepEqual(runResult.Metadata, streamResult.Metadata) ||
		!reflect.DeepEqual(runResult.Raw(), streamResult.Raw()) ||
		!reflect.DeepEqual(runResult.Transcript(), streamResult.Transcript()) ||
		!reflect.DeepEqual(runResult.Services(), streamResult.Services()) {
		t.Fatalf("Run/Stream Result layers diverged:\nrun=%+v\nstream=%+v", runResult, streamResult)
	}
	var runDecoded, streamDecoded map[string]int
	if err := runResult.Decode(&runDecoded); err != nil {
		t.Fatalf("Run Decode: %v", err)
	}
	if err := streamResult.Decode(&streamDecoded); err != nil {
		t.Fatalf("Stream Decode: %v", err)
	}
	if !reflect.DeepEqual(runDecoded, streamDecoded) {
		t.Fatalf("Run/Stream structured output diverged: %v != %v", runDecoded, streamDecoded)
	}
	if fake.request(t, 0).Streaming != fake.request(t, 1).Streaming {
		t.Fatal("consumer Run and Stream selected different provider transports")
	}
}
