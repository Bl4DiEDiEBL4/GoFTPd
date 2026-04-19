package core

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
)

// MetaLookup handles asynchronous TVMaze / IMDB lookups when a release dir
// is created. On success it writes a `.tvmaze` or `.imdb` meta file into the
// release directory via the master bridge. Those files are then shown on
// CWD via the show_diz mechanism (add `.tvmaze: "*"` and `.imdb: "*"` to
// show_diz in the main config).
type MetaLookup struct {
	debug      bool
	bridge     MasterBridge
	client     *http.Client
	tvSections []string
	imSections []string
	jobs       chan metaJob
	seen       map[string]bool
	seenMu     sync.Mutex
	startOnce  sync.Once
}

type metaJob struct {
	kind    string // "tv" or "imdb"
	dirPath string
	relname string
	section string
}

// NewMetaLookup creates a meta-lookup helper. bridge is used to write the
// output file to the slave; if nil the lookup is skipped.
func NewMetaLookup(bridge MasterBridge, debug bool, tvSections, imSections []string) *MetaLookup {
	if len(tvSections) == 0 {
		tvSections = []string{"TV"}
	}
	if len(imSections) == 0 {
		imSections = []string{"MOVIE", "X264", "X265", "BLURAY", "DVDR"}
	}
	return &MetaLookup{
		debug:      debug,
		bridge:     bridge,
		client:     &http.Client{Timeout: 10 * time.Second},
		tvSections: tvSections,
		imSections: imSections,
		jobs:       make(chan metaJob, 128),
		seen:       map[string]bool{},
	}
}

// OnMKDir is called from the MKD handler. It classifies the dir (TV or movie)
// and enqueues a background lookup. Non-blocking: returns immediately.
func (m *MetaLookup) OnMKDir(dirPath string) {
	if m == nil || m.bridge == nil {
		return
	}
	relname := path.Base(path.Clean(dirPath))
	if relname == "" || relname == "." || relname == "/" {
		return
	}
	section := sectionFromPath(dirPath)
	if section == "" || section == "DEFAULT" {
		return
	}

	kind := ""
	switch {
	case m.matchSection(section, m.tvSections) && isTVReleaseName(relname):
		kind = "tv"
	case m.matchSection(section, m.imSections) && isMovieReleaseName(relname):
		kind = "imdb"
	default:
		return
	}

	// Dedupe — don't re-lookup the same release within a session.
	m.seenMu.Lock()
	if m.seen[relname] {
		m.seenMu.Unlock()
		return
	}
	m.seen[relname] = true
	if len(m.seen) > 2000 {
		m.seen = map[string]bool{relname: true}
	}
	m.seenMu.Unlock()

	m.startWorker()
	select {
	case m.jobs <- metaJob{kind: kind, dirPath: dirPath, relname: relname, section: section}:
	default:
		if m.debug {
			log.Printf("[META] queue full, dropping lookup for %q", relname)
		}
	}
}

func (m *MetaLookup) startWorker() {
	m.startOnce.Do(func() {
		go func() {
			for job := range m.jobs {
				switch job.kind {
				case "tv":
					m.doTVMazeLookup(job)
				case "imdb":
					m.doIMDBLookup(job)
				}
			}
		}()
	})
}

func (m *MetaLookup) matchSection(section string, allowed []string) bool {
	up := strings.ToUpper(section)
	for _, s := range allowed {
		if strings.Contains(up, strings.ToUpper(s)) {
			return true
		}
	}
	return false
}

// sectionFromPath is provided by events.go (same package).

// isTVReleaseName checks for scene TV naming: dots, dashes, and SxxEyy or
// year tag. Skips Sample/Proof/Subs subfolders.
func isTVReleaseName(rel string) bool {
	if !strings.Contains(rel, ".") || !strings.Contains(rel, "-") {
		return false
	}
	re := regexp.MustCompile(`(?i)(^|\.)(S\d{1,2}(E\d{1,3})?|Season\.?\d+|\d{4})(\.|$)`)
	return re.MatchString(rel)
}

// isMovieReleaseName checks for scene movie naming: dots, dashes, and a
// 4-digit year between dots. Skips Sample/Proof/Subs/CDn subfolders.
func isMovieReleaseName(rel string) bool {
	if !strings.Contains(rel, ".") || !strings.Contains(rel, "-") {
		return false
	}
	return regexp.MustCompile(`\.(19|20)\d{2}\.`).MatchString(rel)
}

// =============================================================================
// TVMaze
// =============================================================================

type tvmSearchResult struct {
	Show tvmShow `json:"show"`
}

type tvmShow struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Language string   `json:"language"`
	Genres   []string `json:"genres"`
	Type     string   `json:"type"`
	Status   string   `json:"status"`
	Premiered string  `json:"premiered"`
	Rating   struct {
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
	Summary string `json:"summary"`
	Externals struct {
		IMDB string `json:"imdb"`
	} `json:"externals"`
	Embedded struct {
		Episodes []tvmEpisode `json:"episodes"`
	} `json:"_embedded"`
}

type tvmEpisode struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Season   int    `json:"season"`
	Number   int    `json:"number"`
	Airdate  string `json:"airdate"`
	URL      string `json:"url"`
	Summary  string `json:"summary"`
}

func (m *MetaLookup) doTVMazeLookup(job metaJob) {
	title, season, episode := parseTVName(job.relname)
	if title == "" {
		return
	}

	// Search for the show (with embedded episodes)
	q := url.QueryEscape(title)
	searchURL := fmt.Sprintf("https://api.tvmaze.com/singlesearch/shows?q=%s&embed=episodes", q)
	resp, err := m.client.Get(searchURL)
	if err != nil {
		if m.debug {
			log.Printf("[META/TV] search %q failed: %v", title, err)
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
		if m.debug {
			log.Printf("[META/TV] parse %q failed: %v", title, err)
		}
		return
	}
	if show.ID == 0 {
		return
	}

	// Find matching episode if SxxEyy was in release name
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
	filePath := path.Join(job.dirPath, ".tvmaze")
	if err := m.bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[META/TV] WriteFile %s failed: %v", filePath, err)
		return
	}
	if m.debug {
		log.Printf("[META/TV] Wrote .tvmaze for %s", job.relname)
	}
}

// parseTVName extracts show name, season, and episode from a scene release name.
// e.g. "Kill.Blue.S01E02.1080p.WEB.H264-SKYANiME" -> ("Kill Blue", 1, 2)
func parseTVName(rel string) (string, int, int) {
	// Strip -GROUP suffix
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
	// Fallback: strip year/season tag and return the title
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

	epTitle := ""
	epLink := ""
	epAirdate := ""
	if ep != nil {
		epTitle = ep.Name
		epLink = ep.URL
		epAirdate = ep.Airdate
	}

	fmt.Fprintf(&b, " Title........: %s\n", show.Name)
	if epTitle != "" {
		fmt.Fprintf(&b, " Episode......: S%02dE%02d - %s\n", ep.Season, ep.Number, epTitle)
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
	if epLink != "" {
		fmt.Fprintf(&b, " Episode Link.: %s\n", epLink)
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
	if epAirdate != "" {
		fmt.Fprintf(&b, " Airdate......: %s\n", epAirdate)
	}
	fmt.Fprintf(&b, " -\n")

	// Plot — prefer episode summary over show summary, wrap at 70 chars
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
	fmt.Fprintf(&b, " Plot.........: %s\n", wrapPlot(plot, 70, " "))
	fmt.Fprintf(&b, "\n%s\n", bar)
	return b.String()
}

// =============================================================================
// IMDB (via imdbapi.dev)
// =============================================================================

type imdbSearchDaemon struct {
	Titles []imdbTitleDaemon `json:"titles"`
}

type imdbTitleDaemon struct {
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

func (m *MetaLookup) doIMDBLookup(job metaJob) {
	title, year := parseMovieName(job.relname)
	if title == "" {
		return
	}

	searchURL := "https://api.imdbapi.dev/search/titles?query=" + url.QueryEscape(title)
	resp, err := m.client.Get(searchURL)
	if err != nil {
		if m.debug {
			log.Printf("[META/IMDB] search %q failed: %v", title, err)
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
	var sr imdbSearchDaemon
	if err := json.Unmarshal(body, &sr); err != nil {
		return
	}
	if len(sr.Titles) == 0 {
		return
	}

	// Pick best match (prefer exact year + movie type)
	var best *imdbTitleDaemon
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

	// Fetch full details
	detailURL := "https://api.imdbapi.dev/titles/" + url.PathEscape(best.ID)
	resp2, err := m.client.Get(detailURL)
	if err == nil && resp2.StatusCode == 200 {
		if b2, err := io.ReadAll(resp2.Body); err == nil {
			var full imdbTitleDaemon
			if err := json.Unmarshal(b2, &full); err == nil {
				best = &full
			}
		}
		resp2.Body.Close()
	} else if resp2 != nil {
		resp2.Body.Close()
	}

	content := formatIMDBFile(best)
	filePath := path.Join(job.dirPath, ".imdb")
	if err := m.bridge.WriteFile(filePath, []byte(content)); err != nil {
		log.Printf("[META/IMDB] WriteFile %s failed: %v", filePath, err)
		return
	}
	if m.debug {
		log.Printf("[META/IMDB] Wrote .imdb for %s", job.relname)
	}
}

// parseMovieName extracts title and year from a scene movie name.
// e.g. "The.Matrix.1999.1080p.BluRay.x264-GROUP" -> ("The Matrix", 1999)
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

func formatIMDBFile(t *imdbTitleDaemon) string {
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
	fmt.Fprintf(&b, " Plot.........: %s\n", wrapPlot(plot, 70, " "))
	fmt.Fprintf(&b, "\n%s\n", bar)
	return b.String()
}

// =============================================================================
// Helpers
// =============================================================================

// stripHTML removes HTML tags and decodes a few common entities.
func stripHTML(s string) string {
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.TrimSpace(s)
}

// wrapPlot word-wraps s to width, with the given indent on continuation lines.
func wrapPlot(s string, width int, indent string) string {
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
				out.WriteString("\n")
				out.WriteString(indent)
				out.WriteString("              ")
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
			out.WriteString("\n")
			out.WriteString(indent)
			out.WriteString("              ")
			out.WriteString(line)
		}
	}
	return out.String()
}
