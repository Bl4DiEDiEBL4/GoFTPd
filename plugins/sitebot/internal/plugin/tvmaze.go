package plugin

import (
	"goftpd/plugins/sitebot/internal/event"
	"fmt"
	"regexp"
	"strings"
)

// TVMazePlugin handles TV show information announcements
type TVMazePlugin struct {
	enabled bool
	apiKey  string
	template string
	debug   bool
}

// NewTVMazePlugin creates TVMaze plugin
func NewTVMazePlugin() *TVMazePlugin {
	return &TVMazePlugin{
		template: "%b{TV}%c2{:} %c3{%show_name} %c1{- Episode:} %c3{%episode_number} %c1{- Aired:} %c3{%airdate}",
	}
}

// Name returns plugin name
func (p *TVMazePlugin) Name() string {
	return "TVMaze"
}

// Initialize loads plugin config
func (p *TVMazePlugin) Initialize(config map[string]interface{}) error {
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	
	if enabled, ok := config["tvmaze_enabled"].(bool); ok {
		p.enabled = enabled
	} else {
		p.enabled = true
	}
	
	if apiKey, ok := config["tvmaze_api_key"].(string); ok {
		p.apiKey = apiKey
	}
	
	if tmpl, ok := config["tvmaze_template"].(string); ok {
		p.template = tmpl
	}
	
	return nil
}

// OnEvent handles events and looks for TV show releases
func (p *TVMazePlugin) OnEvent(evt *event.Event) (string, error) {
	if !p.enabled || evt.Type != event.EventUpload {
		return "", nil
	}
	
	// Try to extract show name and episode from filename
	showInfo := p.parseReleaseName(evt.Filename)
	if showInfo == nil {
		return "", nil
	}
	
	// Build announcement using template
	vars := map[string]string{
		"show_name":      showInfo["show"],
		"episode_number": showInfo["episode"],
		"airdate":        showInfo["date"],
		"user":           evt.User,
		"section":        evt.Section,
	}
	
	// Simple template rendering
	output := p.template
	for k, v := range vars {
		output = strings.ReplaceAll(output, "%"+k, v)
	}
	
	return output, nil
}

// Close does cleanup
func (p *TVMazePlugin) Close() error {
	return nil
}

// parseReleaseName extracts show name, season, episode from release name
// Supports: ShowName.S01E05.720p, ShowName.1x05.720p, etc
func (p *TVMazePlugin) parseReleaseName(filename string) map[string]string {
	// Pattern: ShowName.SnnEnn or ShowName.nnxnn
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^(.+?)\.S(\d{2})E(\d{2})`),        // ShowName.S01E05
		regexp.MustCompile(`^(.+?)\.(\d{1,2})x(\d{2})`),       // ShowName.1x05
		regexp.MustCompile(`^(.+?)\.(\d{1,2})(\d{2})\.\d+p`), // ShowName.0105.720p
	}
	
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(filename)
		if len(matches) >= 4 {
			showName := strings.ReplaceAll(matches[1], ".", " ")
			season := matches[2]
			episode := matches[3]
			
			return map[string]string{
				"show":    showName,
				"season":  season,
				"episode": fmt.Sprintf("S%sE%s", season, episode),
				"date":    "N/A", // Would fetch from API
			}
		}
	}
	
	return nil
}
