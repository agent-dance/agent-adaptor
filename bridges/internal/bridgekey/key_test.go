package bridgekey_test

import (
	"testing"

	"github.com/agent-dance/agent-adaptor/bridges/internal/bridgekey"
)

func TestEncodeIsCollisionFreeAcrossTupleBoundaries(t *testing.T) {
	pairs := [][2][]string{
		{{"a/b", "c"}, {"a", "b/c"}},
		{{"ab", "c"}, {"a", "bc"}},
		{{"", "a"}, {"a", ""}},
		{{"agui", "x"}, {"a", "gui/x"}},
	}
	for _, pair := range pairs {
		if got, other := bridgekey.Encode(pair[0]...), bridgekey.Encode(pair[1]...); got == other {
			t.Fatalf("Encode(%q) collided with Encode(%q): %q", pair[0], pair[1], got)
		}
	}
	if got, want := bridgekey.Encode("agui", "thread"), bridgekey.Encode("agui", "thread"); got != want {
		t.Fatalf("encoding is not deterministic: %q != %q", got, want)
	}
}
