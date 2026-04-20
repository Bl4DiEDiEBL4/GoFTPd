// Package imdb is a goftpd plugin that looks up movie metadata on
// imdbapi.dev when a release directory is created, and writes a .imdb
// file into the release dir for display via show_diz.
//
// Config (in the main goftpd config.yml under plugins.imdb):
//
//	plugins:
//	  imdb:
//	    enabled: true
//	    sections: ["MOVIE", "X264", "X265", "BLURAY", "DVDR"]
//	    debug: false
package imdb

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"goftpd/internal/plugin"
)

// Handler is the imdb plugin. One instance per goftpd process.
type Handler struct {
	svc      *plugin.Services
	debug    bool
	sections []string

	client   *http.Client
	jobs     chan job
	seen     map[string]bool
	seenMu   sync.Mutex
	stopCh   chan struct{}
	stopOnce sync.Once
}

type job struct {
	dirPath string
	relname string
	section string
}

// New returns an uninitialised Handler. Init() wires it up.
func New() *Handler {
	return &Handler{
		client: &http.Client{Timeout: 10 * time.Second},
		jobs:   make(chan job, 128),
		seen:   make(map[string]bool),
		stopCh: make(chan struct{}),
	}
}

func (h *Handler) Name() string { return "imdb" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.sections = toStringSlice(cfg["sections"])
	if len(h.sections) == 0 {
		h.sections = []string{"MOVIE", "X264", "X265", "BLURAY", "DVDR"}
	}
	if v, ok := cfg["debug"].(bool); ok {
		h.debug = v
	} else if svc != nil {
		h.debug = svc.Debug
	}
	go h.worker()
	if h.debug {
		log.Printf("[IMDB] initialized, sections=%v", h.sections)
	}
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
	if evt == nil || evt.Type != plugin.EventMKDir {
		return nil
	}
	if h.svc == nil || h.svc.Bridge == nil {
		return nil
	}
	if !matchSection(evt.Section, h.sections) {
		return nil
	}
	if !isMovieReleaseName(evt.Filename) {
		return nil
	}

	h.seenMu.Lock()
	if h.seen[evt.Path] {
		h.seenMu.Unlock()
		return nil
	}
	h.seen[evt.Path] = true
	if len(h.seen) > 2000 {
		h.seen = make(map[string]bool)
	}
	h.seenMu.Unlock()

	select {
	case h.jobs <- job{dirPath: evt.Path, relname: evt.Filename, section: evt.Section}:
	default:
		if h.debug {
			log.Printf("[IMDB] job queue full, dropping %s", evt.Filename)
		}
	}
	return nil
}

func (h *Handler) Stop() error {
	h.stopOnce.Do(func() { close(h.stopCh) })
	return nil
}

func (h *Handler) worker() {
	for {
		select {
		case <-h.stopCh:
			return
		case j := <-h.jobs:
			h.doLookup(j)
		}
	}
}

// =============================================================================
// imdbapi.dev API
// =============================================================================

type imdbSearchResp struct {
	Titles []imdbTitle `json:"titles"`
}

type imdbTitle struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	PrimaryTitle  string   `json:"primaryTitle"`
	OriginalTitle string   `json:"originalTitle"`
	StartYear     int      `json:"startYear"`
	RuntimeSecs   int      `json:"runtimeSeconds"`
	Genres        []string `json:"genres"`
	Plot          string   `json:"plot"`
	Rating        struct {
		AggregateRating float64 `json:"aggregateRating"`
		VoteCount       int     `json:"voteCount"`
	} `json:"rating"`
	Metacritic struct {
		Score int `json:"score"`
	} `json:"metacritic"`
	Directors []struct {
		DisplayName string `json:"displayName"`
	} `json:"directors"`
	Stars []struct {
		DisplayName string `json:"displayName"`
	} `json:"stars"`
	OriginCountries []struct {
		Name string `json:"name"`
	} `json:"originCountries"`
	SpokenLanguages []struct {
		Name string `json:"name"`
	} `json:"spokenLanguages"`
}

func (h *Handler) doLookup(j job) {
	title, year := parseMovieName(j.relname)
	if title == "" {
		return
	}

	searchURL := "https://api.imdbapi.dev/search/titles?query=" + url.QueryEscape(title)
	resp, err := h.client.Get(searchURL)
	if err != nil {
		if h.debug {
			log.Printf("[IMDB] search %q failed: %v", title, err)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	var sr imdbSearchResp
	if err := json.Unmarshal(body, &sr); err != nil {
		return
	}
	if len(sr.Titles) == 0 {
		return
	}

	// Pick best: prefer exact year + movie type, else first movie, else first.
	var best *imdbTitle
	for i := range sr.Titles {
		t := &sr.Titles[i]
		if year > 0 && t.StartYear == year && strings.EqualFold(t.Type, "movie") {
			best = t
			break
		}
		if best == nil && strings.EqualFold(t.Type, "movie") {
			best = t
		}
	}
	if best == nil {
		best = &sr.Titles[0]
	}

	// Fetch full detail record — search-results don't include genres/plot/etc.
	if full := h.fetchDetails(best.ID); full != nil {
		best = full
	}

	content := formatIMDBFile(best)
	filePath := path.Join(j.dirPath, ".imdb")
	if err := h.svc.Bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[IMDB] WriteFile %s failed: %v", filePath, err)
		return
	}
	if h.debug {
		log.Printf("[IMDB] Wrote .imdb for %s", j.relname)
	}
}

func (h *Handler) fetchDetails(id string) *imdbTitle {
	if id == "" {
		return nil
	}
	u := "https://api.imdbapi.dev/titles/" + url.PathEscape(id)
	resp, err := h.client.Get(u)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var t imdbTitle
	if err := json.Unmarshal(body, &t); err != nil {
		return nil
	}
	return &t
}

// =============================================================================
// Parsing / formatting
// =============================================================================

func isMovieReleaseName(rel string) bool {
	if !strings.Contains(rel, ".") || !strings.Contains(rel, "-") {
		return false
	}
	return regexp.MustCompile(`\.(19|20)\d{2}\.`).MatchString(rel)
}

func parseMovieName(rel string) (string, int) {
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	re := regexp.MustCompile(`\.((?:19|20)\d{2})\.`)
	loc := re.FindStringSubmatchIndex(rel)
	if loc == nil {
		return "", 0
	}
	title := strings.ReplaceAll(rel[:loc[0]], ".", " ")
	year := 0
	fmt.Sscanf(rel[loc[2]:loc[3]], "%d", &year)
	return strings.TrimSpace(title), year
}

func formatIMDBFile(t *imdbTitle) string {
	var b strings.Builder
	const bar = "========================== IMDB INFO v1.0 =========================="
	fmt.Fprintf(&b, "%s\n\n", bar)

	fmt.Fprintf(&b, " Title........: %s\n", t.PrimaryTitle)
	if t.OriginalTitle != "" && t.OriginalTitle != t.PrimaryTitle {
		fmt.Fprintf(&b, " Original.....: %s\n", t.OriginalTitle)
	}
	year := "NA"
	if t.StartYear > 0 {
		year = fmt.Sprintf("%d", t.StartYear)
	}
	fmt.Fprintf(&b, " Year.........: %s\n", year)
	fmt.Fprintf(&b, " -\n")

	if t.ID != "" {
		fmt.Fprintf(&b, " IMDB Link....: https://www.imdb.com/title/%s/\n", t.ID)
	}
	genres := "NA"
	if len(t.Genres) > 0 {
		genres = strings.Join(t.Genres, ", ")
	}
	fmt.Fprintf(&b, " Genre........: %s\n", genres)
	rating := "NA"
	if t.Rating.AggregateRating > 0 {
		rating = fmt.Sprintf("%.1f/10 (%d votes)", t.Rating.AggregateRating, t.Rating.VoteCount)
	}
	fmt.Fprintf(&b, " Rating.......: %s\n", rating)
	meta := "NA"
	if t.Metacritic.Score > 0 {
		meta = fmt.Sprintf("%d", t.Metacritic.Score)
	}
	fmt.Fprintf(&b, " Metacritic...: %s\n", meta)
	runtime := "NA"
	if t.RuntimeSecs > 0 {
		runtime = fmt.Sprintf("%d min", t.RuntimeSecs/60)
	}
	fmt.Fprintf(&b, " Runtime......: %s\n", runtime)
	fmt.Fprintf(&b, " -\n")

	director := "NA"
	if len(t.Directors) > 0 {
		director = t.Directors[0].DisplayName
	}
	fmt.Fprintf(&b, " Director.....: %s\n", director)
	stars := "NA"
	if len(t.Stars) > 0 {
		names := make([]string, 0, 3)
		for i, s := range t.Stars {
			if i >= 3 {
				break
			}
			names = append(names, s.DisplayName)
		}
		stars = strings.Join(names, ", ")
	}
	fmt.Fprintf(&b, " Stars........: %s\n", stars)
	country := "NA"
	if len(t.OriginCountries) > 0 {
		country = t.OriginCountries[0].Name
	}
	fmt.Fprintf(&b, " Country......: %s\n", country)
	language := "NA"
	if len(t.SpokenLanguages) > 0 {
		language = t.SpokenLanguages[0].Name
	}
	fmt.Fprintf(&b, " Language.....: %s\n", language)
	fmt.Fprintf(&b, " -\n")

	plot := t.Plot
	if plot == "" {
		plot = "NA"
	}
	fmt.Fprintf(&b, " Plot.........: %s\n", wrapPlot(plot, 70))
	fmt.Fprintf(&b, "\n%s\n", bar)
	return b.String()
}

// =============================================================================
// Helpers (self-contained per plugin)
// =============================================================================

func matchSection(section string, allowed []string) bool {
	if section == "" || len(allowed) == 0 {
		return false
	}
	up := strings.ToUpper(section)
	for _, s := range allowed {
		if strings.Contains(up, strings.ToUpper(s)) {
			return true
		}
	}
	return false
}

func wrapPlot(s string, width int) string {
	s = strings.TrimSpace(s)
	if len(s) <= width {
		return s
	}
	var out strings.Builder
	words := strings.Fields(s)
	line := ""
	first := true
	for _, w := range words {
		if len(line)+1+len(w) > width {
			if first {
				out.WriteString(line)
				first = false
			} else {
				out.WriteString("\n                ")
				out.WriteString(line)
			}
			line = w
			continue
		}
		if line == "" {
			line = w
		} else {
			line += " " + w
		}
	}
	if line != "" {
		if first {
			out.WriteString(line)
		} else {
			out.WriteString("\n                ")
			out.WriteString(line)
		}
	}
	return out.String()
}

func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		out := make([]string, 0, len(s))
		for _, x := range s {
			if str, ok := x.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case string:
		if s == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	return nil
}
