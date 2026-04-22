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

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
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
	Name     string   `json:"name"`
	Language string   `json:"language"`
	Genres   []string `json:"genres"`
	Type     string   `json:"type"`
	Rating   struct {
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
	Show tvmazeShow `json:"show"`
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
	// Scene releases always contain dots and a group suffix.
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

// extractShowName parses a scene-style release name and returns a query title.
// e.g. "Fire.Country.S04E15.1080p.WEB.h264-ETHEL" -> "Fire Country"
func extractShowName(rel string) string {
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	re := regexp.MustCompile(`(?i)\.(S\d{1,2}(E\d{1,3})?|Season\.?\d+|\d{4})\.`)
	if loc := re.FindStringIndex(rel); loc != nil {
		rel = rel[:loc[0]]
	}
	return strings.ReplaceAll(rel, ".", " ")
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

func (p *TVMazePlugin) lookup(query string) (*tvmazeShow, error) {
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
	s := results[0].Show
	return &s, nil
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
	query := extractShowName(job.rel)
	if query == "" {
		return
	}
	show, err := p.lookup(query)
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
