package plugin

import (
	"goftpd/plugins/sitebot/internal/event"
	tmpl "goftpd/plugins/sitebot/internal/template"
	"fmt"
	"log"
)

// AnnouncePlugin announces events to IRC
type AnnouncePlugin struct {
	templates map[string]*tmpl.Template
	debug     bool
}

// NewAnnouncePlugin creates announce plugin
func NewAnnouncePlugin() *AnnouncePlugin {
	return &AnnouncePlugin{
		templates: make(map[string]*tmpl.Template),
	}
}

// Name returns plugin name
func (p *AnnouncePlugin) Name() string {
	return "Announce"
}

// Initialize loads templates
func (p *AnnouncePlugin) Initialize(config map[string]interface{}) error {
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	
	// Templates would be loaded from config
	// For now, we'll create default ones
	return nil
}

// OnEvent handles events and returns formatted announcement
func (p *AnnouncePlugin) OnEvent(evt *event.Event) (string, error) {
	vars := p.eventToVars(evt)
	
	switch evt.Type {
	case event.EventUpload:
		return p.announceUpload(vars), nil
	case event.EventDownload:
		return p.announceDownload(vars), nil
	case event.EventNuke:
		return p.announceNuke(vars), nil
	default:
		return "", nil
	}
}

// Close does cleanup
func (p *AnnouncePlugin) Close() error {
	return nil
}

// eventToVars converts event to template variables
func (p *AnnouncePlugin) eventToVars(evt *event.Event) map[string]string {
	vars := make(map[string]string)
	
	vars["user"] = evt.User
	vars["group"] = evt.Group
	vars["section"] = evt.Section
	vars["filename"] = evt.Filename
	vars["path"] = evt.Path
	vars["size"] = fmt.Sprintf("%.2f MB", float64(evt.Size)/1024/1024)
	vars["speed"] = fmt.Sprintf("%.2f MB/s", evt.Speed)
	
	// Include custom data
	for k, v := range evt.Data {
		vars[k] = v
	}
	
	return vars
}

func (p *AnnouncePlugin) announceUpload(vars map[string]string) string {
	// Default template if none loaded
	return fmt.Sprintf("%s[%s] %s uploaded %s to %s (%s)",
		"\x0304", // Red
		vars["section"],
		vars["user"],
		vars["filename"],
		vars["path"],
		vars["speed"],
	)
}

func (p *AnnouncePlugin) announceDownload(vars map[string]string) string {
	return fmt.Sprintf("%s[%s] %s downloaded %s (%s)",
		"\x0302", // Blue
		vars["section"],
		vars["user"],
		vars["filename"],
		vars["speed"],
	)
}

func (p *AnnouncePlugin) announceNuke(vars map[string]string) string {
	return fmt.Sprintf("%s[%s] NUKED: %s - by %s",
		"\x0304", // Red
		vars["section"],
		vars["filename"],
		vars["user"],
	)
}
