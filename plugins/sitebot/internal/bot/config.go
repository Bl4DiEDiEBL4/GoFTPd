package bot

import (
	"os"
	"gopkg.in/yaml.v3"
)

// Config represents bot configuration
type Config struct {
	Debug       bool       `yaml:"debug"`
	EventFIFO   string     `yaml:"event_fifo"`
	IRC         IRCConfig  `yaml:"irc"`
	Encryption  EncConfig  `yaml:"encryption"`
	Plugins     PlugConfig `yaml:"plugins"`
}

// IRCConfig is IRC settings
type IRCConfig struct {
	Host     string   `yaml:"host"`
	Port     int      `yaml:"port"`
	Nick     string   `yaml:"nick"`
	User     string   `yaml:"user"`
	RealName string   `yaml:"realname"`
	Password string   `yaml:"password"` // IRC server password (optional)
	Channels []string `yaml:"channels"`
}

// EncConfig is encryption settings
type EncConfig struct {
	Enabled bool              `yaml:"enabled"`
	Keys    map[string]string `yaml:"keys"` // channel -> key
}

// PlugConfig is plugin settings
type PlugConfig struct {
	Enabled map[string]bool   `yaml:"enabled"`
	Config  map[string]interface{} `yaml:"config"`
}

// LoadConfig loads config from YAML file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	
	return cfg, nil
}

// SaveConfig saves config to YAML file
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	
	return os.WriteFile(path, data, 0644)
}
