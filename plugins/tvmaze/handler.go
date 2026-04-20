// Package tvmaze is a goftpd plugin that looks up TV show metadata on
// TVMaze when a release directory is created, and writes a .tvmaze file
// into the release dir for display via show_diz.
//
// Config (in the main goftpd config.yml under plugins.tvmaze):
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

	"goftpd/internal/plugin"
)

// Handler is the tvmaze plugin. One instance per goftpd process.
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
	go h.worker()
	if h.debug {
		log.Printf("[TVMAZE] initialized, sections=%v", h.sections)
	}
	return nil
}

// OnEvent is the hot path — must return fast. We only care about MKDIR.
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
	if !isTVReleaseName(evt.Filename) {
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

	select {
	case h.jobs <- job{dirPath: evt.Path, relname: evt.Filename, section: evt.Section}:
	default:
		if h.debug {
			log.Printf("[TVMAZE] job queue full, dropping %s", evt.Filename)
		}
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

func (h *Handler) doLookup(j job) {
	title, season, episode := parseTVName(j.relname)
	if title == "" {
		return
	}

	q := url.QueryEscape(title)
	searchURL := fmt.Sprintf("https://api.tvmaze.com/singlesearch/shows?q=%s&embed=episodes", q)
	resp, err := h.client.Get(searchURL)
	if err != nil {
		if h.debug {
			log.Printf("[TVMAZE] search %q failed: %v", title, err)
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
	var show tvmShow
	if err := json.Unmarshal(body, &show); err != nil {
		if h.debug {
			log.Printf("[TVMAZE] parse %q failed: %v", title, err)
		}
		return
	}
	if show.ID == 0 {
		return
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

	content := formatTVMazeFile(&show, ep)
	filePath := path.Join(j.dirPath, ".tvmaze")
	if err := h.svc.Bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[TVMAZE] WriteFile %s failed: %v", filePath, err)
		return
	}
	if h.debug {
		log.Printf("[TVMAZE] Wrote .tvmaze for %s", j.relname)
	}
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
	if idx := strings.LastIndex(rel, "-"); idx > 0 {
		rel = rel[:idx]
	}
	re := regexp.MustCompile(`(?i)^(.+?)\.S(\d{1,2})E(\d{1,3})\.`)
	if m := re.FindStringSubmatch(rel); m != nil {
		var s, e int
		fmt.Sscanf(m[2], "%d", &s)
		fmt.Sscanf(m[3], "%d", &e)
		return strings.ReplaceAll(m[1], ".", " "), s, e
	}
	re2 := regexp.MustCompile(`(?i)^(.+?)\.(S\d{1,2}|Season\.?\d+|\d{4})\.`)
	if m := re2.FindStringSubmatch(rel); m != nil {
		return strings.ReplaceAll(m[1], ".", " "), 0, 0
	}
	return strings.ReplaceAll(rel, ".", " "), 0, 0
}

func formatTVMazeFile(show *tvmShow, ep *tvmEpisode) string {
	var b strings.Builder
	const bar = "========================= TVMAZE INFO v1.0 ========================="
	fmt.Fprintf(&b, "%s\n\n", bar)

	fmt.Fprintf(&b, " Title........: %s\n", show.Name)
	if ep != nil {
		fmt.Fprintf(&b, " Episode......: S%02dE%02d - %s\n", ep.Season, ep.Number, ep.Name)
	}
	premiered := show.Premiered
	if len(premiered) >= 4 {
		premiered = premiered[:4]
	}
	if premiered != "" {
		fmt.Fprintf(&b, " Premiered....: %s\n", premiered)
	}
	fmt.Fprintf(&b, " -\n")

	if show.Externals.IMDB != "" {
		fmt.Fprintf(&b, " IMDB Link....: https://www.imdb.com/title/%s/\n", show.Externals.IMDB)
	} else {
		fmt.Fprintf(&b, " IMDB Link....: NA\n")
	}
	if show.URL != "" {
		fmt.Fprintf(&b, " TVMaze Link..: %s\n", show.URL)
	}
	if ep != nil && ep.URL != "" {
		fmt.Fprintf(&b, " Episode Link.: %s\n", ep.URL)
	}
	if len(show.Genres) > 0 {
		fmt.Fprintf(&b, " Genre........: %s\n", strings.Join(show.Genres, ", "))
	}
	if show.Type != "" {
		fmt.Fprintf(&b, " Type.........: %s\n", show.Type)
	}
	rating := "NA"
	if show.Rating.Average > 0 {
		rating = fmt.Sprintf("%.1f", show.Rating.Average)
	}
	fmt.Fprintf(&b, " User Rating..: %s\n", rating)
	fmt.Fprintf(&b, " -\n")

	country := show.Network.Country.Code
	if country == "" {
		country = "NA"
	}
	fmt.Fprintf(&b, " Country......: %s\n", country)
	language := show.Language
	if language == "" {
		language = "NA"
	}
	fmt.Fprintf(&b, " Language.....: %s\n", language)
	network := show.Network.Name
	if network == "" {
		network = show.WebChannel.Name
	}
	if network == "" {
		network = "NA"
	}
	fmt.Fprintf(&b, " Network......: %s\n", network)
	status := show.Status
	if status == "" {
		status = "NA"
	}
	fmt.Fprintf(&b, " Status.......: %s\n", status)
	if ep != nil && ep.Airdate != "" {
		fmt.Fprintf(&b, " Airdate......: %s\n", ep.Airdate)
	}
	fmt.Fprintf(&b, " -\n")

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
	fmt.Fprintf(&b, " Plot.........: %s\n", wrapPlot(plot, 70))
	fmt.Fprintf(&b, "\n%s\n", bar)
	return b.String()
}

// =============================================================================
// Helpers (kept local so each plugin is self-contained)
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
