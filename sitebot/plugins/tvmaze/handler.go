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

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
	tmpl "weaveftpd/sitebot/internal/template"
)

type TVMazePlugin struct {
	debug    bool
	theme    *tmpl.Theme
	mu       sync.Mutex
	seen     map[string]bool
	client   *http.Client
	sections []string // only announce when section contains one of these (case-insensitive)

	// Async lookup plumbing. OnEvent enqueues a job; the worker goroutine
	// performs the HTTP call and uses asyncEmit to post the resulting TV-INFO
	// line to IRC. This keeps the bot's event loop from stalling for up to
	// 8s per HTTP timeout when TVMaze is slow or unreachable.
	jobs      chan tvmazeJob
	asyncEmit func(outType, text string, section, relpath string)
	startOnce sync.Once
}

type tvmazeJob struct {
	rel     string
	section string
	relpath string
	data    map[string]string
}

type tvmazeShow struct {
	Name      string   `json:"name"`
	Premiered string   `json:"premiered"`
	Language  string   `json:"language"`
	Genres    []string `json:"genres"`
	Type      string   `json:"type"`
	Rating    struct {
		Average float64 `json:"average"`
	} `json:"rating"`
	Network struct {
		Name string `json:"name"`
	} `json:"network"`
	WebChannel struct {
		Name string `json:"name"`
	} `json:"webChannel"`
	URL string `json:"url"`
}

type tvmazeSearchResult struct {
	Score float64    `json:"score"`
	Show  tvmazeShow `json:"show"`
}

func New() *TVMazePlugin {
	return &TVMazePlugin{
		seen:     map[string]bool{},
		client:   &http.Client{Timeout: 8 * time.Second},
		sections: []string{"TV"},
		jobs:     make(chan tvmazeJob, 64),
	}
}

// SetAsyncEmitter wires up the callback used to post late TV-INFO lines.
// Called by the bot during plugin setup. If not set, async output is dropped.
func (p *TVMazePlugin) SetAsyncEmitter(fn func(outType, text string, section, relpath string)) {
	p.asyncEmit = fn
}

// startWorker launches a single background goroutine that drains tvmaze jobs.
// Running one lookup at a time keeps the TVMaze API happy and avoids a stampede.
func (p *TVMazePlugin) startWorker() {
	p.startOnce.Do(func() {
		go func() {
			for job := range p.jobs {
				p.doLookup(job)
			}
		}()
	})
}

func (p *TVMazePlugin) Name() string { return "TVMaze" }

func (p *TVMazePlugin) Initialize(config map[string]interface{}) error {
	tvmazeConfig := plugin.ConfigSection(config, "tvmaze")
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}
	if raw, ok := config["tvmaze_sections"]; ok {
		p.sections = plugin.ToStringSlice(raw, p.sections)
	}
	if raw, ok := tvmazeConfig["sections"]; ok {
		p.sections = plugin.ToStringSlice(raw, p.sections)
	}
	return nil
}

func (p *TVMazePlugin) Close() error { return nil }

// isReleaseDirName returns true if the directory name looks like a scene
// release rather than a subfolder (Sample, Proof, Subs, etc). A real release
// has dots, a -GROUP suffix, and a season/year tag.
func isReleaseDirName(rel string) bool {
	// Release names here usually contain dots and a group suffix.
	if !strings.Contains(rel, ".") {
		return false
	}
	if !strings.Contains(rel, "-") {
		return false
	}
	// Must have a season/episode tag or a 4-digit year.
	re := regexp.MustCompile(`(?i)(^|\.)(S\d{1,2}(E\d{1,3})?|Season\.?\d+|\d{4})(\.|$)`)
	return re.MatchString(rel)
}

// extractShowName parses a release name and returns a query title.
// e.g. "Fire.Country.S04E15.1080p.WEB.h264-ETHEL" -> "Fire Country"
func extractShowName(rel string) string {
	title, _ := extractShowQuery(rel)
	return title
}

func extractShowQuery(rel string) (string, int) {
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	seasonRe := regexp.MustCompile(`(?i)^(.+?)\.S\d{1,2}E\d{1,3}\.`)
	if m := seasonRe.FindStringSubmatch(rel); m != nil {
		return splitTrailingLookupYear(strings.ReplaceAll(m[1], ".", " "))
	}
	tagRe := regexp.MustCompile(`(?i)^(.+?)\.(S\d{1,2}|Season\.?\d+|(\d{4}))\.`)
	if m := tagRe.FindStringSubmatch(rel); m != nil {
		yearHint := 0
		if len(m) > 3 {
			yearHint, _ = parseLookupYear(m[3])
		}
		return strings.ReplaceAll(m[1], ".", " "), yearHint
	}
	return splitTrailingLookupYear(strings.ReplaceAll(rel, ".", " "))
}

func (p *TVMazePlugin) sectionMatches(section string) bool {
	up := strings.ToUpper(section)
	for _, s := range p.sections {
		if strings.Contains(up, strings.ToUpper(s)) {
			return true
		}
	}
	return false
}

func (p *TVMazePlugin) lookup(query string, yearHint int) (*tvmazeShow, error) {
	var lastErr error
	for _, candidateQuery := range tvmazeLookupQueries(query) {
		show, err := p.search(candidateQuery, yearHint)
		if err == nil {
			return show, nil
		}
		lastErr = fmt.Errorf("%s: %w", candidateQuery, err)
		if show, singleErr := p.singleSearch(candidateQuery); singleErr == nil && show.Name != "" {
			return show, nil
		} else if singleErr != nil {
			lastErr = fmt.Errorf("%s: %w", candidateQuery, singleErr)
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no results")
}

func (p *TVMazePlugin) search(query string, yearHint int) (*tvmazeShow, error) {
	u := "https://api.tvmaze.com/search/shows?q=" + url.QueryEscape(query)
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tvmaze status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var results []tvmazeSearchResult
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no results")
	}
	show := selectBestTVMazeShow(results, query, yearHint)
	if show == nil {
		return nil, fmt.Errorf("no safe match")
	}
	return show, nil
}

func (p *TVMazePlugin) singleSearch(query string) (*tvmazeShow, error) {
	u := "https://api.tvmaze.com/singlesearch/shows?q=" + url.QueryEscape(query)
	resp, err := p.client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tvmaze status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var show tvmazeShow
	if err := json.Unmarshal(body, &show); err != nil {
		return nil, err
	}
	if show.Name == "" {
		return nil, fmt.Errorf("no results")
	}
	return &show, nil
}

func selectBestTVMazeShow(results []tvmazeSearchResult, query string, yearHintOpt ...int) *tvmazeShow {
	yearHint := 0
	if len(yearHintOpt) > 0 {
		yearHint = yearHintOpt[0]
	}
	var best *tvmazeShow
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

func (p *TVMazePlugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
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
	// Skip scene subfolders like Sample, Proof, Subs, Cover, etc.
	// These sit inside a release dir, not at the section root.
	if !isReleaseDirName(rel) {
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

	// Copy event data so the worker goroutine has a stable snapshot.
	dataCopy := map[string]string{}
	for k, v := range evt.Data {
		dataCopy[k] = v
	}

	p.startWorker()
	// Enqueue non-blocking — if queue is full (TVMaze is stuck on slow
	// lookups), drop this one rather than blocking the bot's event loop.
	select {
	case p.jobs <- tvmazeJob{rel: rel, section: evt.Section, relpath: evt.Path, data: dataCopy}:
	default:
		if p.debug {
			log.Printf("[TVMaze] queue full, dropping lookup for %q", rel)
		}
	}
	return nil, nil
}

// doLookup performs the TVMaze HTTP call and emits TV-INFO asynchronously.
// Runs on the plugin's worker goroutine — can block on HTTP without stalling
// the bot's main event loop.
func (p *TVMazePlugin) doLookup(job tvmazeJob) {
	query, yearHint := extractShowQuery(job.rel)
	if query == "" {
		return
	}
	show, err := p.lookup(query, yearHint)
	if err != nil {
		if p.debug {
			log.Printf("[TVMaze] lookup %q failed: %v", query, err)
		}
		return
	}

	genres := "N/A"
	if len(show.Genres) > 0 {
		genres = strings.Join(show.Genres, ", ")
	}
	network := show.Network.Name
	if network == "" {
		network = show.WebChannel.Name
	}
	if network == "" {
		network = "N/A"
	}
	showType := show.Type
	if showType == "" {
		showType = "N/A"
	}
	language := show.Language
	if language == "" {
		language = "N/A"
	}
	rating := "N/A"
	if show.Rating.Average > 0 {
		rating = fmt.Sprintf("%.1f", show.Rating.Average)
	}
	link := show.URL
	if link == "" {
		link = "N/A"
	}

	vars := map[string]string{
		"section":  job.section,
		"relname":  job.rel,
		"genre":    genres,
		"type":     showType,
		"network":  network,
		"rating":   rating,
		"language": language,
		"link":     link,
		"title":    show.Name,
	}
	for k, v := range job.data {
		vars[k] = v
	}
	p.addSectionPalette(vars, job.section)

	var text string
	if p.theme != nil {
		if raw, ok := p.theme.Announces["TVINFO"]; ok && raw != "" {
			text = tmpl.Render(raw, vars)
		}
	}
	if text == "" {
		text = fmt.Sprintf("TV-INFO: [%s] %s - Genre: %s - Type: %s - Link: %s\nTV-INFO: [%s] %s - Network: %s - Rating: %s - Language: %s",
			job.section, job.rel, genres, showType, link, job.section, job.rel, network, rating, language)
	}

	if p.asyncEmit != nil {
		p.asyncEmit("TV_INFO", text, job.section, job.relpath)
	}
}

func (p *TVMazePlugin) addSectionPalette(vars map[string]string, section string) {
	for i := 1; i <= 5; i++ {
		key := fmt.Sprintf("sec_c%d", i)
		vars[key] = p.sectionColor(section, i)
	}
	vars["section_colored"] = "\x03" + vars["sec_c2"] + section + "\x03"
}

func (p *TVMazePlugin) sectionColor(section string, slot int) string {
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
