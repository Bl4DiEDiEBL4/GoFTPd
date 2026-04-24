package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Private: source path + mutex for rehash.
	configPath string       `yaml:"-"`
	rehashMu   sync.RWMutex `yaml:"-"`

	Debug              bool           `yaml:"debug"`
	LogFile            string         `yaml:"log_file"`
	LogKeepDays        int            `yaml:"log_keep_days"`
	LogDeleteAfterDays int            `yaml:"log_delete_after_days"`
	LogConsole         bool           `yaml:"log_console"`
	EventFIFO          string         `yaml:"event_fifo"`
	IRC                IRCConfig      `yaml:"irc"`
	Encryption         EncConfig      `yaml:"encryption"`
	Announce           AnnounceConfig `yaml:"announce"`
	Sections           []SectionRoute `yaml:"sections"`
	Plugins            PluginsConfig  `yaml:"plugins"`
}

type IRCConfig struct {
	Host          string   `yaml:"host"`
	Port          int      `yaml:"port"`
	SSL           bool     `yaml:"ssl"`
	Nick          string   `yaml:"nick"`
	User          string   `yaml:"user"`
	RealName      string   `yaml:"realname"`
	Password      string   `yaml:"password"`
	Channels      []string `yaml:"channels"`
	OperUser      string   `yaml:"operuser"`
	OperPassword  string   `yaml:"operpassword"`
	AutoOper      bool     `yaml:"auto_oper"`
	UserModes     string   `yaml:"user_modes"`
	AutoJoinDelay int      `yaml:"autojoin_delay_ms"`
}

type EncConfig struct {
	Enabled bool              `yaml:"enabled"`
	Keys    map[string]string `yaml:"keys"`
}

type AnnounceConfig struct {
	ConfigFile     string              `yaml:"config_file"`
	DefaultChannel string              `yaml:"default_channel"`
	TypeRoutes     map[string][]string `yaml:"type_routes"`
	ThemeFile      string              `yaml:"theme_file"`
}

type SectionRoute struct {
	Name     string   `yaml:"name"`
	Channels []string `yaml:"channels"`
	Paths    []string `yaml:"paths"`
}

type PluginsConfig struct {
	Enabled map[string]bool        `yaml:"enabled"`
	Config  map[string]interface{} `yaml:"config"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := resolveAnnounceConfigFile(&cfg.Announce, filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := resolvePluginConfigFiles(cfg.Plugins.Config, filepath.Dir(path)); err != nil {
		return nil, err
	}
	if cfg.Announce.TypeRoutes == nil {
		cfg.Announce.TypeRoutes = map[string][]string{}
	}
	if cfg.Encryption.Keys == nil {
		cfg.Encryption.Keys = map[string]string{}
	}
	if cfg.Plugins.Enabled == nil {
		cfg.Plugins.Enabled = map[string]bool{}
	}
	if cfg.Plugins.Config == nil {
		cfg.Plugins.Config = map[string]interface{}{}
	}
	if !cfg.LogConsole {
		// default false value from yaml means either unset or explicit false.
		// Keep console logging enabled by default unless user set log_console.
		raw := map[string]interface{}{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			if _, ok := raw["log_console"]; !ok {
				cfg.LogConsole = true
			}
		} else {
			cfg.LogConsole = true
		}
	}
	if cfg.LogDeleteAfterDays <= 0 && cfg.LogKeepDays > 0 {
		cfg.LogDeleteAfterDays = cfg.LogKeepDays
	}
	if cfg.Announce.ThemeFile == "" {
		cfg.Announce.ThemeFile = "./etc/templates/pzsng.theme"
	}
	cfg.configPath = path
	return cfg, nil
}

func resolveAnnounceConfigFile(cfg *AnnounceConfig, baseDir string) error {
	configFile := strings.TrimSpace(cfg.ConfigFile)
	if configFile == "" {
		return nil
	}
	loaded := AnnounceConfig{}
	data, err := loadConfigFileMap(configFile, baseDir)
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	raw, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, &loaded); err != nil {
		return err
	}
	if cfg.DefaultChannel != "" {
		loaded.DefaultChannel = cfg.DefaultChannel
	}
	if cfg.ThemeFile != "" {
		loaded.ThemeFile = cfg.ThemeFile
	}
	if len(cfg.TypeRoutes) > 0 {
		loaded.TypeRoutes = cfg.TypeRoutes
	}
	loaded.ConfigFile = cfg.ConfigFile
	*cfg = loaded
	return nil
}

func resolvePluginConfigFiles(config map[string]interface{}, baseDir string) error {
	for name, raw := range config {
		section := sectionAsStringMap(raw)
		if len(section) == 0 {
			continue
		}
		configFile, _ := section["config_file"].(string)
		configFile = strings.TrimSpace(configFile)
		if configFile == "" {
			continue
		}
		loaded, err := loadConfigFileMap(configFile, baseDir)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", name, err)
		}
		merged := map[string]interface{}{}
		for k, v := range loaded {
			merged[k] = v
		}
		for k, v := range section {
			merged[k] = v
		}
		config[name] = merged
		if _, ok := raw.(map[string]interface{}); !ok {
			config[name] = merged
		}
	}
	return nil
}

func loadConfigFileMap(path, baseDir string) (map[string]interface{}, error) {
	resolved := path
	checked := []string{}
	if !filepath.IsAbs(path) {
		candidates := []string{
			filepath.Clean(path),
			filepath.Clean(filepath.Join(baseDir, path)),
			filepath.Clean(filepath.Join(filepath.Dir(baseDir), path)),
		}
		for _, candidate := range candidates {
			checked = append(checked, candidate)
			if _, err := os.Stat(candidate); err == nil {
				resolved = candidate
				break
			}
		}
	} else {
		checked = append(checked, filepath.Clean(path))
	}
	data, err := os.ReadFile(filepath.Clean(resolved))
	if err != nil {
		return nil, fmt.Errorf("config_file %q not found; check config", path)
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func sectionAsStringMap(raw interface{}) map[string]interface{} {
	if section, ok := raw.(map[string]interface{}); ok {
		return section
	}
	if section, ok := raw.(map[interface{}]interface{}); ok {
		out := map[string]interface{}{}
		for k, v := range section {
			if key, ok := k.(string); ok {
				out[key] = v
			}
		}
		return out
	}
	return map[string]interface{}{}
}

// Rehash reloads the config from disk and swaps in fields that are safe
// to change while running. Host/port/SSL/nick/password in `IRC` are NOT
// reloaded — the IRC socket is already established; changing those would
// require a full reconnect. Channels, encryption keys, themes, sections,
// announce routing, and plugin settings all ARE reloaded.
func (c *Config) Rehash() (string, error) {
	if c.configPath == "" {
		return "", fmt.Errorf("no config path recorded; was this loaded via LoadConfig?")
	}
	fresh, err := LoadConfig(c.configPath)
	if err != nil {
		return c.configPath, err
	}

	c.rehashMu.Lock()
	defer c.rehashMu.Unlock()

	c.Debug = fresh.Debug
	c.LogFile = fresh.LogFile
	c.LogKeepDays = fresh.LogKeepDays
	c.LogDeleteAfterDays = fresh.LogDeleteAfterDays
	c.LogConsole = fresh.LogConsole
	// EventFIFO path change requires restart (reader goroutine has it open)
	// IRC connection details: keep old. Only refresh auto-join list + modes.
	c.IRC.Channels = fresh.IRC.Channels
	c.IRC.UserModes = fresh.IRC.UserModes
	c.IRC.AutoJoinDelay = fresh.IRC.AutoJoinDelay
	c.IRC.AutoOper = fresh.IRC.AutoOper
	c.IRC.OperUser = fresh.IRC.OperUser
	c.IRC.OperPassword = fresh.IRC.OperPassword

	c.Encryption = fresh.Encryption
	c.Announce = fresh.Announce
	c.Sections = fresh.Sections
	c.Plugins = fresh.Plugins
	return c.configPath, nil
}
