package bot

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Private: source path + mutex for rehash.
	configPath string       `yaml:"-"`
	rehashMu   sync.RWMutex `yaml:"-"`

	Debug      bool           `yaml:"debug"`
	EventFIFO  string         `yaml:"event_fifo"`
	IRC        IRCConfig      `yaml:"irc"`
	Encryption EncConfig      `yaml:"encryption"`
	Announce   AnnounceConfig `yaml:"announce"`
	Sections   []SectionRoute `yaml:"sections"`
	Plugins    PluginsConfig  `yaml:"plugins"`
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
	if cfg.Announce.ThemeFile == "" {
		cfg.Announce.ThemeFile = "./etc/templates/pzsng.theme"
	}
	cfg.configPath = path
	return cfg, nil
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
