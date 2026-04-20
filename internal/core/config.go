package core

import (
	"fmt"
	"os"
	"sync"

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
	DisplaySize  string `yaml:"display_size_unit"`
	DisplaySpeed string `yaml:"display_speed_unit"`
	ColorMode    int    `yaml:"color_mode"`

	// Nuke
	NukeMaxMultiplier int    `yaml:"nuke_max_multiplier"`
	NukeDirStyle      string `yaml:"nukedir_style"`

	// Plugins
	Plugins         map[string]map[string]interface{} `yaml:"plugins"`
	PluginManager   *PluginManager                    `yaml:"-"` // Set at runtime
	EventFIFO       string                            `yaml:"event_fifo"`
	EventDispatcher *EventDispatcher                  `yaml:"-"` // Set at runtime
	MasterManager   interface{}                       `yaml:"-"` // *master.Manager for master mode
	RehashHook      func(*Config)                     `yaml:"-"` // called after Rehash() swaps fields

	// SITE PRE — move a release from /PRE/<group>/<rel> to /<section>/<rel>
	// and announce it. Affils are listed in `affils:`, which maps group
	// name -> pre staging path (inside the site root). Example:
	//   pre_enabled: true
	//   pre_base: "/PRE"
	//   pre_sections: ["TV-1080P", "MOVIE", "MP3", "FLAC", "X264", "X265"]
	//   pre_bw_duration: 30        # seconds to sample bandwidth after pre
	//   pre_bw_interval_ms: 500    # polling interval (ms)
	//   affils:
	//     - group: "GoFTPd"
	//       predir: "/PRE/GoFTPd"
	//     - group: "B0MBARDiERS"
	//       predir: "/PRE/B0MBARDiERS"
	PreEnabled     bool        `yaml:"pre_enabled"`
	PreBase        string      `yaml:"pre_base"`
	PreSections    []string    `yaml:"pre_sections"`
	PreDatedSections []string  `yaml:"pre_dated_sections"` // use MMDD subdir (MP3/FLAC/0DAY)
	PreBWDuration  int         `yaml:"pre_bw_duration"`    // seconds
	PreBWIntervalMs int        `yaml:"pre_bw_interval_ms"` // polling interval
	Affils         []AffilRule `yaml:"affils"`

	// Debug
	Debug bool `yaml:"debug"`

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
}

// AffilRule maps a group name to a pre-staging directory. Used by SITE PRE
// to enforce that only members of the affil group can pre from that group's
// dir. `Predir` is relative to the site root (ACLBasePath), e.g. "/PRE/GoFTPd".
type AffilRule struct {
	Group  string `yaml:"group"`
	Predir string `yaml:"predir"`
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
	cfg.configPath = filePath
	return cfg, nil
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

	c.rehashMu.Lock()
	defer c.rehashMu.Unlock()

	// Identity / cosmetic
	c.SiteName = fresh.SiteName
	c.SiteNameShort = fresh.SiteNameShort
	c.Version = fresh.Version
	c.Email = fresh.Email
	c.LoginPrompt = fresh.LoginPrompt

	// File-show-diz map
	c.ShowDiz = fresh.ShowDiz

	// Release management
	c.NukeMaxMultiplier = fresh.NukeMaxMultiplier
	c.NukeDirStyle = fresh.NukeDirStyle

	// Invite + sitebot pointer
	c.InviteChannels = fresh.InviteChannels
	c.SitebotConfig = fresh.SitebotConfig

	// Plugin config blocks — swapped in place so any plugin that re-reads
	// c.Plugins on each Dispatch will see the new values. Stock plugins
	// (tvmaze/imdb) snapshot their config at Init and don't re-read, but
	// custom plugins can implement live reload by reading c.Plugins.
	c.Plugins = fresh.Plugins

	// SITE PRE
	c.PreEnabled = fresh.PreEnabled
	c.PreBase = fresh.PreBase
	c.PreSections = fresh.PreSections
	c.PreDatedSections = fresh.PreDatedSections
	c.PreBWDuration = fresh.PreBWDuration
	c.PreBWIntervalMs = fresh.PreBWIntervalMs
	c.Affils = fresh.Affils

	// Slaves policy
	c.Slaves = fresh.Slaves

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

	// Fire post-rehash hook if set (e.g. reapply slave policies to SlaveManager).
	if c.RehashHook != nil {
		c.RehashHook(c)
	}

	return path, nil
}
