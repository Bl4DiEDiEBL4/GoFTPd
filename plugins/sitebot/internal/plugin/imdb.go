package plugin

import (
	"goftpd/plugins/sitebot/internal/event"
	"fmt"
	"regexp"
	"strings"
)

// IMDBPlugin handles movie information announcements
type IMDBPlugin struct {
	enabled  bool
	template string
	debug    bool
}

// NewIMDBPlugin creates IMDB plugin
func NewIMDBPlugin() *IMDBPlugin {
	return &IMDBPlugin{
		template: "%b{MOVIE}%c4{:} %c3{%movie_name} %c1{(}%c3{%year}%c1{)} %c1{- Quality:} %c3{%quality}",
	}
}

// Name returns plugin name
func (p *IMDBPlugin) Name() string {
	return "IMDB"
}

// Initialize loads plugin config
func (p *IMDBPlugin) Initialize(config map[string]interface{}) error {
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	
	if enabled, ok := config["imdb_enabled"].(bool); ok {
		p.enabled = enabled
	} else {
		p.enabled = true
	}
	
	if tmpl, ok := config["imdb_template"].(string); ok {
		p.template = tmpl
	}
	
	return nil
}

// OnEvent handles events and looks for movie releases
func (p *IMDBPlugin) OnEvent(evt *event.Event) (string, error) {
	if !p.enabled || evt.Type != event.EventUpload {
		return "", nil
	}
	
	// Try to extract movie name and year from filename
	movieInfo := p.parseReleaseName(evt.Filename)
	if movieInfo == nil {
		return "", nil
	}
	
	// Build announcement using template
	vars := map[string]string{
		"movie_name": movieInfo["name"],
		"year":       movieInfo["year"],
		"quality":    movieInfo["quality"],
		"user":       evt.User,
		"section":    evt.Section,
	}
	
	// Simple template rendering
	output := p.template
	for k, v := range vars {
		output = strings.ReplaceAll(output, "%"+k, v)
	}
	
	return output, nil
}

// Close does cleanup
func (p *IMDBPlugin) Close() error {
	return nil
}

// parseReleaseName extracts movie name, year, quality from release name
// Supports: MovieName.2023.720p, MovieName.2023.1080p, etc
func (p *IMDBPlugin) parseReleaseName(filename string) map[string]string {
	// Pattern: MovieName.YYYY.quality
	// Common qualities: 720p, 1080p, 2160p, WEB-DL, BluRay, etc
	pattern := regexp.MustCompile(`^(.+?)\.(\d{4})\.([^.]+)`)
	
	matches := pattern.FindStringSubmatch(filename)
	if len(matches) >= 4 {
		movieName := strings.ReplaceAll(matches[1], ".", " ")
		year := matches[2]
		quality := matches[3]
		
		// Normalize quality
		quality = strings.ToUpper(quality)
		if strings.Contains(quality, "2160") {
			quality = "4K"
		} else if strings.Contains(quality, "1080") {
			quality = "1080p"
		} else if strings.Contains(quality, "720") {
			quality = "720p"
		}
		
		return map[string]string{
			"name":    movieName,
			"year":    year,
			"quality": quality,
		}
	}
	
	// Try simpler pattern: MovieName.quality
	simplePattern := regexp.MustCompile(`^(.+?)\.([0-9]{3,4}p|WEB-DL|BluRay|HDTV)`)
	matches = simplePattern.FindStringSubmatch(filename)
	if len(matches) >= 3 {
		movieName := strings.ReplaceAll(matches[1], ".", " ")
		quality := matches[2]
		
		return map[string]string{
			"name":    movieName,
			"year":    "????",
			"quality": quality,
		}
	}
	
	return nil
}
