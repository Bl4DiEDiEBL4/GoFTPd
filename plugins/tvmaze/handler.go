// Package tvmaze is a weaveftpd plugin that looks up TV show metadata on
// TVMaze when a release directory is created, and writes a .tvmaze file
// into the release dir for display via show_diz.
//
// Config (in the main weaveftpd config.yml under plugins.tvmaze):
//
//	plugins:
//	  tvmaze:
//	    enabled: true
//	    sections: ["TV"]        # section substring match (case-insensitive)
//	    debug: false
package tvmaze

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

// Handler is the tvmaze plugin. One instance per weaveftpd process.
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

// Name satisfies plugin.Plugin.
func (h *Handler) Name() string { return "tvmaze" }

// Init wires up services + config and starts the background worker.
func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	h.svc = svc
	h.sections = toStringSlice(cfg["sections"])
	if len(h.sections) == 0 {
		h.sections = []string{"TV"}
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
		log.Printf("[TVMAZE] initialized, sections=%v proxy=%q", h.sections, h.proxy)
	}
	return nil
}

// OnEvent is the hot path — must return fast. We only care about MKDIR.
func (h *Handler) OnEvent(evt *plugin.Event) error {
	if evt == nil || evt.Type != plugin.EventMKDir {
		return nil
	}
	if h.debug {
		log.Printf("[TVMAZE] OnEvent MKDIR path=%s filename=%s section=%s", evt.Path, evt.Filename, evt.Section)
	}
	if h.svc == nil || h.svc.Bridge == nil {
		log.Printf("[TVMAZE] skipping %s: svc or bridge nil", evt.Filename)
		return nil
	}
	if !matchSection(evt.Section, h.sections) {
		if h.debug {
			log.Printf("[TVMAZE] skipping %s: section %q not in %v", evt.Filename, evt.Section, h.sections)
		}
		return nil
	}
	if !isTVReleaseName(evt.Filename) {
		if h.debug {
			log.Printf("[TVMAZE] skipping %s: not a TV release name", evt.Filename)
		}
		return nil
	}

	// Dedupe by path — avoids double-lookup if the same dir is re-created.
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

	log.Printf("[TVMAZE] queued lookup for %s", evt.Filename)

	select {
	case h.jobs <- job{dirPath: evt.Path, relname: evt.Filename, section: evt.Section}:
	default:
		log.Printf("[TVMAZE] job queue full, dropping %s", evt.Filename)
	}
	return nil
}

// Stop closes the worker.
func (h *Handler) Stop() error {
	h.stopOnce.Do(func() { close(h.stopCh) })
	return nil
}

// worker drains the job queue sequentially (TVMaze has a request/second limit).
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
// TVMaze API
// =============================================================================

type tvmShow struct {
	ID        int      `json:"id"`
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Language  string   `json:"language"`
	Genres    []string `json:"genres"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	Premiered string   `json:"premiered"`
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Network struct {
		Name    string `json:"name"`
		Country struct {
			Code string `json:"code"`
			Name string `json:"name"`
		} `json:"country"`
	} `json:"network"`
	WebChannel struct {
		Name string `json:"name"`
	} `json:"webChannel"`
	Summary   string `json:"summary"`
	Externals struct {
		IMDB string `json:"imdb"`
	} `json:"externals"`
	Embedded struct {
		Episodes []tvmEpisode `json:"episodes"`
	} `json:"_embedded"`
}

type tvmEpisode struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Season  int    `json:"season"`
	Number  int    `json:"number"`
	Airdate string `json:"airdate"`
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

type tvmSearchResult struct {
	Score float64 `json:"score"`
	Show  tvmShow `json:"show"`
}

func (h *Handler) doLookup(j job) {
	title, yearHint, season, episode := parseTVNameWithYear(j.relname)
	if title == "" {
		log.Printf("[TVMAZE] parseTVName returned empty title for %s, skipping", j.relname)
		return
	}
	log.Printf("[TVMAZE] lookup %s (title=%q season=%d episode=%d)", j.relname, title, season, episode)

	show, err := h.lookupShow(title, yearHint)
	if err != nil {
		log.Printf("[TVMAZE] search %q failed: %v", title, err)
		return
	}

	if show.ID > 0 {
		if full, err := h.fetchShowWithEpisodes(show.ID); err == nil && full.ID > 0 {
			show = full
		} else if h.debug {
			log.Printf("[TVMAZE] episode fetch %q failed: %v", title, err)
		}
	}

	var ep *tvmEpisode
	if season > 0 && episode > 0 {
		for i := range show.Embedded.Episodes {
			e := &show.Embedded.Episodes[i]
			if e.Season == season && e.Number == episode {
				ep = e
				break
			}
		}
	}

	content := formatTVMazeFile(show, ep, h.version)
	filePath := path.Join(j.dirPath, ".tvmaze")
	if err := h.svc.Bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[TVMAZE] WriteFile %s failed: %v", filePath, err)
		return
	}
	log.Printf("[TVMAZE] Wrote .tvmaze for %s", j.relname)
}

func (h *Handler) lookupShow(title string, yearHint int) (*tvmShow, error) {
	var lastErr error
	for _, query := range tvmazeLookupQueries(title) {
		show, err := h.searchShow(query, yearHint)
		if err == nil {
			return show, nil
		}
		lastErr = fmt.Errorf("%s: %w", query, err)
		if show, singleErr := h.singleSearchShow(query); singleErr == nil && show.ID > 0 {
			return show, nil
		} else if singleErr != nil {
			lastErr = fmt.Errorf("%s: %w", query, singleErr)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no results")
}

func (h *Handler) searchShow(title string, yearHint int) (*tvmShow, error) {
	q := url.QueryEscape(title)
	searchURL := fmt.Sprintf("https://api.tvmaze.com/search/shows?q=%s", q)
	resp, err := h.client.Get(searchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var results []tvmSearchResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no results")
	}
	show := selectBestTVMazeShow(results, title, yearHint)
	if show == nil {
		return nil, fmt.Errorf("no safe match")
	}
	return show, nil
}

func (h *Handler) singleSearchShow(title string) (*tvmShow, error) {
	q := url.QueryEscape(title)
	searchURL := fmt.Sprintf("https://api.tvmaze.com/singlesearch/shows?q=%s", q)
	resp, err := h.client.Get(searchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var show tvmShow
	if err := json.Unmarshal(body, &show); err != nil {
		return nil, err
	}
	if show.ID == 0 {
		return nil, fmt.Errorf("no results")
	}
	return &show, nil
}

func (h *Handler) fetchShowWithEpisodes(id int) (*tvmShow, error) {
	u := fmt.Sprintf("https://api.tvmaze.com/shows/%d?embed=episodes", id)
	resp, err := h.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var show tvmShow
	if err := json.Unmarshal(body, &show); err != nil {
		return nil, err
	}
	return &show, nil
}

// =============================================================================
// Parsing / formatting
// =============================================================================

// isTVReleaseName: scene TV naming — dots, dash, and SxxEyy or year tag.
func isTVReleaseName(rel string) bool {
	if !strings.Contains(rel, ".") || !strings.Contains(rel, "-") {
		return false
	}
	re := regexp.MustCompile(`(?i)(^|\.)(S\d{1,2}(E\d{1,3})?|Season\.?\d+|\d{4})(\.|$)`)
	return re.MatchString(rel)
}

// parseTVName: "Kill.Blue.S01E02.1080p.WEB.H264-SKYANiME" -> ("Kill Blue", 1, 2)
func parseTVName(rel string) (string, int, int) {
	title, _, season, episode := parseTVNameWithYear(rel)
	return title, season, episode
}

func parseTVNameWithYear(rel string) (string, int, int, int) {
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	re := regexp.MustCompile(`(?i)^(.+?)\.S(\d{1,2})E(\d{1,3})\.`)
	if m := re.FindStringSubmatch(rel); m != nil {
		var s, e int
		fmt.Sscanf(m[2], "%d", &s)
		fmt.Sscanf(m[3], "%d", &e)
		title, yearHint := splitTrailingLookupYear(strings.ReplaceAll(m[1], ".", " "))
		return title, yearHint, s, e
	}
	re2 := regexp.MustCompile(`(?i)^(.+?)\.(S\d{1,2}|Season\.?\d+|(\d{4}))\.`)
	if m := re2.FindStringSubmatch(rel); m != nil {
		yearHint := 0
		if len(m) > 3 {
			yearHint, _ = parseLookupYear(m[3])
		}
		return strings.ReplaceAll(m[1], ".", " "), yearHint, 0, 0
	}
	title, yearHint := splitTrailingLookupYear(strings.ReplaceAll(rel, ".", " "))
	return title, yearHint, 0, 0
}

func selectBestTVMazeShow(results []tvmSearchResult, query string, yearHintOpt ...int) *tvmShow {
	yearHint := 0
	if len(yearHintOpt) > 0 {
		yearHint = yearHintOpt[0]
	}
	var best *tvmShow
	bestScore := -1
	for i := range results {
		show := &results[i].Show
		titleScore := titleSimilarityScore(query, show.Name)
		if titleScore < 70 {
			continue
		}
		score := titleScore
		if results[i].Score > 0 {
			score += int(results[i].Score * 5)
		}
		if yearHint > 0 && premiereYear(show.Premiered) == yearHint {
			score += 20
		}
		if score > bestScore {
			best = show
			bestScore = score
		}
	}
	return best
}

func tvmazeLookupQueries(title string) []string {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	queries := []string{title}
	if base, _ := splitTrailingLookupYear(title); base != "" && !sameLookupTitle(base, title) {
		queries = append(queries, base)
	}
	switch normalizeLookupTitle(title) {
	case "love island us":
		queries = append(queries, "Love Island USA")
	}
	return uniqueLookupQueries(queries)
}

func uniqueLookupQueries(queries []string) []string {
	out := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		key := normalizeLookupTitle(query)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, query)
	}
	return out
}

func sameLookupTitle(a, b string) bool {
	return normalizeLookupTitle(a) == normalizeLookupTitle(b)
}

func splitTrailingLookupYear(title string) (string, int) {
	fields := strings.Fields(strings.TrimSpace(title))
	if len(fields) < 2 {
		return strings.TrimSpace(title), 0
	}
	year, ok := parseLookupYear(fields[len(fields)-1])
	if !ok {
		return strings.TrimSpace(title), 0
	}
	return strings.Join(fields[:len(fields)-1], " "), year
}

func parseLookupYear(s string) (int, bool) {
	if len(s) != 4 {
		return 0, false
	}
	var year int
	if _, err := fmt.Sscanf(s, "%d", &year); err != nil {
		return 0, false
	}
	return year, year >= 1900 && year <= 2099
}

func premiereYear(premiered string) int {
	if len(premiered) < 4 {
		return 0
	}
	year, ok := parseLookupYear(premiered[:4])
	if !ok {
		return 0
	}
	return year
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
	s = strings.NewReplacer("'", "", "’", "", "`", "", "´", "").Replace(s)
	var b strings.Builder
	lastSpace := false
	lastClass := 0
	for _, r := range s {
		class := 0
		if r >= 'a' && r <= 'z' {
			class = 1
		} else if r >= '0' && r <= '9' {
			class = 2
		}
		if class != 0 {
			if !lastSpace && lastClass != 0 && class != lastClass {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			lastSpace = false
			lastClass = class
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
		lastClass = 0
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func formatTVMazeFile(show *tvmShow, ep *tvmEpisode, version string) string {
	premiered := show.Premiered
	if len(premiered) >= 4 {
		premiered = premiered[:4]
	}
	rating := "NA"
	if show.Rating.Average > 0 {
		rating = fmt.Sprintf("%.1f", show.Rating.Average)
	}
	country := show.Network.Country.Code
	if country == "" {
		country = "NA"
	}
	language := show.Language
	if language == "" {
		language = "NA"
	}
	network := show.Network.Name
	if network == "" {
		network = show.WebChannel.Name
	}
	if network == "" {
		network = "NA"
	}
	status := show.Status
	if status == "" {
		status = "NA"
	}
	plot := ""
	if ep != nil && ep.Summary != "" {
		plot = ep.Summary
	} else {
		plot = show.Summary
	}
	plot = stripHTML(plot)
	if plot == "" {
		plot = "NA"
	}

	bx := newInfoBox("T V M A Z E   I N F O", version)
	bx.field("Title", show.Name)
	if ep != nil {
		bx.field("Episode", fmt.Sprintf("S%02dE%02d - %s", ep.Season, ep.Number, ep.Name))
	}
	if premiered != "" {
		bx.field("Premiered", premiered)
	}
	bx.sep()
	if show.Externals.IMDB != "" {
		bx.field("IMDB Link", "https://www.imdb.com/title/"+show.Externals.IMDB+"/")
	} else {
		bx.field("IMDB Link", "NA")
	}
	if show.URL != "" {
		bx.field("TVMaze Link", show.URL)
	}
	if ep != nil && ep.URL != "" {
		bx.field("Episode Link", ep.URL)
	}
	if len(show.Genres) > 0 {
		bx.field("Genre", strings.Join(show.Genres, ", "))
	}
	if show.Type != "" {
		bx.field("Type", show.Type)
	}
	bx.field("User Rating", rating)
	bx.sep()
	bx.field("Country", country)
	bx.field("Language", language)
	bx.field("Network", network)
	bx.field("Status", status)
	if ep != nil && ep.Airdate != "" {
		bx.field("Airdate", ep.Airdate)
	}
	bx.sep()
	bx.fieldWrapped("Plot", plot)
	return bx.render()
}

// =============================================================================
// Helpers (kept local so each plugin is self-contained)
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

func stripHTML(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.TrimSpace(s)
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

// toStringSlice normalizes yaml-parsed slices which can come back as
// []interface{} or a single string.
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
			return nil, fmt.Errorf("tvmaze proxy %q is invalid: %w", proxyValue, err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}, nil
}
