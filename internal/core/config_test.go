package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestLoadConfigRejectsMissingSlaveHost(t *testing.T) {
	path := writeConfigFixture(t, `
sitename_long: "GoFTPd"
sitename_short: "GoFTPd"
version: "1.0.6b"
timezone: "Europe/Amsterdam"
mode: "slave"
storage_path: "./site"
acl_base_path: "/"
tls_enabled: false
slave:
  name: "SLAVE1"
`)

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "slave.master_host is required") {
		t.Fatalf("LoadConfig() error = %v, want slave.master_host validation error", err)
	}
}

func TestLoadConfigRejectsTLSRequirementWithoutTLS(t *testing.T) {
	path := writeConfigFixture(t, `
sitename_long: "GoFTPd"
sitename_short: "GoFTPd"
version: "1.0.6b"
timezone: "Europe/Amsterdam"
mode: "master"
listen_port: 21
storage_path: "./site"
acl_base_path: "/"
tls_enabled: false
require_tls_control: true
master:
  listen_host: "0.0.0.0"
  control_port: 1099
`)

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "require_tls_control needs tls_enabled: true") {
		t.Fatalf("LoadConfig() error = %v, want require_tls_control validation error", err)
	}
}

func TestLoadConfigSlaveIgnoresMissingPluginConfigFiles(t *testing.T) {
	path := writeConfigFixture(t, `
sitename_long: "GoFTPd"
sitename_short: "GoFTPd"
version: "1.0.6b"
timezone: "Europe/Amsterdam"
mode: "slave"
storage_path: "./site"
acl_base_path: "/"
tls_enabled: false
slave:
  name: "SLAVE1"
  master_host: "127.0.0.1"
  master_port: 1099
plugins:
  autonuke:
    enabled: true
    config_file: "plugins/autonuke/config.yml"
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want slave config to load without plugin config files", err)
	}
	if cfg.Mode != "slave" {
		t.Fatalf("LoadConfig() mode = %q, want slave", cfg.Mode)
	}
}
