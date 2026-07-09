package core

import "testing"

func TestRaceCountsComplete(t *testing.T) {
	cases := []struct {
		name              string
		present, total    int
		allowUnknownTotal bool
		want              bool
	}{
		{"unknown total, unknown not allowed", 0, 0, false, false},
		{"unknown total, unknown allowed", 0, 0, true, true},
		{"present with unknown total, unknown allowed", 3, 0, true, true},
		{"known total, present short", 1, 2, false, false},
		{"known total, present short, unknown allowed has no effect", 1, 2, true, false},
		{"known total, present reached", 2, 2, false, true},
		{"known total, present exceeds", 3, 2, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := raceCountsComplete(c.present, c.total, c.allowUnknownTotal); got != c.want {
				t.Fatalf("raceCountsComplete(%d, %d, %v) = %v, want %v", c.present, c.total, c.allowUnknownTotal, got, c.want)
			}
		})
	}
}

func TestReleaseStatusCompleteRequiresKnownTotal(t *testing.T) {
	if releaseStatusComplete(ReleaseStatus{Present: 0, Total: 0}) {
		t.Fatalf("expected zero-total status to be incomplete, not complete")
	}
	if !releaseStatusComplete(ReleaseStatus{Present: 2, Total: 2}) {
		t.Fatalf("expected present==total status to be complete")
	}
	if releaseStatusComplete(ReleaseStatus{Present: 1, Total: 2}) {
		t.Fatalf("expected present<total status to be incomplete")
	}
}
