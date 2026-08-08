package imdb

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetIMDBJSONRetriesAndDecodesTiffaraSearch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"titles":[{"id":"tt0213802","type":"movie","primaryTitle":"Legion of the Dead","originalTitle":"Legion of the Dead","startYear":2001}]}`))
	}))
	defer server.Close()

	var result imdbSearchResult
	if err := getIMDBJSON(server.Client(), server.URL, &result); err != nil {
		t.Fatalf("getIMDBJSON() error = %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want 2", requests.Load())
	}
	if len(result.Titles) != 1 || result.Titles[0].ID != "tt0213802" {
		t.Fatalf("decoded titles = %#v", result.Titles)
	}
}

func TestNormalizePlotText(t *testing.T) {
	got := normalizePlotText("What was supposed to be fun &amp; games\nbecomes a long story", 24)
	want := "What was supposed..."
	if got != want {
		t.Fatalf("normalizePlotText() = %q, want %q", got, want)
	}
}

func TestExtractMovieTitleYear(t *testing.T) {
	title, year := extractMovieTitleYear("Daemonen.1986.REMASTERED.German.720p.BluRay.x264-CONTRiBUTiON")
	if title != "Daemonen" || year != 1986 {
		t.Fatalf("extractMovieTitleYear() = %q, %d; want Daemonen, 1986", title, year)
	}
}

func TestExtractMovieTitleYearNumericTitles(t *testing.T) {
	cases := []struct {
		rel       string
		wantTitle string
		wantYear  int
	}{
		{"Blade.Runner.2049.2017.1080p.BluRay.x264-GROUP", "Blade Runner 2049", 2017},
		{"2012.2009.1080p.BluRay.x264-GROUP", "2012", 2009},
		{"1917.2019.2160p.UHD.BluRay.x265-GROUP", "1917", 2019},
		{"1922.2017.1080p.NF.WEB-DL.x264-GROUP", "1922", 2017},
		{"The.Matrix.1999.1080p.BluRay.x264-GROUP", "The Matrix", 1999},
	}
	for _, c := range cases {
		title, year := extractMovieTitleYear(c.rel)
		if title != c.wantTitle || year != c.wantYear {
			t.Fatalf("extractMovieTitleYear(%q) = %q, %d; want %q, %d", c.rel, title, year, c.wantTitle, c.wantYear)
		}
	}
}

func TestSelectBestIMDBTitleRejectsYearMismatch(t *testing.T) {
	titles := []imdbTitle{{
		ID:           "tt1457767",
		Type:         "movie",
		PrimaryTitle: "The Conjuring",
		StartYear:    2013,
	}}
	if got := selectBestIMDBTitle(titles, "Daemonen", 1986); got != nil {
		t.Fatalf("selectBestIMDBTitle() = %#v, want nil for unsafe year mismatch", got)
	}
}

func TestSelectBestIMDBTitleAcceptsLocalizedExactYear(t *testing.T) {
	titles := []imdbTitle{{
		ID:           "tt0000001",
		Type:         "movie",
		PrimaryTitle: "Dämonen",
		StartYear:    1986,
	}}
	got := selectBestIMDBTitle(titles, "Daemonen", 1986)
	if got == nil || got.ID != "tt0000001" {
		t.Fatalf("selectBestIMDBTitle() = %#v, want localized exact-year match", got)
	}
}
