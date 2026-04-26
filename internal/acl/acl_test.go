package acl

import (
	"os"
	"path/filepath"
	"testing"

	"goftpd/internal/user"
)

func TestLoadStructuredYAMLRules(t *testing.T) {
	data := []byte(`
roles:
  anyone:
    anyone: true
  staff:
    any_groups: ["STAFF", "SiteOP"]
  siteop:
    all_flags: ["1"]
  goftpd_bot:
    users: ["goftpd"]

rules:
  sitecmd:
    - allow: [HELP, AFFILS, PRE]
      required: $anyone
    - allow: [WHO, SWHO]
      required: $staff
    - allow: [REHASH]
      required: $siteop
    - deny: ["*"]
      required:
        nobody: true
  nuke:
    - path: /site/*
      required: $goftpd_bot
`)

	e := &Engine{RulesByType: map[string][]Rule{}}
	if loaded, err := loadYAMLRules(e, data); err != nil || !loaded {
		t.Fatalf("loadYAMLRules() = (%v, %v), want (true, nil)", loaded, err)
	}

	if got := len(e.RulesByType["sitecmd"]); got != 7 {
		t.Fatalf("len(sitecmd rules) = %d, want 7", got)
	}
	if got := len(e.RulesByType["nuke"]); got != 1 {
		t.Fatalf("len(nuke rules) = %d, want 1", got)
	}

	anyUser := &user.User{Name: "tester"}
	staffUser := &user.User{Name: "staffer", PrimaryGroup: "STAFF", Groups: map[string]int{"STAFF": 0}}
	siteopUser := &user.User{Name: "siteop", Flags: "1"}
	goftpdBot := &user.User{Name: "goftpd"}

	if !e.CanPerformRuleOnly(anyUser, "sitecmd", "AFFILS") {
		t.Fatal("public AFFILS rule should allow any user")
	}
	if e.CanPerformRuleOnly(anyUser, "sitecmd", "WHO") {
		t.Fatal("WHO should not be public")
	}
	if !e.CanPerformRuleOnly(staffUser, "sitecmd", "WHO") {
		t.Fatal("WHO should allow staff user")
	}
	if !e.CanPerformRuleOnly(siteopUser, "sitecmd", "REHASH") {
		t.Fatal("REHASH should allow siteop user")
	}
	if !e.CanPerform(goftpdBot, "NUKE", "/site/MP3/Release-GRP") {
		t.Fatal("goftpd bot should be allowed to nuke in structured config")
	}
}

func TestStructuredRequirementSupportsAllGroupsAndAnyFlags(t *testing.T) {
	req := &Requirement{
		AllGroups: []string{"Admin", "NUKERS"},
		AnyFlags:  []string{"A", "B"},
	}

	allowed := &user.User{
		Name:         "allowed",
		Flags:        "B",
		PrimaryGroup: "Admin",
		Groups:       map[string]int{"Admin": 0, "NUKERS": 0},
	}
	deniedMissingGroup := &user.User{
		Name:         "denied-group",
		Flags:        "A",
		PrimaryGroup: "Admin",
		Groups:       map[string]int{"Admin": 0},
	}
	deniedMissingFlag := &user.User{
		Name:         "denied-flag",
		Flags:        "1",
		PrimaryGroup: "Admin",
		Groups:       map[string]int{"Admin": 0, "NUKERS": 0},
	}

	if !checkRequirement(req, allowed) {
		t.Fatal("checkRequirement() should allow user matching all groups and any flag")
	}
	if checkRequirement(req, deniedMissingGroup) {
		t.Fatal("checkRequirement() should reject user missing one required group")
	}
	if checkRequirement(req, deniedMissingFlag) {
		t.Fatal("checkRequirement() should reject user missing any required flag")
	}
}

func TestStructuredRequirementSupportsAnyOfAcrossFlagsAndGroups(t *testing.T) {
	req := &Requirement{
		AnyOf: []*Requirement{
			{AllFlags: []string{"1"}},
			{AllGroups: []string{"NUKERS"}},
		},
	}

	flagUser := &user.User{Name: "flag-user", Flags: "1"}
	groupUser := &user.User{
		Name:         "group-user",
		PrimaryGroup: "NUKERS",
		Groups:       map[string]int{"NUKERS": 0},
	}
	denied := &user.User{Name: "denied", Flags: "3"}

	if !checkRequirement(req, flagUser) {
		t.Fatal("any_of should allow user with matching flag branch")
	}
	if !checkRequirement(req, groupUser) {
		t.Fatal("any_of should allow user with matching group branch")
	}
	if checkRequirement(req, denied) {
		t.Fatal("any_of should reject user matching no branch")
	}
}

func TestLoadStructuredYAMLRulesWithAnyOf(t *testing.T) {
	data := []byte(`
roles:
  anyone:
    anyone: true

rules:
  nuke:
    - path: /site/*
      required:
        any_of:
          - all_flags: ["1"]
          - all_groups: ["NUKERS"]
`)

	e := &Engine{RulesByType: map[string][]Rule{}}
	if loaded, err := loadYAMLRules(e, data); err != nil || !loaded {
		t.Fatalf("loadYAMLRules() = (%v, %v), want (true, nil)", loaded, err)
	}

	flagUser := &user.User{Name: "flag-user", Flags: "1"}
	groupUser := &user.User{
		Name:         "group-user",
		PrimaryGroup: "NUKERS",
		Groups:       map[string]int{"NUKERS": 0},
	}
	denied := &user.User{Name: "denied", Flags: "3"}

	if !e.CanPerform(flagUser, "NUKE", "/site/MP3/Release-GRP") {
		t.Fatal("structured any_of should allow matching flag branch")
	}
	if !e.CanPerform(groupUser, "NUKE", "/site/MP3/Release-GRP") {
		t.Fatal("structured any_of should allow matching group branch")
	}
	if e.CanPerform(denied, "NUKE", "/site/MP3/Release-GRP") {
		t.Fatal("structured any_of should reject unmatched user")
	}
}

func TestMatchesRulePathCoversPrivatePathDescendants(t *testing.T) {
	e := &Engine{RulesByType: map[string][]Rule{
		"privpath": {
			{Type: "privpath", Path: "/site/PRE/GROUP", Required: "=GROUP =SiteOP"},
			{Type: "privpath", Path: "/site/LINKS/*", Required: "1"},
		},
	}}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "exact", path: "/site/PRE/GROUP", want: true},
		{name: "child release", path: "/site/PRE/GROUP/Release-GRP", want: true},
		{name: "nested file", path: "/site/PRE/GROUP/Release-GRP/file.rar", want: true},
		{name: "sibling group", path: "/site/PRE/OTHER/Release-GRP", want: false},
		{name: "wildcard", path: "/site/LINKS/something", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.MatchesRulePath("privpath", tt.path)
			if got != tt.want {
				t.Fatalf("MatchesRulePath(privpath, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCanPerformCoversExactPathDescendants(t *testing.T) {
	e := &Engine{RulesByType: map[string][]Rule{
		"dirlog": {
			{Type: "dirlog", Path: "/site/MP3", Required: "*"},
			{Type: "dirlog", Path: "/site/PRE/GROUP", Required: "=GROUP =SiteOP"},
		},
	}}

	u := &user.User{
		Name:         "tester",
		PrimaryGroup: "GROUP",
		Groups:       map[string]int{"GROUP": 0},
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "exact root", path: "/site/MP3", want: true},
		{name: "dated dir", path: "/site/MP3/0424", want: true},
		{name: "release below exact rule", path: "/site/MP3/0424/Artist-Album-2026-GRP", want: true},
		{name: "private exact path descendant", path: "/site/PRE/GROUP/Release-GRP", want: true},
		{name: "private nested descendant", path: "/site/PRE/GROUP/Release-GRP/file.rar", want: true},
		{name: "outside subtree", path: "/site/FLAC/0424/Artist-Album-2026-GRP", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := e.CanPerform(u, "DIRLOG", tt.path)
			if got != tt.want {
				t.Fatalf("CanPerform(DIRLOG, %q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestLoadEngineRejectsInvalidYAMLPermissionsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.yml")
	data := []byte("roles:\n\tanyone:\n\t  anyone: true\n\nrules:\n\tsitecmd:\n\t  - allow: [HELP]\n\t    required: $anyone\n")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := LoadEngine(path)
	if err == nil {
		t.Fatal("LoadEngine() error = nil, want invalid YAML error")
	}
}
