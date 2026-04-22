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

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
)

// IMDBPlugin looks up movie info on imdbapi.dev and announces MOVIE-INFO.
// Uses the same async worker pattern as TVMazePlugin so HTTP latency never
// blocks the sitebot's event loop.
type IMDBPlugin struct {
	debug    bool
	theme    *tmpl.Theme
	mu       sync.Mutex
	seen     map[string]bool
	client   *http.Client
	sections []string // only fire when section name contains one of these (case-insensitive)

	jobs      chan imdbJob
	asyncEmit func(outType, text, section, relpath string)
	startOnce sync.Once
}

type imdbJob struct {
	rel     string
	section string
	relpath string
	data    map[string]string
}

// imdbapi.dev response types — only the fields we use.
// Field names are camelCase in the JSON (not snake_case).
type imdbSearchResult struct {
	Titles []imdbTitle `json:"titles"`
}

type imdbTitle struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	PrimaryTitle  string   `json:"primaryTitle"`
	OriginalTitle string   `json:"originalTitle"`
	StartYear     int      `json:"startYear"`
	EndYear       int      `json:"endYear"`
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
	Directors       []imdbPerson `json:"directors"`
	Stars           []imdbPerson `json:"stars"`
	OriginCountries []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"originCountries"`
	SpokenLanguages []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"spokenLanguages"`
}

type imdbPerson struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

func New() *IMDBPlugin {
	return &IMDBPlugin{
		seen:     map[string]bool{},
		client:   &http.Client{Timeout: 8 * time.Second},
		sections: []string{"MOVIE", "X264", "X265", "BLURAY", "DVDR"},
		jobs:     make(chan imdbJob, 64),
	}
}

func (p *IMDBPlugin) Name() string { return "IMDB" }

func (p *IMDBPlugin) Initialize(config map[string]interface{}) error {
	imdbConfig := plugin.ConfigSection(config, "imdb")
	if d, ok := config["debug"].(bool); ok {
		p.debug = d
	}
	if tf, ok := config["theme_file"].(string); ok && tf != "" {
		th, err := tmpl.LoadTheme(tf)
		if err == nil {
			p.theme = th
		} else if p.debug {
			log.Printf("[IMDB] theme load failed: %v", err)
		}
	}
	if raw, ok := config["imdb_sections"]; ok {
		p.sections = plugin.ToStringSlice(raw, p.sections)
	}
	if raw, ok := imdbConfig["sections"]; ok {
		p.sections = plugin.ToStringSlice(raw, p.sections)
	}
	return nil
}

func (p *IMDBPlugin) Close() error { return nil }

// SetAsyncEmitter wires up the callback used to post late MOVIE-INFO lines.
func (p *IMDBPlugin) SetAsyncEmitter(fn func(outType, text, section, relpath string)) {
	p.asyncEmit = fn
}

func (p *IMDBPlugin) startWorker() {
	p.startOnce.Do(func() {
		go func() {
			for job := range p.jobs {
				p.doLookup(job)
			}
		}()
	})
}

func (p *IMDBPlugin) sectionMatches(section string) bool {
	up := strings.ToUpper(section)
	for _, s := range p.sections {
		if strings.Contains(up, strings.ToUpper(s)) {
			return true
		}
	}
	return false
}

// isMovieReleaseDirName returns true if the directory name looks like a scene
// movie release rather than a subfolder (Sample, Proof, Subs, CD1, etc).
// A movie release has dots, a -GROUP suffix, and a 4-digit year.
func isMovieReleaseDirName(rel string) bool {
	if !strings.Contains(rel, ".") || !strings.Contains(rel, "-") {
		return false
	}
	// Must contain a 4-digit year between dots (scene convention).
	return regexp.MustCompile(`\.(19|20)\d{2}\.`).MatchString(rel)
}

// extractMovieTitleYear parses "The.Matrix.1999.1080p.BluRay.x264-GROUP" into
// ("The Matrix", 1999). If no year is found, returns ("", 0).
func extractMovieTitleYear(rel string) (string, int) {
	// Strip -GROUP suffix
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

func (p *IMDBPlugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventMKDir {
		return nil, nil
	}
	if !p.sectionMatches(evt.Section) {
		return nil, nil
	}
	rel := path.Base(path.Clean(evt.Path))
	if rel == "" || rel == "." || rel == "/" {
		return nil, nil
	}
	// Skip scene subfolders (Sample, Proof, Subs, CD1, etc.)
	if !isMovieReleaseDirName(rel) {
		return nil, nil
	}

	p.mu.Lock()
	if p.seen[rel] {
		p.mu.Unlock()
		return nil, nil
	}
	p.seen[rel] = true
	if len(p.seen) > 2000 {
		p.seen = map[string]bool{rel: true}
	}
	p.mu.Unlock()

	dataCopy := map[string]string{}
	for k, v := range evt.Data {
		dataCopy[k] = v
	}

	p.startWorker()
	select {
	case p.jobs <- imdbJob{rel: rel, section: evt.Section, relpath: evt.Path, data: dataCopy}:
	default:
		if p.debug {
			log.Printf("[IMDB] queue full, dropping lookup for %q", rel)
		}
	}
	return nil, nil
}

// lookup queries imdbapi.dev. If year is non-zero, it's used to disambiguate.
// Returns the best match (prefers movie type over series, prefers matching year).
func (p *IMDBPlugin) lookup(title string, year int) (*imdbTitle, error) {
	u := "https://api.imdbapi.dev/search/titles?query=" + url.QueryEscape(title)
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("imdbapi.dev status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var sr imdbSearchResult
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, err
	}
	if len(sr.Titles) == 0 {
		return nil, fmt.Errorf("no results")
	}

	// Prefer exact year match on a movie-type title; fall back to first result.
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

	// Search results don't include genres/plot/runtime/countries/languages.
	// Fetch the full title detail by ID.
	if best.ID != "" {
		if detail, err := p.fetchDetails(best.ID); err == nil {
			return detail, nil
		} else if p.debug {
			log.Printf("[IMDB] detail fetch %s failed: %v", best.ID, err)
		}
	}
	return best, nil
}

// fetchDetails hits /titles/{id} for the full title record.
func (p *IMDBPlugin) fetchDetails(id string) (*imdbTitle, error) {
	u := "https://api.imdbapi.dev/titles/" + url.PathEscape(id)
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var t imdbTitle
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (p *IMDBPlugin) doLookup(job imdbJob) {
	title, year := extractMovieTitleYear(job.rel)
	if title == "" {
		return
	}
	m, err := p.lookup(title, year)
	if err != nil {
		if p.debug {
			log.Printf("[IMDB] lookup %q (%d) failed: %v", title, year, err)
		}
		return
	}

	genres := "N/A"
	if len(m.Genres) > 0 {
		genres = strings.Join(m.Genres, ", ")
	}
	rating := "N/A"
	if m.Rating.AggregateRating > 0 {
		rating = fmt.Sprintf("%.1f", m.Rating.AggregateRating)
	}
	votes := "N/A"
	if m.Rating.VoteCount > 0 {
		votes = fmt.Sprintf("%d", m.Rating.VoteCount)
	}
	country := "N/A"
	if len(m.OriginCountries) > 0 {
		country = m.OriginCountries[0].Name
	}
	language := "N/A"
	if len(m.SpokenLanguages) > 0 {
		language = m.SpokenLanguages[0].Name
	}
	runtime := "N/A"
	if m.RuntimeSecs > 0 {
		runtime = fmt.Sprintf("%dmin", m.RuntimeSecs/60)
	}
	yr := "N/A"
	if m.StartYear > 0 {
		yr = fmt.Sprintf("%d", m.StartYear)
	}
	link := "N/A"
	if m.ID != "" {
		link = "https://www.imdb.com/title/" + m.ID + "/"
	}
	director := "N/A"
	if len(m.Directors) > 0 {
		director = m.Directors[0].DisplayName
	}
	stars := "N/A"
	if len(m.Stars) > 0 {
		names := make([]string, 0, 3)
		for i, s := range m.Stars {
			if i >= 3 {
				break
			}
			names = append(names, s.DisplayName)
		}
		stars = strings.Join(names, ", ")
	}
	metacritic := "N/A"
	if m.Metacritic.Score > 0 {
		metacritic = fmt.Sprintf("%d", m.Metacritic.Score)
	}
	plot := m.Plot
	// Trim very long plots — IRC lines get awkward beyond ~300 chars.
	if len(plot) > 280 {
		plot = plot[:277] + "..."
	}
	if plot == "" {
		plot = "N/A"
	}

	vars := map[string]string{
		"section":    job.section,
		"relname":    job.rel,
		"title":      m.PrimaryTitle,
		"year":       yr,
		"genre":      genres,
		"rating":     rating,
		"votes":      votes,
		"metacritic": metacritic,
		"country":    country,
		"language":   language,
		"runtime":    runtime,
		"director":   director,
		"stars":      stars,
		"plot":       plot,
		"link":       link,
	}
	for k, v := range job.data {
		vars[k] = v
	}
	p.addSectionPalette(vars, job.section)

	var text string
	if p.theme != nil {
		if raw, ok := p.theme.Announces["MOVIEINFO"]; ok && raw != "" {
			text = tmpl.Render(raw, vars)
		}
	}
	if text == "" {
		text = fmt.Sprintf("MOVIE-INFO: [%s] %s - %s (%s) - Genre: %s - Rating: %s/10 (%s votes) - Metacritic: %s - Runtime: %s - Country: %s - Language: %s\nMOVIE-INFO: [%s] %s - Link: %s - Director: %s - Stars: %s",
			job.section, job.rel, m.PrimaryTitle, yr, genres, rating, votes, metacritic, runtime, country, language, job.section, job.rel, link, director, stars)
	}

	if p.asyncEmit != nil {
		p.asyncEmit("MOVIE_INFO", text, job.section, job.relpath)
	}
}

func (p *IMDBPlugin) addSectionPalette(vars map[string]string, section string) {
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("sec_c%d", i)
		vars[key] = p.sectionColor(section, i)
	}
	vars["section_colored"] = "\x03" + vars["sec_c2"] + section + "\x03"
}

func (p *IMDBPlugin) sectionColor(section string, slot int) string {
	fallback := "02"
	if p.theme != nil {
		keys := []string{
			fmt.Sprintf("COLOR_%s_%d", strings.ToUpper(section), slot),
			fmt.Sprintf("section_color.%s.%d", section, slot),
			fmt.Sprintf("section_color.%s.%d", strings.ToUpper(section), slot),
			fmt.Sprintf("COLOR_DEFAULT_%d", slot),
			fmt.Sprintf("section_color.default.%d", slot),
			"section_color." + section,
			"section_color." + strings.ToUpper(section),
			"section_color.default",
		}
		for _, key := range keys {
			if c := strings.TrimSpace(p.theme.Vars[key]); c != "" {
				return strings.TrimLeft(c, "cC")
			}
		}
	}
	return fallback
}
