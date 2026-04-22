package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	SitenameLong  string        `yaml:"sitename_long"`
	SitenameShort string        `yaml:"sitename_short"`
	Version       string        `yaml:"version"`
	Email         string        `yaml:"email"`
	ListenPort    int           `yaml:"listen_port"`
	PublicIP      string        `yaml:"public_ip"`
	PasvMin       int           `yaml:"pasv_min"`
	PasvMax       int           `yaml:"pasv_max"`
	Mode          string        `yaml:"mode"` // "master" or "slave"
	Master        MasterConfig  `yaml:"master"`
	Slave         SlaveConfig   `yaml:"slave"`
	Slaves        []SlavePolicy `yaml:"slaves"` // per-slave routing policy (master side)
	StoragePath   string        `yaml:"storage_path"`
	TLSEnabled    bool          `yaml:"tls_enabled"`
	TLSCert       string        `yaml:"tls_cert"`
	TLSKey        string        `yaml:"tls_key"`
	Debug         bool          `yaml:"debug"`
}

// SlavePolicy defines routing rules for a slave. Configured on the master.
// Sections and Paths restrict which uploads can go to this slave.
// Weight biases the load balancer (higher weight = more uploads sent here).
// If Sections is empty and Paths is empty, the slave accepts all uploads.
type SlavePolicy struct {
	Name     string   `yaml:"name"`     // must match the slave's registered name
	Sections []string `yaml:"sections"` // e.g. ["TV-1080P", "MP3"] — case-insensitive
	Paths    []string `yaml:"paths"`    // e.g. ["/TV-1080P/*", "/MOVIES/*"]
	Weight   int      `yaml:"weight"`   // default 1; higher = more traffic
	ReadOnly bool     `yaml:"readonly"` // true = scan/download only; never route uploads here
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
