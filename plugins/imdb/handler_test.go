package imdb

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
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

	var result imdbSearchResp
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

func TestTiffaraLiveSearch(t *testing.T) {
	if os.Getenv("WEAVEFTPD_TEST_LIVE_IMDB") != "1" {
		t.Skip("set WEAVEFTPD_TEST_LIVE_IMDB=1 to run the live API check")
	}

	var result imdbSearchResp
	client := &http.Client{Timeout: 10 * time.Second}
	if err := getIMDBJSON(client, imdbAPIBaseURL+"/search/titles?query=Legion%20Of%20The%20Dead", &result); err != nil {
		t.Fatalf("live Tiffara search failed: %v", err)
	}
	match := selectBestIMDBTitle(result.Titles, "Legion Of The Dead", 2001)
	if match == nil || match.ID != "tt0213802" {
		t.Fatalf("live Tiffara match = %#v, want tt0213802", match)
	}
}

func TestParseMovieName(t *testing.T) {
	title, year := parseMovieName("Daemonen.1986.REMASTERED.German.720p.BluRay.x264-CONTRiBUTiON")
	if title != "Daemonen" || year != 1986 {
		t.Fatalf("parseMovieName() = %q, %d; want Daemonen, 1986", title, year)
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
