package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"goftpd/plugins/autonuke"
	"goftpd/plugins/dateddirs"
	"goftpd/plugins/imdb"
	"goftpd/plugins/pre"
	"goftpd/plugins/pretime"
	"goftpd/plugins/releaseguard"
	"goftpd/plugins/request"
	"goftpd/plugins/slowkick"
	"goftpd/plugins/spacekeeper"
	"goftpd/plugins/speedtest"
	"goftpd/plugins/tvmaze"
	"gopkg.in/yaml.v3"
)

func main() {
	configPath, err := configPathFromArgs(os.Args[1:])
	if err != nil {
		log.Fatalf("Invalid arguments: %v", err)
	}

	// 1. Load Server Config
	cfg, err := core.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load %s: %v", configPath, err)
	}
	if err := timeutil.Set(cfg.Timezone); err != nil {
		log.Fatalf("Invalid timezone %q in %s: %v", cfg.Timezone, configPath, err)
	}
	// 1a. Install logging early. File logs always keep the full stream when
	// log_file is set. Console stays full in debug mode, otherwise it is
	// filtered down to warnings/errors.
	if cfg.LogFile != "" {
		if err := core.InstallFileLogger(cfg.LogFile, cfg.LogDeleteAfterDays, cfg.LogConsole, cfg.Debug); err != nil {
			log.Printf("[LOG] file logger init failed: %v (continuing with stderr only)", err)
		}
	} else {
		core.InstallConsoleLogger(cfg.Debug)
	}
	core.PrintStartupBanner(cfg.Version, "GoFTPd daemon")

	// SLAVE MODE: No FTP server, just connect to master and serve files
	if cfg.Mode == "slave" {
		startSlave(cfg)
		return
	}

	// 2. Load ACL Engine (Permissions)
	aclEngine, err := acl.LoadEngine("etc/permissions.yml")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("Warning: etc/permissions.yml not found, using empty rules: %v", err)
			aclEngine = &acl.Engine{
				RulesByType: make(map[string][]acl.Rule),
			}
		} else {
			log.Fatalf("Failed to load etc/permissions.yml: %v", err)
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
			time.Duration(intFromCfg(cfg.Master, "heartbeat_timeout", 60))*time.Second,
		)
		if err := sm.ConfigureAuthAllowlist(stringSliceFromCfg(cfg.Master, "slave_allowlist")); err != nil {
			log.Fatalf("Invalid master.slave_allowlist: %v", err)
		}
		if err := sm.ConfigureAuthDenylistFile(stringFromCfg(cfg.Master, "slave_denylist_file", "etc/slave_denylist.txt")); err != nil {
			log.Fatalf("Invalid master.slave_denylist_file: %v", err)
		}
		sm.ConfigureAuthGuard(
			intFromCfg(cfg.Master, "slave_auth_fail_limit", 2),
			time.Duration(intFromCfg(cfg.Master, "slave_auth_fail_window_seconds", 900))*time.Second,
			time.Duration(intFromCfg(cfg.Master, "slave_auth_ban_seconds", 3600))*time.Second,
		)
		sm.SetDiskStatusHook(func(name string, status protocol.DiskStatus, online, available bool, sections []string) {
			roots := make([]map[string]string, 0, len(status.Roots))
			for _, root := range status.Roots {
				roots = append(roots, map[string]string{
					"path":        root.Path,
					"mount_path":  root.MountPath,
					"free_bytes":  fmt.Sprintf("%d", root.SpaceAvailable),
					"total_bytes": fmt.Sprintf("%d", root.SpaceCapacity),
				})
			}
			rootsJSON, _ := json.Marshal(roots)
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
					"roots_json":  string(rootsJSON),
				},
			})
		})
		sm.SetSecurityHook(func(ip, remoteAddr, action, reason string, strikes, limit int, bannedUntil time.Time) {
			data := map[string]string{
				"remote_ip":    ip,
				"remote_addr":  remoteAddr,
				"action":       action,
				"reason":       reason,
				"strikes":      fmt.Sprintf("%d", strikes),
				"limit":        fmt.Sprintf("%d", limit),
				"banned_until": bannedUntil.Format(time.RFC3339),
			}
			core.PublishEvent(cfg, core.Event{
				Type:      core.EventSlaveAuthFail,
				Timestamp: time.Now(),
				Data:      data,
			})
		})
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
		sm.SetBootstrapDirs(configuredBootstrapDirs(cfg))
		sm.SetProtectedDirs(protectedVFSDirs(cfg))
		sm.SetHiddenPaths(cfg.HiddenVFSPaths)
		sm.SetExcludePaths(cfg.ExcludeVFSPaths)
		sm.SetStatusMarkerConfig(cfg.Zipscript)
		sm.SetRemergeMode(stringFromCfg(cfg.Master, "remerge_mode", "off"))
		sm.SetManualRemergeMode(stringFromCfg(cfg.Master, "manual_remerge_mode", "instant"))
		sm.SetEnableRemergeChecksums(boolFromCfg(cfg.Master, "remerge_checksums", false))
		sm.SetRemergeFlowControl(
			intFromCfg(cfg.Master, "remerge_pause_threshold", 250),
			intFromCfg(cfg.Master, "remerge_resume_threshold", 50),
		)
		configureBackgroundRemerge(sm, cfg.Master)
		if err := sm.Start(); err != nil {
			log.Fatalf("SlaveManager failed: %v", err)
		}
		if len(policies) > 0 {
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
			sm.SetBootstrapDirs(configuredBootstrapDirs(c))
			sm.SetProtectedDirs(protectedVFSDirs(c))
			sm.SetHiddenPaths(c.HiddenVFSPaths)
			sm.SetExcludePaths(c.ExcludeVFSPaths)
			sm.SetStatusMarkerConfig(c.Zipscript)
			sm.SetRemergeMode(stringFromCfg(c.Master, "remerge_mode", "off"))
			sm.SetManualRemergeMode(stringFromCfg(c.Master, "manual_remerge_mode", "instant"))
			sm.SetEnableRemergeChecksums(boolFromCfg(c.Master, "remerge_checksums", false))
			sm.SetRemergeFlowControl(
				intFromCfg(c.Master, "remerge_pause_threshold", 250),
				intFromCfg(c.Master, "remerge_resume_threshold", 50),
			)
			configureBackgroundRemerge(sm, c.Master)
			if err := sm.ConfigureAuthAllowlist(stringSliceFromCfg(c.Master, "slave_allowlist")); err != nil {
				log.Printf("[REHASH] invalid master.slave_allowlist: %v", err)
			}
			if err := sm.ConfigureAuthDenylistFile(stringFromCfg(c.Master, "slave_denylist_file", "etc/slave_denylist.txt")); err != nil {
				log.Printf("[REHASH] invalid master.slave_denylist_file: %v", err)
			}
			sm.ConfigureAuthGuard(
				intFromCfg(c.Master, "slave_auth_fail_limit", 2),
				time.Duration(intFromCfg(c.Master, "slave_auth_fail_window_seconds", 900))*time.Second,
				time.Duration(intFromCfg(c.Master, "slave_auth_ban_seconds", 3600))*time.Second,
			)
			sm.EnsureBootstrapDirs()
			sm.PublishAllDiskStatuses()
			log.Printf("[REHASH] reapplied %d slave policies", len(policies))
		}
	}
	cfg.ACLRehashHook = func(c *core.Config) error {
		freshACL, err := acl.LoadEngine("etc/permissions.yml")
		if err != nil {
			return err
		}
		aclEngine = freshACL
		core.UpdateActiveSessionACL(freshACL)
		log.Printf("[REHASH] reloaded ACL engine from etc/permissions.yml")
		return nil
	}

	// 7. Initialize Plugin System
	if cfg.Debug {
		log.Printf("[PLUGINS] Initializing plugin system...")
	}
	cfg.PluginManager = core.NewPluginManager(cfg.Debug)
	cfg.PluginManager.SetConfig(cfg)

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
		ListSlaveStates: func() []plugin.SlaveState {
			if sm == nil {
				return nil
			}
			slaves := sm.GetAvailableSlaves()
			out := make([]plugin.SlaveState, 0, len(slaves))
			for _, rs := range slaves {
				if rs == nil {
					continue
				}
				ds := rs.GetDiskStatus()
				rootStates := make([]plugin.SlaveRootState, 0, len(ds.Roots))
				for _, root := range ds.Roots {
					rootStates = append(rootStates, plugin.SlaveRootState{
						Path:       root.Path,
						MountPath:  root.MountPath,
						FreeBytes:  root.SpaceAvailable,
						TotalBytes: root.SpaceCapacity,
					})
				}
				out = append(out, plugin.SlaveState{
					Name:            rs.Name(),
					Available:       rs.IsAvailable(),
					ReadOnly:        sm.IsSlaveReadOnly(rs.Name()),
					ActiveTransfers: rs.ActiveTransfers(),
					FreeBytes:       ds.SpaceAvailable,
					TotalBytes:      ds.SpaceCapacity,
					Roots:           rootStates,
				})
			}
			return out
		},
		ListActiveSessions: core.ListActiveSessionsForPlugins,
		DisconnectSession:  core.DisconnectActiveSession,
		GetLiveTransferStats: func() []plugin.LiveTransferStat {
			if masterBridge == nil {
				return nil
			}
			stats := masterBridge.GetLiveTransferStats()
			out := make([]plugin.LiveTransferStat, 0, len(stats))
			for _, stat := range stats {
				out = append(out, plugin.LiveTransferStat{
					SlaveName:     stat.SlaveName,
					TransferIndex: stat.TransferIndex,
					Direction:     stat.Direction,
					Path:          stat.Path,
					StartedAt:     stat.StartedAt,
					Transferred:   stat.Transferred,
					SpeedBytes:    stat.SpeedBytes,
				})
			}
			return out
		},
		AbortTransfer: func(slaveName string, transferIndex int32, reason string) bool {
			if masterBridge == nil {
				return false
			}
			return masterBridge.AbortTransfer(slaveName, transferIndex, reason)
		},
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
		if pluginCfg["version"] == nil {
			pluginCfg["version"] = cfg.Version
		}
		if pluginCfg["debug"] == nil {
			pluginCfg["debug"] = cfg.Debug
		}
		var p plugin.Plugin
		switch canonicalName {
		case "autonuke":
			p = autonuke.New()
		case "dateddirs":
			p = dateddirs.New()
		case "tvmaze":
			p = tvmaze.New()
		case "imdb":
			p = imdb.New()
		case "pre":
			p = pre.New()
		case "pretime":
			p = pretime.New()
		case "releaseguard":
			p = releaseguard.New()
		case "request":
			p = request.New()
		case "speedtest":
			p = speedtest.New()
		case "slowkick":
			p = slowkick.New()
		case "spacekeeper":
			p = spacekeeper.New()
		default:
			log.Printf("[PLUGINS] Unknown plugin: %s (add a case in cmd/goftpd/main.go)", pluginName)
			continue
		}

		if err := cfg.PluginManager.RegisterPlugin(p); err != nil {
			log.Fatalf("Failed to register %s plugin: %v", pluginName, err)
		}
		log.Printf("[PLUGINS] Plugin loaded: %s", pluginName)
	}
	if len(cfg.Plugins) > 0 {
		log.Printf("[PLUGINS] Plugin load complete")
	}

	// 7b. Initialize all plugins with config
	if err := cfg.PluginManager.InitializePlugins(pluginConfigs); err != nil {
		log.Fatalf("Failed to initialize plugins: %v", err)
	}
	if masterBridge != nil {
		masterBridge.SetTransferSpeedPolicy(func(username, primaryGroup, transferPath, direction string) (int64, int64, int64) {
			if cfg.PluginManager == nil {
				return 0, 0, 0
			}
			return cfg.PluginManager.TransferSpeedLimits(username, primaryGroup, transferPath, direction)
		})
	}
	if cfg.Debug {
		log.Printf("[PLUGINS] All plugins initialized")
	}
	if freshACL, err := acl.LoadEngine("etc/permissions.yml"); err != nil {
		log.Printf("[ACL] post-plugin reload skipped: %v", err)
	} else {
		aclEngine = freshACL
		if cfg.Debug {
			log.Printf("[ACL] reloaded ACL engine after plugin initialization")
		}
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
	log.Printf("[STARTUP] GoFTPd online [mode=%s] [listen=%s] [public_ip=%s] [passthrough=%t] [plugins=%d]",
		cfg.Mode, listenAddr, cfg.PublicIP, cfg.Passthrough, pluginCount)

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

func configPathFromArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("goftpd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "etc/config.yml", "path to daemon config file")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	cleanPath := strings.TrimSpace(*configPath)
	if cleanPath == "" {
		return "", fmt.Errorf("config path must not be empty")
	}
	return cleanPath, nil
}

// startSlave runs the slave daemon — no FTP server, just connect to master.
func startSlave(cfg *core.Config) {
	// Extract slave config from the map
	slaveCfg := cfg.Slave
	name, _ := slaveCfg["name"].(string)
	masterHost, _ := slaveCfg["master_host"].(string)
	masterPort := intFromCfg(slaveCfg, "master_port", 1099)

	roots, mountedRoots := parseSlaveRoots(slaveCfg)
	pasvMin := intFromCfg(slaveCfg, "pasv_port_min", 0)
	pasvMax := intFromCfg(slaveCfg, "pasv_port_max", 0)
	bindIP, _ := slaveCfg["bind_ip"].(string)
	timeout := intFromCfg(slaveCfg, "timeout", 60)
	ignorePartialRemerge := boolFromCfg(slaveCfg, "ignore_partial_remerge", false)
	transferBufferSize := intFromCfg(slaveCfg, "transfer_buffer_size", 0)
	remergeDelayMS := intFromCfg(slaveCfg, "remerge_delay_ms", 0)
	remergePauseOnActiveTransfers := intFromCfg(slaveCfg, "remerge_pause_on_active_transfers", 0)

	log.Printf("[STARTUP] Slave mode [name=%s] [master=%s:%d] [roots=%v] [mounted_roots=%v] [bind_ip=%s] [pasv=%d-%d] [remerge_delay_ms=%d] [remerge_pause_on_active_transfers=%d]",
		name, masterHost, masterPort, roots, mountedRoots, bindIP, pasvMin, pasvMax, remergeDelayMS, remergePauseOnActiveTransfers)

	s := slave.NewSlave(slave.SlaveConfig{
		Name:                          name,
		MasterHost:                    masterHost,
		MasterPort:                    masterPort,
		Roots:                         roots,
		MountedRoots:                  mountedRoots,
		PasvPortMin:                   pasvMin,
		PasvPortMax:                   pasvMax,
		TLSEnabled:                    cfg.TLSEnabled,
		TLSCert:                       cfg.TLSCert,
		TLSKey:                        cfg.TLSKey,
		BindIP:                        bindIP,
		Timeout:                       timeout,
		IgnorePartialRemerge:          ignorePartialRemerge,
		TransferBufferSize:            transferBufferSize,
		FreeSpaceMB:                   cfg.FreeSpaceMB,
		RemergeDelayMS:                remergeDelayMS,
		RemergePauseOnActiveTransfers: remergePauseOnActiveTransfers,
		Debug:                         cfg.Debug,
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

func boolFromCfg(m map[string]interface{}, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func configureBackgroundRemerge(sm *master.SlaveManager, masterCfg map[string]interface{}) {
	if sm == nil {
		return
	}
	intervalSeconds := intFromCfg(masterCfg, "background_remerge_interval_seconds", 0)
	startDelaySeconds := intFromCfg(masterCfg, "background_remerge_start_delay_seconds", 300)
	staggerSeconds := intFromCfg(masterCfg, "background_remerge_stagger_seconds", 60)
	basePath := stringFromCfg(masterCfg, "background_remerge_path", "/")
	rootsOnly := boolFromCfg(masterCfg, "background_remerge_roots_only", false)
	skipBusy := boolFromCfg(masterCfg, "background_remerge_skip_busy_slaves", true)

	sm.ConfigureBackgroundRemerge(
		time.Duration(intervalSeconds)*time.Second,
		time.Duration(startDelaySeconds)*time.Second,
		time.Duration(staggerSeconds)*time.Second,
		basePath,
		rootsOnly,
		skipBusy,
	)
}

func parseSlaveRoots(slaveCfg map[string]interface{}) ([]string, []slave.MountedRoot) {
	var roots []string
	var mounted []slave.MountedRoot
	if rootsRaw, ok := slaveCfg["roots"]; ok {
		if rootsList, ok := rootsRaw.([]interface{}); ok {
			for _, item := range rootsList {
				switch v := item.(type) {
				case string:
					if strings.TrimSpace(v) != "" {
						roots = append(roots, strings.TrimSpace(v))
					}
				case map[string]interface{}:
					pathValue, _ := v["path"].(string)
					mountValue, _ := v["mount_path"].(string)
					if strings.TrimSpace(pathValue) == "" {
						continue
					}
					mounted = append(mounted, slave.MountedRoot{
						Path:      strings.TrimSpace(pathValue),
						MountPath: strings.TrimSpace(mountValue),
					})
				}
			}
		}
	}
	if mountedRaw, ok := slaveCfg["mounted_roots"]; ok {
		if mountedList, ok := mountedRaw.([]interface{}); ok {
			for _, item := range mountedList {
				v, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				pathValue, _ := v["path"].(string)
				mountValue, _ := v["mount_path"].(string)
				if strings.TrimSpace(pathValue) == "" {
					continue
				}
				mounted = append(mounted, slave.MountedRoot{
					Path:      strings.TrimSpace(pathValue),
					MountPath: strings.TrimSpace(mountValue),
				})
			}
		}
	}
	return roots, mounted
}

func stringSliceFromCfg(m map[string]interface{}, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func stringFromCfg(m map[string]interface{}, key, def string) string {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	s, ok := v.(string)
	if !ok {
		return def
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
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
	return configuredSectionDirs(cfg)
}

func configuredBootstrapDirs(cfg *core.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string, skipWritablePluginRoots bool) {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "*?[]") {
			return
		}
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		p = path.Clean(p)
		if p == "." || p == "/" || seen[p] {
			return
		}
		if skipWritablePluginRoots && isWritablePluginRoot(cfg, p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, section := range cfg.Sections {
		add(section, true)
	}
	if daemonPluginEnabled(cfg, "pre") {
		preCfg := cfg.Plugins["pre"]
		base := "/PRE"
		if configuredBase, ok := preCfg["base"].(string); ok && strings.TrimSpace(configuredBase) != "" {
			base = configuredBase
		}
		add(base, false)
	}
	if daemonPluginEnabled(cfg, "request") {
		requestCfg := cfg.Plugins["request"]
		if _, pinned := pluginStorageSlave(requestCfg); !pinned {
			dir := "/REQUESTS"
			if configuredDir, ok := requestCfg["dir"].(string); ok && strings.TrimSpace(configuredDir) != "" {
				dir = configuredDir
			}
			add(dir, false)
		}
	}
	if daemonPluginEnabled(cfg, "speedtest") {
		speedtestCfg := cfg.Plugins["speedtest"]
		dir := "/SPEEDTEST"
		if configuredDir, ok := speedtestCfg["dir"].(string); ok && strings.TrimSpace(configuredDir) != "" {
			dir = configuredDir
		}
		add(dir, false)
	}
	return out
}

func daemonPluginEnabled(cfg *core.Config, name string) bool {
	if cfg == nil || cfg.Plugins == nil {
		return false
	}
	pluginCfg := cfg.Plugins[strings.ToLower(strings.TrimSpace(name))]
	if pluginCfg == nil {
		return false
	}
	enabled, ok := pluginCfg["enabled"].(bool)
	return ok && enabled
}

func isDisabledPluginOwnedSection(cfg *core.Config, p string) bool {
	clean := path.Clean("/" + strings.TrimSpace(p))
	switch clean {
	case "/PRE":
		return !daemonPluginEnabled(cfg, "pre")
	case "/REQUESTS":
		return !daemonPluginEnabled(cfg, "request")
	case "/SPEEDTEST":
		return !daemonPluginEnabled(cfg, "speedtest")
	default:
		return false
	}
}

func isWritablePluginRoot(cfg *core.Config, p string) bool {
	clean := path.Clean("/" + strings.TrimSpace(p))
	if daemonPluginEnabled(cfg, "request") {
		requestCfg := cfg.Plugins["request"]
		dir := "/REQUESTS"
		if configuredDir, ok := requestCfg["dir"].(string); ok && strings.TrimSpace(configuredDir) != "" {
			dir = configuredDir
		}
		if clean == path.Clean("/"+strings.TrimSpace(dir)) {
			return true
		}
	}
	if daemonPluginEnabled(cfg, "speedtest") {
		speedtestCfg := cfg.Plugins["speedtest"]
		dir := "/SPEEDTEST"
		if configuredDir, ok := speedtestCfg["dir"].(string); ok && strings.TrimSpace(configuredDir) != "" {
			dir = configuredDir
		}
		if clean == path.Clean("/"+strings.TrimSpace(dir)) {
			return true
		}
	}
	return false
}

func pluginStorageSlave(pluginCfg map[string]interface{}) (string, bool) {
	if pluginCfg == nil {
		return "", false
	}
	raw, ok := pluginCfg["storage_slave"]
	if !ok {
		return "", false
	}
	slave, ok := raw.(string)
	if !ok {
		return "", false
	}
	slave = strings.TrimSpace(slave)
	return slave, slave != ""
}

func configuredSectionDirs(cfg *core.Config) []string {
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
		if isWritablePluginRoot(cfg, p) {
			return
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
	for _, section := range cfg.Sections {
		if isDisabledPluginOwnedSection(cfg, section) {
			continue
		}
		add(section)
	}
	if daemonPluginEnabled(cfg, "dateddirs") {
		datedCfg := cfg.Plugins["dateddirs"]
		for _, section := range stringSliceFromPluginConfig(datedCfg["sections"]) {
			add(section)
		}
	}
	if daemonPluginEnabled(cfg, "pre") {
		preCfg := cfg.Plugins["pre"]
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
	if daemonPluginEnabled(cfg, "request") {
		requestCfg := cfg.Plugins["request"]
		if dir, ok := requestCfg["dir"].(string); ok && strings.TrimSpace(dir) != "" {
			add(dir)
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
