package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"goftpd/internal/timeutil"
	"goftpd/internal/zipscript"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// Private: path we loaded from, and a mutex protecting rehash swaps.
	configPath string       `yaml:"-"`
	rehashMu   sync.RWMutex `yaml:"-"`

	// Server Identity
	SiteName      string `yaml:"sitename_long"`
	SiteNameShort string `yaml:"sitename_short"`
	Version       string `yaml:"version"`
	Email         string `yaml:"email"`
	LoginPrompt   string `yaml:"login_prompt"`
	Timezone      string `yaml:"timezone"`

	// Network
	ListenPort int    `yaml:"listen_port"`
	PublicIP   string `yaml:"public_ip"`
	PasvMin    int    `yaml:"pasv_min"`
	PasvMax    int    `yaml:"pasv_max"`

	// Daemon Mode

	// Master/Slave Mode (master or slave)
	Mode   string                 `yaml:"mode"`
	Master map[string]interface{} `yaml:"master"`
	Slave  map[string]interface{} `yaml:"slave"`
	Slaves []SlavePolicyConfig    `yaml:"slaves"` // per-slave routing/affinity rules (master mode)

	// Sections are virtual section directories the master should keep alive in
	// VFS and create on matching writable slaves. Nested dirs such as
	// "/FOREIGN/TV-NL" are supported.
	Sections []string `yaml:"sections"`

	// InviteChannels maps channel names to required user flags. Channels
	// not listed here are considered public and returned to every user
	// regardless of flags. A user needs at least ONE of the listed flags.
	// Example:
	//   invite_channels:
	//     - channel: "#goftpd-staff"
	//       flags: "1"         # siteop only
	//     - channel: "#goftpd-nuke"
	//       flags: "12"        # siteop or group admin
	InviteChannels []InviteRule `yaml:"invite_channels"`

	// SitebotConfig is the path to the sitebot's config.yml. Used by
	// SITE INVITE to read the announce channels from the sitebot config
	// (single source of truth).
	SitebotConfig string `yaml:"sitebot_config"`

	// Storage & Paths
	StoragePath string `yaml:"storage_path"`
	RootPath    string `yaml:"rootpath"`
	DataPath    string `yaml:"datapath"`
	ACLBasePath string `yaml:"acl_base_path"`
	PasswdFile  string `yaml:"passwd_file"`
	ShellPath   string `yaml:"shell_path"`
	MsgPath     string `yaml:"msg_path"`

	// TLS/SSL
	TLSEnabled       bool   `yaml:"tls_enabled"`
	TLSCert          string `yaml:"tls_cert"`
	TLSKey           string `yaml:"tls_key"`
	TLSMinProto      string `yaml:"tls_min_protocol"`
	TLSMaxProto      string `yaml:"tls_max_protocol"`
	SSLCleanShutdown bool   `yaml:"ssl_clean_shutdown"`

	// Security Policies
	Shutdown           int    `yaml:"shutdown"`
	UserRejectSecure   string `yaml:"userrejectsecure"`
	UserRejectInsecure string `yaml:"userrejectinsecure"`
	DenyDirEncrypted   string `yaml:"denydiruncrypted"`
	DenyDataEncrypted  string `yaml:"denydatauncrypted"`

	// User Limits
	MaxConnections int  `yaml:"max_connections"` // Global max concurrent connections
	Passthrough    bool `yaml:"passthrough"`     // Direct client→slave transfers (drftpd-style)
	MaxUsers       int  `yaml:"max_users"`
	MaxUsersPerIP  int  `yaml:"max_users_per_ip"`
	TotalUsers     int  `yaml:"total_users"`

	// File Rules
	FileNamesLower bool              `yaml:"file_names_lowercase"`
	DirNamesLower  bool              `yaml:"dir_names_lowercase"`
	AllowASCII     []string          `yaml:"allow_ascii_uploads"`
	HiddenFiles    []string          `yaml:"hidden_files"`
	ShowDiz        map[string]string `yaml:"show_diz"`      // filename -> who_can_see
	ShowSymlinks   bool              `yaml:"show_symlinks"` // Show symlinks in LIST
	SFVCheck       bool              `yaml:"sfv_check"`

	// Dupe Checking
	DupeCheck    bool     `yaml:"dupe_check"`
	DupeDB       string   `yaml:"dupe_db_path"`
	XdupeEnabled bool     `yaml:"xdupe_enabled"`
	XdupeDBPath  string   `yaml:"xdupe_db_path"`
	XdupeExts    []string `yaml:"xdupe_extensions"`

	// Download & Credits
	DLIncomplete bool `yaml:"dl_incomplete"`
	CreditLoss   int  `yaml:"credit_loss"`
	CreditCheck  bool `yaml:"creditcheck"`
	FreeSpaceMB  int  `yaml:"free_space_mb"`

	// Display
	DisplaySize   string `yaml:"display_size_unit"`
	DisplaySpeed  string `yaml:"display_speed_unit"`
	ColorMode     int    `yaml:"color_mode"`
	ShowCWDBanner bool   `yaml:"show_cwd_banner"`
	// Nuke
	NukeMaxMultiplier int              `yaml:"nuke_max_multiplier"`
	NukeDirStyle      string           `yaml:"nukedir_style"`
	Zipscript         zipscript.Config `yaml:"zipscript"`

	// Plugins
	Plugins         map[string]map[string]interface{} `yaml:"plugins"`
	PluginManager   *PluginManager                    `yaml:"-"` // Set at runtime
	EventFIFO       string                            `yaml:"event_fifo"`
	EventDispatcher *EventDispatcher                  `yaml:"-"` // Set at runtime
	MasterManager   interface{}                       `yaml:"-"` // *master.Manager for master mode
	RehashHook      func(*Config)                     `yaml:"-"` // called after Rehash() swaps fields
	ACLRehashHook   func(*Config) error               `yaml:"-"` // reloads ACL-backed state after Rehash()

	// Debug
	Debug bool `yaml:"debug"`

	// Log file. If LogFile is set AND Debug is true, log output is tee'd to
	// both stderr and the given file, with daily rotation. Rotated files
	// (<LogFile>.YYYY-MM-DD) older than LogKeepDays are deleted. Default
	// LogKeepDays = 1 (today + yesterday only).
	LogFile            string `yaml:"log_file"`
	LogKeepDays        int    `yaml:"log_keep_days"`
	LogDeleteAfterDays int    `yaml:"log_delete_after_days"`
	LogConsole         bool   `yaml:"log_console"`

	// TLS/Security Policy
	RequireTLSControl bool                `yaml:"require_tls_control"` // Force TLS on control channel
	RequireTLSData    bool                `yaml:"require_tls_data"`    // Force TLS on data channel
	TLSExemptUsers    []string            `yaml:"tls_exempt_users"`    // Users allowed without TLS (local scripting)
	IPRestrictions    map[string][]string `yaml:"ip_restrictions"`     // username -> allowed IPs (optional)
}

// InviteRule restricts a channel to users with specific flags.
// If any of the flag characters in `Flags` appears in the user's flag string,
// the user may see the channel on SITE INVITE.
type InviteRule struct {
	Channel string `yaml:"channel"`
	Flags   string `yaml:"flags"`
}

// SlavePolicyConfig defines per-slave routing rules (section affinity + load-balancer weight).
// Parsed from the master config's `slaves:` list.
type SlavePolicyConfig struct {
	Name     string   `yaml:"name"`     // must match slave's registered name
	Sections []string `yaml:"sections"` // e.g. ["TV-1080P", "MP3"] (case-insensitive)
	Paths    []string `yaml:"paths"`    // e.g. ["/TV-1080P/*"]
	Weight   int      `yaml:"weight"`   // default 1, higher = more uploads routed here
	ReadOnly bool     `yaml:"readonly"` // true = scan/download only; never route uploads here
}

func LoadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	raw := map[string]interface{}{}
	_ = yaml.Unmarshal(data, &raw)
	if _, ok := raw["show_cwd_banner"]; !ok {
		cfg.ShowCWDBanner = true
	}
	if err := resolvePluginConfigFiles(cfg.Plugins, filepath.Dir(filePath)); err != nil {
		return nil, err
	}
	if !cfg.LogConsole {
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
	cfg.Zipscript.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.configPath = filePath
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	var errs []string

	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "master", "slave":
	default:
		errs = append(errs, "mode must be \"master\" or \"slave\"")
	}

	if strings.TrimSpace(c.StoragePath) == "" {
		errs = append(errs, "storage_path must not be empty")
	}
	if strings.TrimSpace(c.ACLBasePath) == "" {
		errs = append(errs, "acl_base_path must not be empty")
	}
	if mode == "master" {
		if c.ListenPort < 1 || c.ListenPort > 65535 {
			errs = append(errs, "listen_port must be between 1 and 65535 in master mode")
		}
	}
	if (c.PasvMin > 0 || c.PasvMax > 0) && (c.PasvMin < 1 || c.PasvMax < 1 || c.PasvMin > c.PasvMax) {
		errs = append(errs, "pasv_min/pasv_max must both be set and pasv_min must be <= pasv_max")
	}
	if c.TLSEnabled {
		if strings.TrimSpace(c.TLSCert) == "" {
			errs = append(errs, "tls_cert must not be empty when tls_enabled is true")
		}
		if strings.TrimSpace(c.TLSKey) == "" {
			errs = append(errs, "tls_key must not be empty when tls_enabled is true")
		}
	}
	if c.RequireTLSControl && !c.TLSEnabled {
		errs = append(errs, "require_tls_control needs tls_enabled: true")
	}
	if c.RequireTLSData && !c.TLSEnabled {
		errs = append(errs, "require_tls_data needs tls_enabled: true")
	}

	switch mode {
	case "master":
		if host, ok := mapStringValue(c.Master, "listen_host"); ok {
			if strings.TrimSpace(host) == "" {
				errs = append(errs, "master.listen_host must not be empty")
			}
		} else {
			errs = append(errs, "master.listen_host is required in master mode")
		}
		if port, err := mapIntValue(c.Master, "control_port", 1099); err != nil || port < 1 || port > 65535 {
			errs = append(errs, "master.control_port must be between 1 and 65535")
		}
		seen := map[string]struct{}{}
		for _, sp := range c.Slaves {
			name := strings.TrimSpace(sp.Name)
			if name == "" {
				errs = append(errs, "slaves[].name must not be empty")
				continue
			}
			lower := strings.ToLower(name)
			if _, exists := seen[lower]; exists {
				errs = append(errs, fmt.Sprintf("duplicate slave policy name %q", name))
			}
			seen[lower] = struct{}{}
		}
	case "slave":
		if name, ok := mapStringValue(c.Slave, "name"); !ok || strings.TrimSpace(name) == "" {
			errs = append(errs, "slave.name is required in slave mode")
		}
		if host, ok := mapStringValue(c.Slave, "master_host"); !ok || strings.TrimSpace(host) == "" {
			errs = append(errs, "slave.master_host is required in slave mode")
		}
		if port, err := mapIntValue(c.Slave, "master_port", 1099); err != nil || port < 1 || port > 65535 {
			errs = append(errs, "slave.master_port must be between 1 and 65535")
		}
	}

	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("invalid config.yml: %s", strings.Join(errs, "; "))
}

func mapStringValue(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func mapIntValue(m map[string]interface{}, key string, def int) (int, error) {
	if m == nil {
		return def, nil
	}
	raw, ok := m[key]
	if !ok {
		return def, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("%s is not an integer", key)
	}
}

func resolvePluginConfigFiles(plugins map[string]map[string]interface{}, baseDir string) error {
	for name, inline := range plugins {
		if inline == nil {
			inline = map[string]interface{}{}
		}
		configFile, _ := inline["config_file"].(string)
		configFile = strings.TrimSpace(configFile)
		if configFile == "" {
			plugins[name] = inline
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
		for k, v := range inline {
			merged[k] = v
		}
		plugins[name] = merged
	}
	return nil
}

func loadConfigFileMap(filePath, baseDir string) (map[string]interface{}, error) {
	resolved := filePath
	checked := []string{}
	if !filepath.IsAbs(filePath) {
		candidates := []string{
			filepath.Clean(filePath),
			filepath.Clean(filepath.Join(baseDir, filePath)),
			filepath.Clean(filepath.Join(filepath.Dir(baseDir), filePath)),
		}
		for _, candidate := range candidates {
			checked = append(checked, candidate)
			if _, err := os.Stat(candidate); err == nil {
				resolved = candidate
				break
			}
		}
	} else {
		checked = append(checked, filepath.Clean(filePath))
	}
	data, err := os.ReadFile(filepath.Clean(resolved))
	if err != nil {
		return nil, fmt.Errorf("config_file %q not found; check config", filePath)
	}
	out := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Rehash reloads the YAML config from disk and swaps in the fields that
// are safe to change at runtime. Fields that require process restart
// (listen_port, tls_*, storage_path, mode, master.control_port) are
// intentionally NOT updated.
//
// Runtime pointers (PluginManager, EventDispatcher, MasterManager) are
// preserved. The reload is protected by a mutex so concurrent sessions see
// a consistent snapshot. Plugin configs are also rehashed — the
// PluginManager.Dispatch() path stays live, and individual plugins can
// observe new config at next event via their Init() params (if the plugin
// itself supports live reload; stock tvmaze/imdb do not re-init on rehash
// since their section lists live in their own struct).
//
// Returns the path actually reloaded, or an error.
func (c *Config) Rehash() (string, error) {
	path := c.configPath
	if path == "" {
		return "", fmt.Errorf("no config path recorded; was this loaded via LoadConfig?")
	}
	fresh, err := LoadConfig(path)
	if err != nil {
		return path, err
	}
	if err := timeutil.Set(fresh.Timezone); err != nil {
		return path, fmt.Errorf("invalid timezone %q: %w", fresh.Timezone, err)
	}
	if c.ACLRehashHook != nil {
		if err := c.ACLRehashHook(fresh); err != nil {
			return path, err
		}
	}

	c.rehashMu.Lock()
	defer c.rehashMu.Unlock()

	// Identity / cosmetic
	c.SiteName = fresh.SiteName
	c.SiteNameShort = fresh.SiteNameShort
	c.Version = fresh.Version
	c.Email = fresh.Email
	c.LoginPrompt = fresh.LoginPrompt
	c.Timezone = fresh.Timezone

	// File-show-diz map
	c.ShowDiz = fresh.ShowDiz

	// Release management
	c.NukeMaxMultiplier = fresh.NukeMaxMultiplier
	c.NukeDirStyle = fresh.NukeDirStyle
	c.Zipscript = fresh.Zipscript
	c.Zipscript.ApplyDefaults()

	// Invite + sitebot pointer
	c.InviteChannels = fresh.InviteChannels
	c.SitebotConfig = fresh.SitebotConfig

	// Plugin config blocks — swapped in place so any plugin that re-reads
	// c.Plugins on each Dispatch will see the new values. Stock plugins
	// (tvmaze/imdb) snapshot their config at Init and don't re-read, but
	// custom plugins can implement live reload by reading c.Plugins.
	c.Plugins = fresh.Plugins

	// Slaves policy
	c.Slaves = fresh.Slaves
	c.Sections = fresh.Sections

	// Security / TLS policy (policy toggles, not socket-level TLS itself)
	c.RequireTLSControl = fresh.RequireTLSControl
	c.RequireTLSData = fresh.RequireTLSData
	c.TLSExemptUsers = fresh.TLSExemptUsers
	c.IPRestrictions = fresh.IPRestrictions

	// User limits
	c.MaxConnections = fresh.MaxConnections
	c.MaxUsers = fresh.MaxUsers
	c.MaxUsersPerIP = fresh.MaxUsersPerIP
	c.TotalUsers = fresh.TotalUsers
	// Debug toggle
	c.Debug = fresh.Debug
	c.LogFile = fresh.LogFile
	c.LogKeepDays = fresh.LogKeepDays
	c.LogDeleteAfterDays = fresh.LogDeleteAfterDays
	c.LogConsole = fresh.LogConsole

	// Fire post-rehash hook if set (e.g. reapply slave policies to SlaveManager).
	if c.RehashHook != nil {
		c.RehashHook(c)
	}

	return path, nil
}
