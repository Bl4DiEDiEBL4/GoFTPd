package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"goftpd/internal/acl"
	"goftpd/internal/core"
	"goftpd/internal/dupe"
	"goftpd/internal/master"
	"goftpd/internal/plugin"
	"goftpd/internal/slave"
	"goftpd/plugins/imdb"
	"goftpd/plugins/tvmaze"
)

func main() {
	// 1. Load Server Config from etc/config.yml
	cfg, err := core.LoadConfig("etc/config.yml")
	if err != nil {
		log.Fatalf("Failed to load etc/config.yml: %v", err)
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
	if cfg.Mode == "master" {
		sm := master.NewSlaveManager(
			cfg.Master["listen_host"].(string),
			intFromCfg(cfg.Master, "control_port", 1099),
			cfg.TLSEnabled,
			cfg.TLSCert,
			cfg.TLSKey,
		)
		if err := sm.Start(); err != nil {
			log.Fatalf("SlaveManager failed: %v", err)
		}

		// Create Bridge (implements core.MasterBridge) and inject into config
		// so the FTP session can route STOR/RETR/LIST/DELE to slaves
		bridge := master.NewBridge(sm)
		cfg.MasterManager = bridge

		log.Printf("[MASTER] SlaveManager listening on port %d, waiting for slaves...",
			intFromCfg(cfg.Master, "control_port", 1099))
	}

	// 7. Initialize Plugin System
	if cfg.Debug {
		log.Printf("[PLUGINS] Initializing plugin system...")
	}
	cfg.PluginManager = core.NewPluginManager(cfg.Debug)

	// 7a. Dynamically load plugins from config
	if cfg.Plugins == nil {
		cfg.Plugins = make(map[string]map[string]interface{})
	}

	for pluginName, pluginCfg := range cfg.Plugins {
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

		// Inject the Master's FileSystem Bridge into Zipscript!


		var p plugin.Plugin

		switch pluginName {
		// zipscript removed - handled by master VFS
		case "tvmaze":
			p = tvmaze.New()
		case "imdb":
			p = imdb.New()
		default:
			if cfg.Debug {
				log.Printf("[PLUGINS] Unknown plugin: %s", pluginName)
			}
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
	if err := cfg.PluginManager.InitializePlugins(cfg.Plugins); err != nil {
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

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
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