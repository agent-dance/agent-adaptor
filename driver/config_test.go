package driver_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-dance/agent-adaptor/driver"
)

func TestCommonConfigPublicTypeGraphDoesNotExposeInternalPackages(t *testing.T) {
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if typ == nil || seen[typ] {
			return
		}
		seen[typ] = true
		if strings.Contains(typ.PkgPath(), "/internal/") {
			t.Fatalf("driver.CommonConfig exposes internal type %v from %q", typ, typ.PkgPath())
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Map:
			visit(typ.Key())
			visit(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				visit(typ.Field(i).Type)
			}
		}
	}

	visit(reflect.TypeOf(driver.CommonConfig{}))
}

func TestCommonConfigCloneRecursivelyCopiesNativeValues(t *testing.T) {
	type nativeEnvelope struct {
		Values []string
	}
	typedMap := map[string][]map[string]any{
		"items": {{"value": "original"}},
	}
	envelope := &nativeEnvelope{Values: []string{"original"}}
	original := driver.CommonConfig{Instructions: &driver.InstructionsBundleRef{Native: map[string]any{
		"typed_map": typedMap,
		"envelope":  envelope,
	}}}

	cloned := original.Clone()
	typedMap["items"][0]["value"] = "mutated"
	envelope.Values[0] = "mutated"

	gotMap := cloned.Instructions.Native["typed_map"].(map[string][]map[string]any)
	if got := gotMap["items"][0]["value"]; got != "original" {
		t.Fatalf("nested typed map value = %v, want original", got)
	}
	gotEnvelope := cloned.Instructions.Native["envelope"].(*nativeEnvelope)
	if got := gotEnvelope.Values[0]; got != "original" {
		t.Fatalf("nested pointer/struct/slice value = %q, want original", got)
	}
}
