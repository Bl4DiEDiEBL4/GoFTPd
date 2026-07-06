package main

import (
	"testing"

	"weaveftpd/internal/core"
)

func TestConfigPathFromArgsDefaultsToEtcConfig(t *testing.T) {
	got, err := configPathFromArgs(nil)
	if err != nil {
		t.Fatalf("configPathFromArgs() error = %v", err)
	}
	if got != "etc/config.yml" {
		t.Fatalf("configPathFromArgs() = %q, want etc/config.yml", got)
	}
}

func TestConfigPathFromArgsAcceptsConfigFlag(t *testing.T) {
	got, err := configPathFromArgs([]string{"--config", "config-slave-example.yml"})
	if err != nil {
		t.Fatalf("configPathFromArgs() error = %v", err)
	}
	if got != "config-slave-example.yml" {
		t.Fatalf("configPathFromArgs() = %q, want config-slave-example.yml", got)
	}
}

func TestConfiguredSectionDirsSkipsWritablePluginRoots(t *testing.T) {
	cfg := &core.Config{
		Sections: []string{"/X265", "/REQUESTS", "/SPEEDTEST"},
		Plugins: map[string]map[string]interface{}{
			"request":   {"enabled": true},
			"speedtest": {"enabled": true},
		},
	}

	sections := configuredSectionDirs(cfg)
	if containsString(sections, "/REQUESTS") {
		t.Fatalf("expected /REQUESTS to stay out of protected section dirs: %#v", sections)
	}
	if containsString(sections, "/SPEEDTEST") {
		t.Fatalf("expected /SPEEDTEST to stay out of protected section dirs: %#v", sections)
	}
	if !containsString(sections, "/X265") {
		t.Fatalf("expected regular section to remain protected: %#v", sections)
	}

	bootstrap := configuredBootstrapDirs(cfg)
	if !containsString(bootstrap, "/REQUESTS") {
		t.Fatalf("expected /REQUESTS to remain a bootstrap dir: %#v", bootstrap)
	}
	if !containsString(bootstrap, "/SPEEDTEST") {
		t.Fatalf("expected /SPEEDTEST to remain a bootstrap dir: %#v", bootstrap)
	}
}

func TestConfiguredBootstrapDirsSkipsPinnedRequestStorage(t *testing.T) {
	cfg := &core.Config{
		Sections: []string{"/X265", "/REQUESTS"},
		Plugins: map[string]map[string]interface{}{
			"request": {
				"enabled":       true,
				"storage_slave": "LOCAL",
			},
		},
	}

	bootstrap := configuredBootstrapDirs(cfg)
	if containsString(bootstrap, "/REQUESTS") {
		t.Fatalf("expected pinned /REQUESTS storage to be created by the plugin only: %#v", bootstrap)
	}
	if !containsString(bootstrap, "/X265") {
		t.Fatalf("expected regular section bootstrap dir to remain: %#v", bootstrap)
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
