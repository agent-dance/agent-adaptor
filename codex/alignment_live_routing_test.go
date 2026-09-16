package codex

import (
	"reflect"
	"slices"
	"testing"

	"github.com/agent-dance/agent-adaptor/codex/internal/livetest"
	"github.com/agent-dance/agent-adaptor/driver"
)

func TestAlignmentLiveRoutingTransportsAndFingerprint(t *testing.T) {
	args, err := livetest.RouteArgs([]byte(`{"version":1,"model_provider":"route.fixture","name":"Fixture","base_url":"https://route.invalid/v1","wire_api":"responses","requires_openai_auth":true}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{CommonConfig: CommonConfig{ExtraArgs: args}, Model: "gpt-5.4"}
	if err := validateCodexPromptArgs(args); err != nil {
		t.Fatal("route conflicts with native instruction guard")
	}
	execArgs, err := codexExecArgs(driver.Request{}, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{args[1], args[3]} {
		if !slices.Contains(execArgs, value) {
			t.Fatal("exec dropped route")
		}
	}
	rpcArgs, err := codexAppServerExtraArgs(args, driver.RunPolicy{})
	if err != nil || !reflect.DeepEqual(rpcArgs, args) {
		t.Fatal("app-server dropped route")
	}
	if err := validateCodexPromptArgs(append(slices.Clone(args), "-c", `developer_instructions="forbidden"`)); err == nil {
		t.Fatal("route bypassed instruction guard")
	}
	withPolicy := append(slices.Clone(args), "-c", `approval_policy="never"`)
	if got := filterCodexPolicyExtraArgs(withPolicy, driver.RunPolicy{}); !reflect.DeepEqual(got, args) {
		t.Fatal("route bypassed policy filtering")
	}
	bound := Driver(cfg)
	before := codexSessionFingerprint(t, bound)
	cfg.ExtraArgs[3] = `model_providers={"route.fixture"={name="Fixture",base_url="https://other.invalid/v1",wire_api="responses",requires_openai_auth=true}}`
	if codexSessionFingerprint(t, bound) != before || codexSessionFingerprint(t, Driver(cfg)) == before {
		t.Fatal("route absent from immutable session config")
	}
	a := persistentSpec{extraArgs: rpcArgs}
	b := persistentSpec{extraArgs: cfg.ExtraArgs}
	if a.sig() == b.sig() {
		t.Fatal("changed route reused a persistent writer")
	}
	if !reflect.DeepEqual(a.openOptions().ExtraArgs, rpcArgs) {
		t.Fatal("persistent launch dropped route")
	}
}
