package agentadaptor

import "testing"

func TestMergeRunPolicy(t *testing.T) {
	base := &RunPolicy{Approvals: ApprovalAsk, Isolation: IsolationReadOnly, WebSearch: FeatureDeny}
	t.Run("nil override", func(t *testing.T) {
		got := mergeRunPolicy(base, nil)
		if got != *base {
			t.Fatalf("got %#v want %#v", got, *base)
		}
	})
	t.Run("partial override", func(t *testing.T) {
		got := mergeRunPolicy(base, &RunPolicy{Approvals: ApprovalOff})
		if got.Approvals != ApprovalOff || got.Isolation != IsolationReadOnly || got.WebSearch != FeatureDeny {
			t.Fatalf("got %#v", got)
		}
	})
	t.Run("nil base", func(t *testing.T) {
		got := mergeRunPolicy(nil, &RunPolicy{Isolation: IsolationUnrestricted})
		if got.Isolation != IsolationUnrestricted || got.Approvals != ApprovalInherit {
			t.Fatalf("got %#v", got)
		}
	})
}
