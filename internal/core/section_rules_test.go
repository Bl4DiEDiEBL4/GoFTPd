package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSectionRulesMatchesCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	content := "Welcome to %S, %U.\nUse TLS."
	if err := os.WriteFile(filepath.Join(dir, "TV-720P.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	section, lines, err := loadSectionRules(dir, "/TV-720P")
	if err != nil {
		t.Fatalf("loadSectionRules: %v", err)
	}
	if section != "TV-720P" || len(lines) != 2 || lines[0] != "Welcome to %S, %U." {
		t.Fatalf("unexpected section rules: section=%q lines=%q", section, lines)
	}
}

func TestLoadSectionRulesMatchesNestedForeignSection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "TV-DE.txt"), []byte("German TV rules\n"), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	section, lines, err := loadSectionRules(dir, "/FOREIGN/TV-DE")
	if err != nil {
		t.Fatalf("loadSectionRules: %v", err)
	}
	if section != "TV-DE" || len(lines) != 1 || lines[0] != "German TV rules" {
		t.Fatalf("unexpected foreign section rules: section=%q lines=%q", section, lines)
	}
}

func TestLoadSectionRulesStaysSilentWithoutMatchingFile(t *testing.T) {
	section, lines, err := loadSectionRules(t.TempDir(), "/TV-720P/Some.Release-GRP")
	if err != nil {
		t.Fatalf("loadSectionRules: %v", err)
	}
	if section != "Some.Release-GRP" || len(lines) != 0 {
		t.Fatalf("expected no release-level rules, section=%q lines=%q", section, lines)
	}
}

func TestSectionRulesNameRejectsUnsafeDirectoryName(t *testing.T) {
	if _, ok := sectionRulesName("/TV 720P"); ok {
		t.Fatal("directory names with unsupported characters should not map to config files")
	}
}

func TestExpandSectionRulesReplacesSupportedVariables(t *testing.T) {
	got := expandSectionRules([]string{"Welcome %U to %S on WeaveFTPd %V"}, "TV-720P", "racer", "1.3.0")
	if len(got) != 1 || got[0] != "Welcome racer to TV-720P on WeaveFTPd 1.3.0" {
		t.Fatalf("unexpected expanded rules: %q", got)
	}
}

func TestLoadConfigReadsSectionRulesCWDSettings(t *testing.T) {
	path := writeConfigFixture(t, `
sitename_long: "WeaveFTPd"
sitename_short: "WeaveFTPd"
timezone: "Europe/Amsterdam"
mode: "master"
listen_port: 21
storage_path: "./site"
acl_base_path: "/"
tls_enabled: false
master:
  listen_host: "0.0.0.0"
  control_port: 1099
zipscript:
  enabled: true
  section_rules:
    cwd: true
    directory: "custom/rules"
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Zipscript.SectionRules.CWD || cfg.Zipscript.SectionRules.Directory != "custom/rules" {
		t.Fatalf("unexpected section rules config: %+v", cfg.Zipscript.SectionRules)
	}
}
