package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SitenameLong  string       `yaml:"sitename_long"`
	SitenameShort string       `yaml:"sitename_short"`
	Version       string       `yaml:"version"`
	Email         string       `yaml:"email"`
	ListenPort    int          `yaml:"listen_port"`
	PublicIP      string       `yaml:"public_ip"`
	PasvMin       int          `yaml:"pasv_min"`
	PasvMax       int          `yaml:"pasv_max"`
	Mode          string       `yaml:"mode"` // "master" or "slave"
	Master        MasterConfig `yaml:"master"`
	Slave         SlaveConfig  `yaml:"slave"`
	StoragePath   string       `yaml:"storage_path"`
	TLSEnabled    bool         `yaml:"tls_enabled"`
	TLSCert       string       `yaml:"tls_cert"`
	TLSKey        string       `yaml:"tls_key"`
	Debug         bool         `yaml:"debug"`
}

type MasterConfig struct {
	ListenHost       string `yaml:"listen_host"`
	ControlPort      int    `yaml:"control_port"`
	HeartbeatTimeout int    `yaml:"heartbeat_timeout"`
	IndexCacheTTL    int    `yaml:"index_cache_ttl"`
}

type SlaveConfig struct {
	Name        string   `yaml:"name"`
	MasterHost  string   `yaml:"master_host"`
	MasterPort  int      `yaml:"master_port"`
	Roots       []string `yaml:"roots"`
	PasvPortMin int      `yaml:"pasv_port_min"`
	PasvPortMax int      `yaml:"pasv_port_max"`
	BindIP      string   `yaml:"bind_ip"`
	Timeout     int      `yaml:"timeout"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	if cfg.Mode != "master" && cfg.Mode != "slave" {
		cfg.Mode = "master"
	}
	if cfg.Master.ControlPort == 0 {
		cfg.Master.ControlPort = 1099
	}
	if cfg.Slave.MasterPort == 0 {
		cfg.Slave.MasterPort = 1099
	}
	if len(cfg.Slave.Roots) == 0 && cfg.StoragePath != "" {
		cfg.Slave.Roots = []string{cfg.StoragePath}
	}

	return &cfg, nil
}
