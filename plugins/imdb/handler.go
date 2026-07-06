// Package imdb is a weaveftpd plugin that looks up movie metadata on
// imdbapi.dev when a release directory is created, and writes a .imdb
// file into the release dir for display via show_diz.
//
// Config (in the main weaveftpd config.yml under plugins.imdb):
//
//	plugins:
//	  imdb:
//	    enabled: true
//	    sections: ["MOVIE", "X264-HD-1080P", "X264-HD-720P", "X264-SD", "X265", "BLURAY", "DVDR"]
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

	"weaveftpd/internal/plugin"
)

// Handler is the imdb plugin. One instance per weaveftpd process.
type Handler struct {
	svc      *plugin.Services
	debug    bool
	sections []string
	version  string
	proxy    string

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
		h.sections = []string{"MOVIE", "X264-HD-1080P", "X264-HD-720P", "X264-SD", "X265", "BLURAY", "DVDR"}
	}
	if v, ok := cfg["debug"].(bool); ok {
		h.debug = v
	} else if svc != nil {
		h.debug = svc.Debug
	}
	if v, ok := cfg["version"].(string); ok && strings.TrimSpace(v) != "" {
		h.version = strings.TrimSpace(v)
	} else {
		h.version = "1.0"
	}
	h.proxy = stringConfig(cfg, "proxy", "")
	client, err := newHTTPClient(10*time.Second, h.proxy)
	if err != nil {
		return err
	}
	h.client = client
	go h.worker()
	if h.debug {
		log.Printf("[IMDB] initialized, sections=%v proxy=%q", h.sections, h.proxy)
	}
	return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
	if evt == nil || evt.Type != plugin.EventMKDir {
		return nil
	}
	if h.debug {
		log.Printf("[IMDB] OnEvent MKDIR path=%s filename=%s section=%s", evt.Path, evt.Filename, evt.Section)
	}
	if h.svc == nil || h.svc.Bridge == nil {
		log.Printf("[IMDB] skipping %s: svc or bridge nil", evt.Filename)
		return nil
	}
	if !matchSection(evt.Section, h.sections) {
		if h.debug {
			log.Printf("[IMDB] skipping %s: section %q not in %v", evt.Filename, evt.Section, h.sections)
		}
		return nil
	}
	if !isMovieReleaseName(evt.Filename) {
		if h.debug {
			log.Printf("[IMDB] skipping %s: not a movie release name", evt.Filename)
		}
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

	log.Printf("[IMDB] queued lookup for %s", evt.Filename)

	select {
	case h.jobs <- job{dirPath: evt.Path, relname: evt.Filename, section: evt.Section}:
	default:
		log.Printf("[IMDB] job queue full, dropping %s", evt.Filename)
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

	best := selectBestIMDBTitle(sr.Titles, title, year)
	if best == nil {
		if h.debug {
			log.Printf("[IMDB] no safe match for %q (%d)", title, year)
		}
		return
	}

	// Fetch full detail record — search-results don't include genres/plot/etc.
	if full := h.fetchDetails(best.ID); full != nil {
		best = full
	}

	content := formatIMDBFile(best, h.version)
	filePath := path.Join(j.dirPath, ".imdb")
	if err := h.svc.Bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[IMDB] WriteFile %s failed: %v", filePath, err)
		return
	}
	log.Printf("[IMDB] Wrote .imdb for %s", j.relname)
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

func formatIMDBFile(t *imdbTitle, version string) string {
	year := "NA"
	if t.StartYear > 0 {
		year = fmt.Sprintf("%d", t.StartYear)
	}
	genres := "NA"
	if len(t.Genres) > 0 {
		genres = strings.Join(t.Genres, ", ")
	}
	rating := "NA"
	if t.Rating.AggregateRating > 0 {
		rating = fmt.Sprintf("%.1f/10 (%d votes)", t.Rating.AggregateRating, t.Rating.VoteCount)
	}
	meta := "NA"
	if t.Metacritic.Score > 0 {
		meta = fmt.Sprintf("%d", t.Metacritic.Score)
	}
	runtime := "NA"
	if t.RuntimeSecs > 0 {
		runtime = fmt.Sprintf("%d min", t.RuntimeSecs/60)
	}
	director := "NA"
	if len(t.Directors) > 0 {
		director = t.Directors[0].DisplayName
	}
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
	country := "NA"
	if len(t.OriginCountries) > 0 {
		country = t.OriginCountries[0].Name
	}
	language := "NA"
	if len(t.SpokenLanguages) > 0 {
		language = t.SpokenLanguages[0].Name
	}
	plot := t.Plot
	if plot == "" {
		plot = "NA"
	}

	bx := newInfoBox("I M D B   I N F O", version)
	bx.field("Title", t.PrimaryTitle)
	if t.OriginalTitle != "" && t.OriginalTitle != t.PrimaryTitle {
		bx.field("Original", t.OriginalTitle)
	}
	bx.field("Year", year)
	bx.sep()
	if t.ID != "" {
		bx.field("IMDB Link", "https://www.imdb.com/title/"+t.ID+"/")
	}
	bx.field("Genre", genres)
	bx.field("Rating", rating)
	bx.field("Metacritic", meta)
	bx.field("Runtime", runtime)
	bx.sep()
	bx.field("Director", director)
	bx.field("Stars", stars)
	bx.field("Country", country)
	bx.field("Language", language)
	bx.sep()
	bx.fieldWrapped("Plot", plot)
	return bx.render()
}

// =============================================================================
// Helpers (self-contained per plugin)
// =============================================================================

const (
	boxTL = "\xDA"
	boxTR = "\xBF"
	boxBL = "\xC0"
	boxBR = "\xD9"
	boxH  = "\xC4"
	boxV  = "\xB3"
	boxLT = "\xC3"
	boxRT = "\xB4"
)

const (
	boxInnerWidth = 70
	boxLabelWidth = 16
)

type infoBox struct{ lines []string }

func newInfoBox(title, version string) *infoBox {
	b := &infoBox{}
	b.lines = append(b.lines,
		boxTL+strings.Repeat(boxH, boxInnerWidth)+boxTR,
		boxV+boxCenterCell(title, boxInnerWidth)+boxV,
		boxV+boxRightCell("WeaveFTPd v"+version+" ", boxInnerWidth)+boxV,
		boxLT+strings.Repeat(boxH, boxInnerWidth)+boxRT,
	)
	return b
}

func (b *infoBox) row(s string)              { b.lines = append(b.lines, boxV+boxTextCell(s, boxInnerWidth)+boxV) }
func (b *infoBox) sep()                      { b.lines = append(b.lines, boxLT+strings.Repeat(boxH, boxInnerWidth)+boxRT) }
func (b *infoBox) field(label, value string) { b.row(boxLabel(label) + value) }

func (b *infoBox) fieldWrapped(label, value string) {
	indent := strings.Repeat(" ", boxLabelWidth)
	for i, w := range wrapWords(value, boxInnerWidth-boxLabelWidth) {
		if i == 0 {
			b.row(boxLabel(label) + w)
		} else {
			b.row(indent + w)
		}
	}
}

func (b *infoBox) render() string {
	b.lines = append(b.lines, boxBL+strings.Repeat(boxH, boxInnerWidth)+boxBR)
	return strings.Join(b.lines, "\n") + "\n"
}

func boxLabel(label string) string {
	prefix := " " + label
	dots := boxLabelWidth - len(prefix) - 2
	if dots < 1 {
		dots = 1
	}
	return prefix + strings.Repeat(".", dots) + ": "
}

func boxTextCell(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		return string(r[:w])
	}
	return string(r) + strings.Repeat(" ", w-len(r))
}

func boxCenterCell(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return string(r[:w])
	}
	l := (w - len(r)) / 2
	return strings.Repeat(" ", l) + string(r) + strings.Repeat(" ", w-len(r)-l)
}

func boxRightCell(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		return string(r[:w])
	}
	return strings.Repeat(" ", w-len(r)) + string(r)
}

func wrapWords(s string, width int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{""}
	}
	if width < 1 {
		width = 1
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) > width:
			out = append(out, line)
			line = w
		default:
			line += " " + w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

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

func stringConfig(cfg map[string]interface{}, key, fallback string) string {
	if cfg == nil {
		return fallback
	}
	if s, ok := cfg[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return fallback
}

func newHTTPClient(timeout time.Duration, proxyValue string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxyValue = strings.TrimSpace(proxyValue)
	if proxyValue != "" {
		proxyURL, err := url.Parse(proxyValue)
		if err != nil {
			return nil, fmt.Errorf("imdb proxy %q is invalid: %w", proxyValue, err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}
