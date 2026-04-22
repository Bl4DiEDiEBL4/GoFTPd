package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"goftpd/internal/acl"
	"goftpd/internal/core"
	"goftpd/internal/dupe"
	"goftpd/internal/master"
	"goftpd/internal/plugin"
	"goftpd/internal/protocol"
	"goftpd/internal/slave"
	"goftpd/internal/timeutil"
	"goftpd/plugins/dateddirs"
	"goftpd/plugins/imdb"
	"goftpd/plugins/mediainfo"
	"goftpd/plugins/pre"
	"goftpd/plugins/speedtest"
	"goftpd/plugins/tvmaze"
	"gopkg.in/yaml.v3"
)

func main() {
	// 1. Load Server Config from etc/config.yml
	cfg, err := core.LoadConfig("etc/config.yml")
	if err != nil {
		log.Fatalf("Failed to load etc/config.yml: %v", err)
	}
	if err := timeutil.Set(cfg.Timezone); err != nil {
		log.Fatalf("Invalid timezone %q in etc/config.yml: %v", cfg.Timezone, err)
	}

	// 1a. Install file logger (active only when debug=true AND log_file is set).
	// Tee's log output to both stderr and the file, rotates daily, keeps the
	// last log_keep_days archived copies (default 1).
	if cfg.Debug && cfg.LogFile != "" {
		if err := core.InstallFileLogger(cfg.LogFile, cfg.LogKeepDays); err != nil {
			log.Printf("[LOG] file logger init failed: %v (continuing with stderr only)", err)
		}
	}

	// SLAVE MODE: No FTP server, just connect to master and serve files
	if cfg.Mode == "slave" {
		startSlave(cfg)
		return
	}

	// 2. Load ACL Engine (Permissions)
	aclEngine, err := acl.LoadEngine("etc/permissions.yml")
	if err != nil {
		log.Printf("Warning: etc/permissions.yml not found, using empty rules: %v", err)
		aclEngine = &acl.Engine{
			RulesByType: make(map[string][]acl.Rule),
		}
	}

	// 3. Load TLS Certificates
	cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		log.Fatalf("Failed to load TLS certs: %v", err)
	}

	// 4. Setup Shared TLS Cache for FXP/Resumption
	sharedCache := tls.NewLRUClientSessionCache(256)
	var ticketKey [32]byte
	copy(ticketKey[:], "goftpd-secret-session-key-32byte")

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS13,
		ClientSessionCache: sharedCache,
		SessionTicketKey:   ticketKey,
		InsecureSkipVerify: true,
	}

	// 5. Ensure Storage Path Exists
	if _, err := os.Stat(cfg.StoragePath); os.IsNotExist(err) {
		os.MkdirAll(cfg.StoragePath, 0755)
	}

	// 6. MASTER MODE: Start SlaveManager and wire Bridge into config
	var sm *master.SlaveManager
	var masterBridge *master.Bridge // set in master block below; nil in slave mode
	if cfg.Mode == "master" {
		sm = master.NewSlaveManager(
			cfg.Master["listen_host"].(string),
			intFromCfg(cfg.Master, "control_port", 1099),
			cfg.TLSEnabled,
			cfg.TLSCert,
			cfg.TLSKey,
		)
		sm.SetDiskStatusHook(func(name string, status protocol.DiskStatus, online, available bool, sections []string) {
			core.PublishEvent(cfg, core.Event{
				Type:      core.EventDiskStatus,
				Timestamp: time.Now(),
				Data: map[string]string{
					"slave":       name,
					"free_bytes":  fmt.Sprintf("%d", status.SpaceAvailable),
					"total_bytes": fmt.Sprintf("%d", status.SpaceCapacity),
					"online":      fmt.Sprintf("%t", online),
					"available":   fmt.Sprintf("%t", available),
					"sections":    strings.Join(sections, ","),
				},
			})
		})
		sm.SetProtectedDirs(protectedVFSDirs(cfg))
		if err := sm.Start(); err != nil {
			log.Fatalf("SlaveManager failed: %v", err)
		}

		// Apply per-slave routing policies (section affinity + load-balancing weights)
		if len(cfg.Slaves) > 0 {
			policies := make(map[string]master.SlaveRoutePolicy, len(cfg.Slaves))
			for _, sp := range cfg.Slaves {
				if sp.Name == "" {
					continue
				}
				policies[sp.Name] = master.SlaveRoutePolicy{
					Sections: sp.Sections,
					Paths:    sp.Paths,
					Weight:   sp.Weight,
					ReadOnly: sp.ReadOnly,
				}
			}
			sm.SetSlavePolicies(policies)
			sm.SetProtectedDirs(protectedVFSDirs(cfg))
			sm.PublishAllDiskStatuses()
			log.Printf("[MASTER] Applied routing policies for %d slave(s)", len(policies))
		}
		// Create Bridge (implements core.MasterBridge) and inject into config
		// so the FTP session can route STOR/RETR/LIST/DELE to slaves
		bridge := master.NewBridge(sm)
		cfg.MasterManager = bridge
		masterBridge = bridge

		log.Printf("[MASTER] SlaveManager listening on port %d, waiting for slaves...",
			intFromCfg(cfg.Master, "control_port", 1099))
	}

	// Register a post-rehash hook so SITE REHASH / SIGHUP both reapply slave
	// routing policies after config is reloaded.
	if sm != nil {
		cfg.RehashHook = func(c *core.Config) {
			policies := make(map[string]master.SlaveRoutePolicy, len(c.Slaves))
			for _, sp := range c.Slaves {
				if sp.Name == "" {
					continue
				}
				policies[sp.Name] = master.SlaveRoutePolicy{
					Sections: sp.Sections,
					Paths:    sp.Paths,
					Weight:   sp.Weight,
					ReadOnly: sp.ReadOnly,
				}
			}
			sm.SetSlavePolicies(policies)
			sm.SetProtectedDirs(protectedVFSDirs(c))
			sm.PublishAllDiskStatuses()
			log.Printf("[REHASH] reapplied %d slave policies", len(policies))
		}
	}

	// 7. Initialize Plugin System
	if cfg.Debug {
		log.Printf("[PLUGINS] Initializing plugin system...")
	}
	cfg.PluginManager = core.NewPluginManager(cfg.Debug)

	// Give plugins access to the master bridge + debug flag via Services.
	// In slave mode masterBridge is nil — plugins that need it will skip
	// their work gracefully (svc.Bridge == nil check).
	var bridgeForPlugins plugin.MasterBridge
	if masterBridge != nil {
		bridgeForPlugins = masterBridge
	}
	cfg.PluginManager.SetServices(&plugin.Services{
		Bridge: bridgeForPlugins,
		Debug:  cfg.Debug,
		EmitEvent: func(eventType, eventPath, filename, section string, size int64, speed float64, data map[string]string) {
			core.PublishEvent(cfg, core.Event{
				Type:      core.EventType(eventType),
				Timestamp: time.Now(),
				Section:   section,
				Filename:  filename,
				Path:      path.Clean(eventPath),
				Size:      size,
				Speed:     speed,
				Data:      data,
			})
		},
	})

	// 7a. Dynamically load plugins from config
	if cfg.Plugins == nil {
		cfg.Plugins = make(map[string]map[string]interface{})
	}

	pluginConfigs := make(map[string]map[string]interface{}, len(cfg.Plugins))
	for pluginName, pluginCfg := range cfg.Plugins {
		canonicalName := strings.ToLower(strings.TrimSpace(pluginName))
		if canonicalName == "" {
			continue
		}
		pluginConfigs[canonicalName] = pluginCfg
		enabled, ok := pluginCfg["enabled"].(bool)
		if !ok || !enabled {
			if cfg.Debug {
				log.Printf("[PLUGINS] Skipping disabled plugin: %s", pluginName)
			}
			continue
		}

		if pluginCfg["storage_path"] == nil {
			pluginCfg["storage_path"] = cfg.StoragePath
		}
		if pluginCfg["sitename"] == nil {
			pluginCfg["sitename"] = cfg.SiteNameShort
		}
		if pluginCfg["debug"] == nil {
			pluginCfg["debug"] = cfg.Debug
		}

		var p plugin.Plugin
		switch canonicalName {
		case "dateddirs":
			p = dateddirs.New()
		case "tvmaze":
			p = tvmaze.New()
		case "imdb":
			p = imdb.New()
		case "mediainfo":
			p = mediainfo.New()
		case "pre":
			p = pre.New()
		case "speedtest":
			p = speedtest.New()
		default:
			log.Printf("[PLUGINS] Unknown plugin: %s (add a case in cmd/goftpd/main.go)", pluginName)
			continue
		}

		if err := cfg.PluginManager.RegisterPlugin(p); err != nil {
			log.Fatalf("Failed to register %s plugin: %v", pluginName, err)
		}
		if cfg.Debug {
			log.Printf("[PLUGINS] Registered %s plugin", pluginName)
		}
	}

	// 7b. Initialize all plugins with config
	if err := cfg.PluginManager.InitializePlugins(pluginConfigs); err != nil {
		log.Fatalf("Failed to initialize plugins: %v", err)
	}
	if cfg.Debug {
		log.Printf("[PLUGINS] All plugins initialized")
	}

	// 8. Initialize dupe checker (duplicate detection)
	var dupeChecker interface{}
	if cfg.XdupeEnabled {
		// Ensure parent directory exists for the dupe DB
		if dir := filepath.Dir(cfg.XdupeDBPath); dir != "" && dir != "." {
			os.MkdirAll(dir, 0755)
		}
		d, err := dupe.NewDupeChecker(cfg.XdupeDBPath, cfg.Debug)
		if err != nil {
			log.Printf("Warning: Failed to initialize dupe checker: %v", err)
		} else {
			dupeChecker = d
			if cfg.Debug {
				log.Printf("[DUPE] Enabled and initialized at %s", cfg.XdupeDBPath)
			}
		}
	}

	if dupeChecker != nil {
		defer func() {
			if d, ok := dupeChecker.(*dupe.DupeChecker); ok {
				d.Close()
			}
		}()
	}

	// 9. Start FTP Listener
	listenAddr := fmt.Sprintf(":%d", cfg.ListenPort)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	pluginCount := 0
	if cfg.PluginManager != nil {
		pluginCount = len(cfg.PluginManager.GetPlugins())
	}
	log.Printf("GoFTPd online at %s [Mode=%s] [Plugins=%d]", listenAddr, cfg.Mode, pluginCount)

	// Accept FTP clients
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				continue
			}
			go core.HandleSession(conn, tlsConfig, cfg, aclEngine, dupeChecker)
		}
	}()

	// Signal handling: SIGINT/SIGTERM shut down; SIGHUP rehashes config.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	for s := range sig {
		if s == syscall.SIGHUP {
			if path, err := cfg.Rehash(); err != nil {
				log.Printf("[REHASH] SIGHUP reload failed: %v", err)
			} else {
				log.Printf("[REHASH] SIGHUP: reloaded %s", path)
			}
			continue
		}
		log.Println("Shutting down...")
		return
	}
}

// startSlave runs the slave daemon — no FTP server, just connect to master.
func startSlave(cfg *core.Config) {
	log.Printf("[SLAVE] Name '%s', connecting to master", cfg.Mode)

	// Extract slave config from the map
	slaveCfg := cfg.Slave
	name, _ := slaveCfg["name"].(string)
	masterHost, _ := slaveCfg["master_host"].(string)
	masterPort := intFromCfg(slaveCfg, "master_port", 1099)

	var roots []string
	if rootsRaw, ok := slaveCfg["roots"]; ok {
		if rootsList, ok := rootsRaw.([]interface{}); ok {
			for _, r := range rootsList {
				if s, ok := r.(string); ok {
					roots = append(roots, s)
				}
			}
		}
	}
	if len(roots) == 0 && cfg.StoragePath != "" {
		roots = []string{cfg.StoragePath}
	}

	pasvMin := intFromCfg(slaveCfg, "pasv_port_min", 0)
	pasvMax := intFromCfg(slaveCfg, "pasv_port_max", 0)
	bindIP, _ := slaveCfg["bind_ip"].(string)
	timeout := intFromCfg(slaveCfg, "timeout", 60)

	log.Printf("[SLAVE] Name=%s Master=%s:%d Roots=%v", name, masterHost, masterPort, roots)

	s := slave.NewSlave(slave.SlaveConfig{
		Name:        name,
		MasterHost:  masterHost,
		MasterPort:  masterPort,
		Roots:       roots,
		PasvPortMin: pasvMin,
		PasvPortMax: pasvMax,
		TLSEnabled:  cfg.TLSEnabled,
		TLSCert:     cfg.TLSCert,
		TLSKey:      cfg.TLSKey,
		BindIP:      bindIP,
		Timeout:     timeout,
	})

	// Boot blocks until disconnected
	if err := s.Boot(); err != nil {
		log.Fatalf("[SLAVE] Error: %v", err)
	}
}

// intFromCfg extracts an int from a map[string]interface{} with a default.
func intFromCfg(m map[string]interface{}, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case int64:
		return int(val)
	default:
		return def
	}
}

func stringSliceFromPluginConfig(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func protectedVFSDirs(cfg *core.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "*?[]") {
			return
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		p = path.Clean(p)
		if p == "." {
			p = "/"
		}
		if p != "/" {
			seen[p] = true
		}
	}
	for _, sp := range cfg.Slaves {
		for _, section := range sp.Sections {
			add(section)
		}
		for _, pat := range sp.Paths {
			clean := path.Clean("/" + strings.TrimSpace(pat))
			parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
			if len(parts) > 0 {
				add(parts[0])
			}
		}
	}
	if datedCfg := cfg.Plugins["dateddirs"]; datedCfg != nil {
		for _, section := range stringSliceFromPluginConfig(datedCfg["sections"]) {
			add(section)
		}
	}
	if preCfg := cfg.Plugins["pre"]; preCfg != nil {
		for _, section := range stringSliceFromPluginConfig(preCfg["sections"]) {
			add(section)
		}
		if base, ok := preCfg["base"].(string); ok {
			add(base)
		}
		affilsFile := "etc/affils.yml"
		if filePath, ok := preCfg["affils_file"].(string); ok && strings.TrimSpace(filePath) != "" {
			affilsFile = strings.TrimSpace(filePath)
		}
		if affilsCfg, err := loadAffilsProtectionConfig(affilsFile); err == nil {
			for _, affil := range affilsCfg.Groups {
				if strings.TrimSpace(affil.Predir) != "" {
					add(affil.Predir)
					continue
				}
				if strings.TrimSpace(affil.Group) != "" {
					base := affilsCfg.Base
					if strings.TrimSpace(base) == "" {
						base = "/PRE"
					}
					add(path.Join(base, affil.Group))
				}
			}
		}
		if affils, ok := preCfg["affils"].([]interface{}); ok {
			for _, raw := range affils {
				item, ok := raw.(map[string]interface{})
				if !ok {
					continue
				}
				if predir, ok := item["predir"].(string); ok && strings.TrimSpace(predir) != "" {
					add(predir)
					continue
				}
				if group, ok := item["group"].(string); ok && strings.TrimSpace(group) != "" {
					base := "/PRE"
					if configuredBase, ok := preCfg["base"].(string); ok && strings.TrimSpace(configuredBase) != "" {
						base = configuredBase
					}
					add(path.Join(base, group))
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

type affilsProtectionConfig struct {
	Base   string                  `yaml:"base"`
	Groups []affilsProtectionGroup `yaml:"groups"`
}

type affilsProtectionGroup struct {
	Group  string `yaml:"group"`
	Predir string `yaml:"predir"`
}

func loadAffilsProtectionConfig(filePath string) (affilsProtectionConfig, error) {
	var cfg affilsProtectionConfig
	data, err := os.ReadFile(strings.TrimSpace(filePath))
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
