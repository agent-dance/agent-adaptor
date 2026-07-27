package keycodec

import "testing"

func TestEncodeIsInjectiveAcrossBoundariesAndBytes(t *testing.T) {
	cases := [][]string{
		nil,
		{""},
		{"", ""},
		{"a"},
		{"a", "b"},
		{"a:b"},
		{"a", ":b"},
		{"a:", "b"},
		{"\x00"},
		{"", "\x00"},
		{"\x00", ""},
		{"你好", "世界"},
		{"你好世界"},
		{"\U0001f680", "e\u0301"},
		{"\U0001f680e\u0301"},
	}
	seen := make(map[string][]string, len(cases))
	for _, parts := range cases {
		encoded := Encode(parts...)
		if previous, ok := seen[encoded]; ok {
			t.Fatalf("Encode(%q) collided with Encode(%q): %q", parts, previous, encoded)
		}
		seen[encoded] = parts
	}
}

func TestEncodeIsDeterministicAndURLSafe(t *testing.T) {
	first := Encode("thread-key", "tenant\x00一", "issue/1")
	second := Encode("thread-key", "tenant\x00一", "issue/1")
	if first != second {
		t.Fatalf("Encode is not deterministic: %q != %q", first, second)
	}
	for _, forbidden := range []byte{'+', '/', '='} {
		for _, got := range []byte(first) {
			if got == forbidden {
				t.Fatalf("Encode returned non-URL-safe byte %q in %q", forbidden, first)
			}
		}
	}
}
