package driver_test

import (
	"reflect"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestProfileContractsExcludeRemovedFields(t *testing.T) {
	tests := []struct {
		name      string
		typeOf    reflect.Type
		forbidden []string
	}{
		{name: "driver.AgentSpec", typeOf: reflect.TypeOf(driver.AgentSpec{}), forbidden: []string{"Content"}},
		{name: "driver.HookSpec", typeOf: reflect.TypeOf(driver.HookSpec{}), forbidden: []string{"Matcher", "Command", "Args", "Env"}},
		{name: "driver.ProfileConfigPatch", typeOf: reflect.TypeOf(driver.ProfileConfigPatch{}), forbidden: []string{"FileKind", "Path", "Section"}},
		{name: "driver.CloneProfileOptions", typeOf: reflect.TypeOf(driver.CloneProfileOptions{}), forbidden: []string{"IncludeAuth"}},
	}
	for _, test := range tests {
		for _, field := range test.forbidden {
			if _, ok := test.typeOf.FieldByName(field); ok {
				t.Errorf("%s still exposes removed field %s", test.name, field)
			}
		}
	}
}
