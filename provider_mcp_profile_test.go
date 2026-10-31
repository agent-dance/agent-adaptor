package adaptor_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/mcp"
)

func mcpRow(t *testing.T, s adaptor.ProfileSnapshot) adaptor.ResourceSnapshot {
	t.Helper()
	for _, r := range s.Resources {
		if r.Kind == adaptor.ProfileResourceMCP {
			return r
		}
	}
	t.Fatal("MCP row absent")
	return adaptor.ResourceSnapshot{}
}

func TestProviderDurableMCP(t *testing.T) {
	for _, provider := range []string{"pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			dir := filepath.Join(t.TempDir(), "profile")
			spec := mcp.Server{Key: "proof", Transport: mcp.TransportHTTP, URL: "https://example.test/mcp", BearerTokenEnvVar: "MCP_PROFILE_SECRET", Required: true}
			a := adaptor.New(profileTestDriver(provider, dir), adaptor.WithMCP(spec))
			defer a.Close(ctx)
			before, err := a.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(mcpRow(t, before).Managed) != 0 {
				t.Fatal("desired MCP reported installed")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("ProfileState initialized profile")
			}
			after, err := a.SyncProfile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if row := mcpRow(t, after); !reflect.DeepEqual(row.Managed, []string{"proof"}) || len(row.Warnings) > 0 {
				t.Fatalf("sync=%+v", row)
			}
			filename := "agent-adaptor-mcp.json"
			if provider == "opencode" {
				filename = "opencode.jsonc"
			}
			path := filepath.Join(dir, filename)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "MCP_PROFILE_SECRET") {
				t.Fatal("bearer reference missing")
			}
			fresh := adaptor.New(profileTestDriver(provider, dir))
			defer fresh.Close(ctx)
			observed, err := fresh.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(mcpRow(t, observed).Managed, []string{"proof"}) {
				t.Fatal("fresh Agent lost installed MCP")
			}
			if _, err := fresh.SyncProfile(ctx); err != nil {
				t.Fatal(err)
			}
			unchanged, _ := os.ReadFile(path)
			if string(unchanged) != string(raw) {
				t.Fatal("undeclared sync changed MCP")
			}
			changed := strings.ReplaceAll(string(raw), "example.test", "externally-edited.test")
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			observed, err = fresh.ProfileState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if row := mcpRow(t, observed); len(row.Managed) != 0 || !reflect.DeepEqual(row.External, []string{"proof"}) {
				t.Fatalf("external edit=%+v", row)
			}
			if _, err := a.SyncProfile(ctx); err == nil {
				t.Fatal("external MCP edit overwritten")
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			clear := adaptor.New(profileTestDriver(provider, dir), adaptor.WithMCP())
			defer clear.Close(ctx)
			cleared, err := clear.SyncProfile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(mcpRow(t, cleared).Managed) != 0 {
				t.Fatal("explicit MCP clear not honored")
			}
		})
	}
}
