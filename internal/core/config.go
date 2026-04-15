package core

import (
	"os"
	"gopkg.in/yaml.v3"
)

type Config struct {
	// Server Identity
	SiteName        string   `yaml:"sitename_long"`
	SiteNameShort   string   `yaml:"sitename_short"`
	Version         string   `yaml:"version"`
	Email           string   `yaml:"email"`
	LoginPrompt     string   `yaml:"login_prompt"`
	
	// Network
	ListenPort      int      `yaml:"listen_port"`
	PublicIP        string   `yaml:"public_ip"`
	PasvMin         int      `yaml:"pasv_min"`
	PasvMax         int      `yaml:"pasv_max"`
	
	// Daemon Mode
	
	// Master/Slave Mode (master or slave)
	Mode            string   `yaml:"mode"`
	Master          map[string]interface{} `yaml:"master"`
	Slave           map[string]interface{} `yaml:"slave"`
	
	// Storage & Paths
	StoragePath     string   `yaml:"storage_path"`
	RootPath        string   `yaml:"rootpath"`
	DataPath        string   `yaml:"datapath"`
	ACLBasePath     string   `yaml:"acl_base_path"`
	PasswdFile      string   `yaml:"passwd_file"`
	ShellPath       string   `yaml:"shell_path"`
	MsgPath         string   `yaml:"msg_path"`
	
	// TLS/SSL
	TLSEnabled      bool     `yaml:"tls_enabled"`
	TLSCert         string   `yaml:"tls_cert"`
	TLSKey          string   `yaml:"tls_key"`
	TLSMinProto     string   `yaml:"tls_min_protocol"`
	TLSMaxProto     string   `yaml:"tls_max_protocol"`
	SSLCleanShutdown bool    `yaml:"ssl_clean_shutdown"`
	
	// Security Policies
	Shutdown        int      `yaml:"shutdown"`
	UserRejectSecure string  `yaml:"userrejectsecure"`
	UserRejectInsecure string `yaml:"userrejectinsecure"`
	DenyDirEncrypted string  `yaml:"denydiruncrypted"`
	DenyDataEncrypted string `yaml:"denydatauncrypted"`
	
	// User Limits
	MaxConnections  int      `yaml:"max_connections"`  // Global max concurrent connections
	MaxUsers        int      `yaml:"max_users"`
	MaxUsersPerIP   int      `yaml:"max_users_per_ip"`
	TotalUsers      int      `yaml:"total_users"`
	
	// File Rules
	FileNamesLower  bool     `yaml:"file_names_lowercase"`
	DirNamesLower   bool     `yaml:"dir_names_lowercase"`
	AllowASCII      []string `yaml:"allow_ascii_uploads"`
	HiddenFiles     []string `yaml:"hidden_files"`
	ShowDiz         map[string]string `yaml:"show_diz"` // filename -> who_can_see
	ShowSymlinks    bool     `yaml:"show_symlinks"` // Show symlinks in LIST
	SFVCheck        bool     `yaml:"sfv_check"`
	
	// Dupe Checking
	DupeCheck       bool     `yaml:"dupe_check"`
	DupeDB          string   `yaml:"dupe_db_path"`
	XdupeEnabled    bool     `yaml:"xdupe_enabled"`
	XdupeDBPath     string   `yaml:"xdupe_db_path"`
	XdupeExts       []string `yaml:"xdupe_extensions"`
	
	// Download & Credits
	DLIncomplete    bool     `yaml:"dl_incomplete"`
	CreditLoss      int      `yaml:"credit_loss"`
	CreditCheck     bool     `yaml:"creditcheck"`
	FreeSpaceMB     int      `yaml:"free_space_mb"`
	
	// Display
	DisplaySize     string   `yaml:"display_size_unit"`
	DisplaySpeed    string   `yaml:"display_speed_unit"`
	ColorMode       int      `yaml:"color_mode"`
	
	// Nuke
	NukeMaxMultiplier int      `yaml:"nuke_max_multiplier"`
	NukeDirStyle      string   `yaml:"nukedir_style"`
	
	// Plugins
	Plugins         map[string]map[string]interface{} `yaml:"plugins"`
	PluginManager   *PluginManager                     `yaml:"-"` // Set at runtime
	MasterManager   interface{}                        `yaml:"-"` // *master.Manager for master mode
	
	// Debug
	Debug           bool     `yaml:"debug"`
	
	// TLS/Security Policy
	RequireTLSControl   bool     `yaml:"require_tls_control"`      // Force TLS on control channel
	RequireTLSData      bool     `yaml:"require_tls_data"`         // Force TLS on data channel
	TLSExemptUsers      []string `yaml:"tls_exempt_users"`         // Users allowed without TLS (local scripting)
	IPRestrictions      map[string][]string `yaml:"ip_restrictions"` // username -> allowed IPs (optional)
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
	return cfg, nil
}