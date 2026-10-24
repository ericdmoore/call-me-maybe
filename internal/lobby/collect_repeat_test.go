package lobby

import "testing"

// Where * means "say that again" — the arithmetic challenge — it ends the
// entry and takes nothing typed with it. Elsewhere it still clears.
func TestCollectStarRepeatsWhenAsked(t *testing.T) {
	if got, reason := collectFrom(t, collectSpec{StarRepeats: true}, "4*"); got != "" || reason != endRepeat {
		t.Fatalf("got %q/%s, want nothing and %s", got, reason, endRepeat)
	}
	if got, reason := collectFrom(t, collectSpec{StarRepeats: true, Seed: "*"}, ""); got != "" || reason != endRepeat {
		t.Fatalf("seeded *: got %q/%s, want %s", got, reason, endRepeat)
	}
	if got, reason := collectFrom(t, collectSpec{}, "4*18#"); got != "18" || reason != endHash {
		t.Fatalf("without StarRepeats: got %q/%s, want * to clear", got, reason)
	}
}
