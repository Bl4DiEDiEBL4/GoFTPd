package plugin

import (
	"goftpd/plugins/sitebot/internal/event"
)

// Handler is the interface plugins must implement
type Handler interface {
	// Name returns the plugin name
	Name() string
	
	// Initialize is called on startup
	Initialize(config map[string]interface{}) error
	
	// OnEvent handles an FTP event
	OnEvent(evt *event.Event) (string, error)
	
	// Close is called on shutdown
	Close() error
}

// Manager manages plugins
type Manager struct {
	plugins map[string]Handler
	debug   bool
}

// NewManager creates a new plugin manager
func NewManager() *Manager {
	return &Manager{
		plugins: make(map[string]Handler),
	}
}

// Register registers a plugin
func (m *Manager) Register(plugin Handler) error {
	m.plugins[plugin.Name()] = plugin
	return nil
}

// Get gets a plugin by name
func (m *Manager) Get(name string) (Handler, bool) {
	p, ok := m.plugins[name]
	return p, ok
}

// List lists all registered plugins
func (m *Manager) List() []string {
	var names []string
	for name := range m.plugins {
		names = append(names, name)
	}
	return names
}

// ProcessEvent sends event to all plugins
func (m *Manager) ProcessEvent(evt *event.Event) ([]string, error) {
	var outputs []string
	
	for name, plugin := range m.plugins {
		output, err := plugin.OnEvent(evt)
		if err != nil {
			if m.debug {
				// Log but don't fail
			}
			continue
		}
		
		if output != "" {
			outputs = append(outputs, output)
		}
	}
	
	return outputs, nil
}

// Close closes all plugins
func (m *Manager) Close() error {
	for _, plugin := range m.plugins {
		plugin.Close()
	}
	return nil
}
