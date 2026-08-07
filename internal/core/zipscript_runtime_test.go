package core

import "testing"

func TestShouldRenderCWDRaceBannerRequiresRaceData(t *testing.T) {
	cfg := &Config{ShowCWDBanner: true}
	if shouldRenderCWDRaceBanner(cfg, nil, nil, 0, 0, 0) {
		t.Fatal("empty race data should not render a CWD race banner")
	}
	if !shouldRenderCWDRaceBanner(cfg, []VFSRaceUser{{Name: "racer"}}, nil, 100, 1, 1) {
		t.Fatal("actual race data should render when the CWD banner is enabled")
	}

	cfg.ShowCWDBanner = false
	if shouldRenderCWDRaceBanner(cfg, []VFSRaceUser{{Name: "racer"}}, nil, 100, 1, 1) {
		t.Fatal("disabled CWD banner should not render race data")
	}
}

func TestRaceCountsComplete(t *testing.T) {
	cases := []struct {
		name           string
		present, total int
		want           bool
	}{
		{"unknown total, no files present", 0, 0, false},
		// A zip race's DIZ-derived total can be unknown mid-race (the DIZ
		// lives inside whichever payload .zip happens to carry it, which
		// isn't necessarily the first one to land) even with other volumes
		// already present -- that must stay incomplete, not flip true just
		// because the total hasn't been read yet.
		{"unknown total, files already present", 3, 0, false},
		{"known total, present short", 1, 2, false},
		{"known total, present reached", 2, 2, true},
		{"known total, present exceeds", 3, 2, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := raceCountsComplete(c.present, c.total); got != c.want {
				t.Fatalf("raceCountsComplete(%d, %d) = %v, want %v", c.present, c.total, got, c.want)
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
