package core

import (
	"log"

	"goftpd/internal/plugin"
	"goftpd/internal/user"
)

// PluginManager manages all loaded plugins
type PluginManager struct {
	plugins []plugin.Plugin
	debug   bool
}

// NewPluginManager creates a new plugin manager
func NewPluginManager(debug bool) *PluginManager {
	return &PluginManager{
		plugins: make([]plugin.Plugin, 0),
		debug:   debug,
	}
}

// RegisterPlugin adds a plugin to the manager
func (pm *PluginManager) RegisterPlugin(p plugin.Plugin) error {
	pm.plugins = append(pm.plugins, p)
	if pm.debug {
		log.Printf("[PLUGIN-MANAGER] Registered plugin: %s", p.Name())
	}
	return nil
}

// InitializePlugins initializes all registered plugins with their configs
func (pm *PluginManager) InitializePlugins(pluginConfigs map[string]map[string]interface{}) error {
	for _, p := range pm.plugins {
		name := p.Name()
		config := pluginConfigs[name]
		if config == nil {
			config = make(map[string]interface{})
		}
		if err := p.Init(config); err != nil {
			if pm.debug {
				log.Printf("[PLUGIN-MANAGER] Failed to initialize %s: %v", name, err)
			}
			return err
		}
	}
	if pm.debug {
		log.Printf("[PLUGIN-MANAGER] Initialized %d plugins", len(pm.plugins))
	}
	return nil
}

// CallOnUpload calls OnUpload on all registered plugins
func (pm *PluginManager) CallOnUpload(userInterface interface{}, path, filename string, size int64, speed float64) error {
	u := userInterface.(*user.User)
	for _, p := range pm.plugins {
		if err := p.OnUpload(u, path, filename, size, speed); err != nil {
			if pm.debug {
				log.Printf("[PLUGIN-MANAGER] %s.OnUpload error: %v", p.Name(), err)
			}
		}
	}
	return nil
}

// CallOnDownload calls OnDownload on all registered plugins
func (pm *PluginManager) CallOnDownload(userInterface interface{}, path, filename string, size int64) error {
	u := userInterface.(*user.User)
	for _, p := range pm.plugins {
		if err := p.OnDownload(u, path, filename, size); err != nil {
			if pm.debug {
				log.Printf("[PLUGIN-MANAGER] %s.OnDownload error: %v", p.Name(), err)
			}
		}
	}
	return nil
}

// CallOnDirList calls OnDirList on all registered plugins
func (pm *PluginManager) CallOnDirList(userInterface interface{}, path string) (string, error) {
	u := userInterface.(*user.User)
	var output string
	for _, p := range pm.plugins {
		if result, err := p.OnDirList(u, path); err == nil && result != "" {
			output += result + "\n"
		}
	}
	return output, nil
}

// StopAll stops all plugins
func (pm *PluginManager) StopAll() error {
	for _, p := range pm.plugins {
		if err := p.Stop(); err != nil {
			log.Printf("[PLUGIN-MANAGER] %s.Stop error: %v", p.Name(), err)
		}
	}
	return nil
}

// GetPlugins returns all registered plugins
func (pm *PluginManager) GetPlugins() []plugin.Plugin {
	return pm.plugins
}
