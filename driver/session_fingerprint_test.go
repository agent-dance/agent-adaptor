package driver

import (
	"errors"
	"strings"
	"testing"
	"unsafe"
)

func TestCanonicalSessionConfigFingerprintDeterministic(t *testing.T) {
	t.Parallel()
	type nested struct {
		Values map[string]any
		Items  []string
	}
	left := nested{
		Values: map[string]any{"second": []int{2, 3}, "first": "one"},
		Items:  []string{},
	}
	rightValues := make(map[string]any)
	rightValues["first"] = "one"
	rightValues["second"] = []int{2, 3}
	right := nested{Values: rightValues, Items: nil}

	leftFingerprint, err := CanonicalSessionConfigFingerprint("test-driver/v1;codec/v1", left)
	if err != nil {
		t.Fatal(err)
	}
	rightFingerprint, err := CanonicalSessionConfigFingerprint("test-driver/v1;codec/v1", right)
	if err != nil {
		t.Fatal(err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("map insertion order and nil/empty slices must normalize: %q != %q", leftFingerprint, rightFingerprint)
	}
	emptyMap := nested{Values: map[string]any{}, Items: []string{"item"}}
	nilMap := nested{Values: nil, Items: []string{"item"}}
	if got, want := mustSessionFingerprint(t, "test-driver/v1;codec/v1", emptyMap), mustSessionFingerprint(t, "test-driver/v1;codec/v1", nilMap); got != want {
		t.Fatalf("nil and empty maps must normalize: %q != %q", got, want)
	}
	if !strings.HasPrefix(leftFingerprint, "sha256:") || len(leftFingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected opaque fingerprint shape %q", leftFingerprint)
	}
}

func TestCanonicalSessionConfigFingerprintDistinguishesDomainAndValues(t *testing.T) {
	t.Parallel()
	type config struct {
		Enabled bool
		Secret  string
		Values  map[string]string
	}
	base := config{Enabled: true, Secret: "secret-one", Values: map[string]string{"key": "value"}}

	fingerprint := mustSessionFingerprint(t, "test-driver/v1;codec/v1", base)
	cases := []struct {
		name   string
		domain string
		value  config
	}{
		{name: "codec domain", domain: "test-driver/v1;codec/v2", value: base},
		{name: "boolean", domain: "test-driver/v1;codec/v1", value: config{Secret: base.Secret, Values: base.Values}},
		{name: "secret", domain: "test-driver/v1;codec/v1", value: config{Enabled: true, Secret: "secret-two", Values: base.Values}},
		{name: "map value", domain: "test-driver/v1;codec/v1", value: config{Enabled: true, Secret: base.Secret, Values: map[string]string{"key": "changed"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustSessionFingerprint(t, tc.domain, tc.value); got == fingerprint {
				t.Fatalf("fingerprint did not change for %s", tc.name)
			}
		})
	}
}

func TestCanonicalSessionConfigFingerprintDereferencesPointers(t *testing.T) {
	t.Parallel()
	type config struct{ Value *string }
	leftValue, rightValue := "same", "same"
	left := mustSessionFingerprint(t, "test/v1", config{Value: &leftValue})
	right := mustSessionFingerprint(t, "test/v1", config{Value: &rightValue})
	if left != right {
		t.Fatal("pointer addresses must not participate in a cross-process fingerprint")
	}
	if nilValue := mustSessionFingerprint(t, "test/v1", config{}); nilValue == left {
		t.Fatal("nil pointer must remain distinct from a present value")
	}
}

func TestCanonicalSessionConfigFingerprintRejectsUnstableValuesWithoutLeak(t *testing.T) {
	t.Parallel()
	const secret = "do-not-leak-this-secret"
	type config struct {
		Secret string
		Native map[string]any
	}
	unstable := []any{func() {}, make(chan struct{}), unsafe.Pointer(new(byte)), uintptr(123)}
	for _, value := range unstable {
		_, err := CanonicalSessionConfigFingerprint("test/v1", config{
			Secret: secret,
			Native: map[string]any{secret: value},
		})
		if err == nil {
			t.Fatalf("expected %T config to be rejected", value)
		}
		var fingerprintErr *SessionConfigFingerprintError
		if !errors.As(err, &fingerprintErr) {
			t.Fatalf("expected SessionConfigFingerprintError, got %T", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("fingerprint error leaked a secret: %q", err)
		}
		if !strings.Contains(fingerprintErr.Path, "[value]") {
			t.Fatalf("error should identify generic map value path, got %q", fingerprintErr.Path)
		}
	}
}

func TestCanonicalSessionConfigFingerprintRejectsCyclesAndUnexportedState(t *testing.T) {
	t.Parallel()
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := CanonicalSessionConfigFingerprint("test/v1", cycle); err == nil {
		t.Fatal("expected cyclic config to be rejected")
	}

	type privateConfig struct{ hidden string }
	if _, err := CanonicalSessionConfigFingerprint("test/v1", privateConfig{hidden: "secret"}); err == nil {
		t.Fatal("expected unexported struct state to be rejected")
	}
}

func TestCanonicalSessionConfigFingerprintRequiresDomain(t *testing.T) {
	t.Parallel()
	if _, err := CanonicalSessionConfigFingerprint(" ", struct{}{}); err == nil {
		t.Fatal("expected empty version domain to be rejected")
	}
}

func mustSessionFingerprint(t *testing.T, domain string, value any) string {
	t.Helper()
	fingerprint, err := CanonicalSessionConfigFingerprint(domain, value)
	if err != nil {
		t.Fatal(err)
	}
	return fingerprint
}
