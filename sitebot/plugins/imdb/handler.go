package imdb

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
	tmpl "weaveftpd/sitebot/internal/template"
)

func normalizePlotText(plot string, maxRunes int) string {
	plot = html.UnescapeString(strings.TrimSpace(plot))
	if plot == "" {
		return ""
	}
	plot = strings.Join(strings.Fields(plot), " ")
	if maxRunes <= 0 {
		return plot
	}
	runes := []rune(plot)
	if len(runes) <= maxRunes {
		return plot
	}
	cut := maxRunes - 3
	if cut < 1 {
		cut = 1
	}
	truncated := strings.TrimSpace(string(runes[:cut]))
	if idx := strings.LastIndex(truncated, " "); idx >= cut/2 {
		truncated = strings.TrimSpace(truncated[:idx])
	}
	return truncated + "..."
}

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
		sections: []string{"MOVIE", "X264-HD-1080P", "X264-HD-720P", "X264-SD", "X264-HD-FOREIGN", "X264-SD-FOREIGN", "X265", "X265-FOREIGN", "BLURAY", "DVDR", "BLURAY-UHD"},
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

var imdbYearTokenRe = regexp.MustCompile(`^(?:19|20)\d{2}$`)

// extractMovieTitleYear parses "The.Matrix.1999.1080p.BluRay.x264-GROUP" into
// ("The Matrix", 1999). If no year is found, returns ("", 0).
//
// Scene naming is Title.YEAR.quality, so when the title itself contains a
// year-like number (Blade.Runner.2049.2017, 2012.2009, 1917.2019, 1922.2017)
// the release year is the LAST year token, not the first. Taking the last keeps
// the numeric part in the title and uses the real release year for matching.
func extractMovieTitleYear(rel string) (string, int) {
	// Strip -GROUP suffix
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	tokens := strings.Split(rel, ".")
	yearIdx := -1
	for i, tok := range tokens {
		if imdbYearTokenRe.MatchString(tok) {
			yearIdx = i
		}
	}
	if yearIdx <= 0 {
		return "", 0
	}
	title := strings.Join(tokens[:yearIdx], " ")
	year := 0
	fmt.Sscanf(tokens[yearIdx], "%d", &year)
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

	best := selectBestIMDBTitle(sr.Titles, title, year)
	if best == nil {
		return nil, fmt.Errorf("no safe match")
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

func selectBestIMDBTitle(titles []imdbTitle, query string, year int) *imdbTitle {
	var best *imdbTitle
	bestScore := -1
	for i := range titles {
		t := &titles[i]
		if !isMovieLikeIMDBType(t.Type) {
			continue
		}
		titleScore := titleSimilarityScore(query, t.PrimaryTitle)
		if originalScore := titleSimilarityScore(query, t.OriginalTitle); originalScore > titleScore {
			titleScore = originalScore
		}
		if titleScore < 70 {
			continue
		}

		score := titleScore
		if year > 0 {
			if t.StartYear <= 0 {
				continue
			}
			delta := absInt(t.StartYear - year)
			switch {
			case delta == 0:
				score += 40
			case delta == 1 && titleScore >= 95:
				score += 10
			default:
				continue
			}
		}
		if strings.EqualFold(t.Type, "movie") {
			score += 10
		}
		if score > bestScore {
			best = t
			bestScore = score
		}
	}
	return best
}

func isMovieLikeIMDBType(t string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(t), " ", "")) {
	case "movie", "tvmovie", "video", "short":
		return true
	default:
		return false
	}
}

func titleSimilarityScore(query, candidate string) int {
	q := normalizeLookupTitle(query)
	c := normalizeLookupTitle(candidate)
	if q == "" || c == "" {
		return 0
	}
	if q == c {
		return 100
	}
	if strings.Contains(c, q) || strings.Contains(q, c) {
		return 85
	}

	qTokens := strings.Fields(q)
	cTokens := strings.Fields(c)
	if len(qTokens) == 0 || len(cTokens) == 0 {
		return 0
	}
	cSet := make(map[string]struct{}, len(cTokens))
	for _, token := range cTokens {
		cSet[token] = struct{}{}
	}
	cInitials := tokenInitials(cTokens)
	common := 0
	for _, token := range qTokens {
		if _, ok := cSet[token]; ok {
			common++
			continue
		}
		if len(token) >= 2 && strings.Contains(cInitials, token) {
			common++
		}
	}
	queryCoverage := common * 100 / len(qTokens)
	if queryCoverage >= 90 && common >= 2 {
		return 90
	}
	maxTokens := len(qTokens)
	if len(cTokens) > maxTokens {
		maxTokens = len(cTokens)
	}
	return common * 100 / maxTokens
}

func tokenInitials(tokens []string) string {
	var b strings.Builder
	for _, token := range tokens {
		if token != "" {
			b.WriteByte(token[0])
		}
	}
	return b.String()
}

var lookupTitleReplacer = strings.NewReplacer(
	"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
	"à", "a", "á", "a", "â", "a", "ã", "a", "å", "a",
	"è", "e", "é", "e", "ê", "e", "ë", "e",
	"ì", "i", "í", "i", "î", "i", "ï", "i",
	"ò", "o", "ó", "o", "ô", "o", "õ", "o",
	"ù", "u", "ú", "u", "û", "u",
	"ç", "c", "ñ", "n",
	"&", " and ",
)

func normalizeLookupTitle(s string) string {
	s = lookupTitleReplacer.Replace(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
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
	plot := normalizePlotText(m.Plot, 280)
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
