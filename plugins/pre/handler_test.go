package pre

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestSyncAffilPermissionsAddsGeneratedPreRules(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "permissions.yml")
	input := `roles:
  anyone:
    anyone: true
  siteop:
    any_of:
      - all_groups: ["SiteOP"]
      - all_flags: ["1"]

rules:
  privpath:
    - path: /LINKS
      required: $siteop
  list:
    - path: /PRE/*
      required: $siteop
    - path: /*
      required: $anyone
  dirlog:
    - path: /PRE/*
      required: $siteop
    - path: /*
      required: $anyone
`
	if err := os.WriteFile(filePath, []byte(input), 0644); err != nil {
		t.Fatalf("write permissions file: %v", err)
	}

	affils := []AffilRule{
		{
			Group:  "iND",
			Predir: "/PRE/iND",
			Permissions: map[string]interface{}{
				"acl_path": "/PRE/iND",
				"privpath": true,
				"list":     true,
				"dirlog":   true,
			},
		},
	}

	if err := syncAffilPermissions(filePath, "/", affils); err != nil {
		t.Fatalf("syncAffilPermissions failed: %v", err)
	}

	doc := loadYAMLDocumentForTest(t, filePath)
	rules := mappingValueForTest(t, doc.Content[0], "rules")

	privpath := mappingValueForTest(t, rules, "privpath")
	if !sequenceHasGeneratedPath(privpath, "/PRE/iND") {
		t.Fatalf("expected generated privpath rule for /PRE/iND")
	}

	list := mappingValueForTest(t, rules, "list")
	if !sequenceHasGeneratedPath(list, "/PRE/iND") {
		t.Fatalf("expected generated list rule for /PRE/iND")
	}
	if firstPathInSequence(list) != "/PRE/iND" {
		t.Fatalf("expected generated list rule before PRE catch-all, got first path %q", firstPathInSequence(list))
	}

	dirlog := mappingValueForTest(t, rules, "dirlog")
	if !sequenceHasGeneratedPath(dirlog, "/PRE/iND") {
		t.Fatalf("expected generated dirlog rule for /PRE/iND")
	}
	if firstPathInSequence(dirlog) != "/PRE/iND" {
		t.Fatalf("expected generated dirlog rule before PRE catch-all, got first path %q", firstPathInSequence(dirlog))
	}
}

func TestSyncAffilPermissionsRemovesStaleGeneratedRules(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "permissions.yml")
	input := `rules:
  privpath: []
  list:
    - path: /PRE/*
      required: $siteop
  dirlog:
    - path: /PRE/*
      required: $siteop
`
	if err := os.WriteFile(filePath, []byte(input), 0644); err != nil {
		t.Fatalf("write permissions file: %v", err)
	}

	affils := []AffilRule{
		{Group: "iND", Predir: "/PRE/iND", Permissions: map[string]interface{}{"acl_path": "/PRE/iND"}},
		{Group: "GRPTST", Predir: "/PRE/GRPTST", Permissions: map[string]interface{}{"acl_path": "/PRE/GRPTST"}},
	}
	if err := syncAffilPermissions(filePath, "/", affils); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	affils = affils[:1]
	if err := syncAffilPermissions(filePath, "/", affils); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read permissions file: %v", err)
	}
	text := string(content)
	if strings.Contains(text, "/PRE/GRPTST") {
		t.Fatalf("expected stale generated GRPTST rules to be removed")
	}
	if !strings.Contains(text, "/PRE/iND") {
		t.Fatalf("expected iND rules to remain after sync")
	}
}

func TestNormalizeAffilsSupportsPredirsSectionsAndReward(t *testing.T) {
	in := []AffilRule{
		{
			Group:           "GRP1",
			Predir:          "/groups/GRP1",
			Predirs:         []string{"/groups/GRP1", "/groups/GRP1ALT"},
			AllowedSections: []string{"MP3", "FLAC"},
			CreditRatio:     3,
		},
	}
	got := normalizeAffils(in, "/PRE")
	if len(got) != 1 {
		t.Fatalf("expected 1 affil, got %d", len(got))
	}
	if got[0].Predir != "/groups/GRP1" {
		t.Fatalf("expected primary predir /groups/GRP1, got %q", got[0].Predir)
	}
	if len(got[0].Predirs) != 2 || got[0].Predirs[1] != "/groups/GRP1ALT" {
		t.Fatalf("expected normalized predirs, got %#v", got[0].Predirs)
	}
	if len(got[0].AllowedSections) != 2 || got[0].AllowedSections[0] != "MP3" {
		t.Fatalf("expected allowed sections to survive normalization, got %#v", got[0].AllowedSections)
	}
	if got[0].CreditRatio != 3 {
		t.Fatalf("expected credit ratio 3, got %d", got[0].CreditRatio)
	}
}

func TestResolveSectionCanonicalizesConfiguredCase(t *testing.T) {
	p := &Plugin{sections: []string{"TV-1080P", "/MP3", "/0DAY"}}
	section, dir, ok := p.resolveSection("mp3")
	if !ok {
		t.Fatalf("expected MP3 section to resolve")
	}
	if section != "MP3" || dir != "/MP3" {
		t.Fatalf("expected canonical MP3 -> /MP3, got section=%q dir=%q", section, dir)
	}
}

func TestAffilACLPathsIncludesAllPredirs(t *testing.T) {
	affil := AffilRule{
		Group:   "GRP1",
		Predir:  "/groups/GRP1",
		Predirs: []string{"/groups/GRP1", "/groups/GRP1ALT"},
	}
	got := affilACLPaths(affil, "/")
	if len(got) != 2 {
		t.Fatalf("expected 2 ACL paths, got %#v", got)
	}
	if got[0] != "/groups/GRP1" || got[1] != "/groups/GRP1ALT" {
		t.Fatalf("unexpected ACL paths: %#v", got)
	}
}

func TestFormatDateDirForPreMatchesDateddirsTokens(t *testing.T) {
	when := time.Date(2026, time.April, 28, 12, 0, 0, 0, time.UTC)
	if got := formatDateDirForPre(when, "MMDD"); got != "0428" {
		t.Fatalf("MMDD = %q, want 0428", got)
	}
	if got := formatDateDirForPre(when, "YYYY-MM-DD"); got != "2026-04-28" {
		t.Fatalf("YYYY-MM-DD = %q, want 2026-04-28", got)
	}
}

func TestBuildMusicPreSuffix(t *testing.T) {
	if got := buildMusicPreSuffix("House", "2026"); got != " :: House 2026" {
		t.Fatalf("buildMusicPreSuffix = %q", got)
	}
}

func TestBuildMusicPreHead(t *testing.T) {
	if got := buildMusicPreHead("Some Artist", "Album Title", "Rel-GRP"); got != "Some Artist - Album Title" {
		t.Fatalf("buildMusicPreHead full = %q", got)
	}
	if got := buildMusicPreHead("", "Album Title", "Rel-GRP"); got != "Album Title" {
		t.Fatalf("buildMusicPreHead title-only = %q", got)
	}
	if got := buildMusicPreHead("", "", "Rel-GRP"); got != "Rel-GRP" {
		t.Fatalf("buildMusicPreHead fallback = %q", got)
	}
}

func TestIsMusicPreMeta(t *testing.T) {
	if isMusicPreMeta(map[string]string{"genre": "N/A", "year": "N/A", "bitrate": "N/A"}) {
		t.Fatal("expected all-N/A metadata to be ignored")
	}
	if !isMusicPreMeta(map[string]string{"artist": "Some Artist", "bitrate": "320kbps"}) {
		t.Fatal("expected real metadata to count as music PRE info")
	}
}

func TestIsMoviePreMeta(t *testing.T) {
	if isMoviePreMeta(map[string]string{"director": "N/A", "rating": "N/A"}) {
		t.Fatal("expected all-N/A movie metadata to be ignored")
	}
	if !isMoviePreMeta(map[string]string{"director": "J. Michael Muro"}) {
		t.Fatal("expected real movie metadata to count")
	}
}

func TestIsTVPreMeta(t *testing.T) {
	if isTVPreMeta(map[string]string{"episode": "N/A", "network": "N/A"}) {
		t.Fatal("expected all-N/A tv metadata to be ignored")
	}
	if !isTVPreMeta(map[string]string{"episode": "S01E01 - Pilot"}) {
		t.Fatal("expected real tv metadata to count")
	}
}

func TestBuildMoviePreSuffix(t *testing.T) {
	fields := map[string]string{
		"title":  "The Boxer",
		"year":   "2009",
		"genre":  "Action, Drama, Sport",
		"rating": "4.9/10 (613 votes)",
	}
	if got := buildMoviePreSuffix(fields, "The.Boxer.2009.MULTi.COMPLETE.BLURAY-PRAWN"); got != " :: The Boxer (2009) :: Action, Drama, Sport :: 4.9/10 (613 votes)" {
		t.Fatalf("buildMoviePreSuffix = %q", got)
	}
}

func loadYAMLDocumentForTest(t *testing.T, filePath string) *yaml.Node {
	t.Helper()
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read yaml document: %v", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal yaml document: %v", err)
	}
	return &doc
}

func mappingValueForTest(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		t.Fatalf("expected mapping node for %q", key)
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if strings.EqualFold(strings.TrimSpace(mapping.Content[i].Value), key) {
			return mapping.Content[i+1]
		}
	}
	t.Fatalf("could not find key %q", key)
	return nil
}

func sequenceHasGeneratedPath(seq *yaml.Node, wantPath string) bool {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, entry := range seq.Content {
		if strings.EqualFold(strings.TrimSpace(mappingScalarValue(entry, "generated_by")), "pre") &&
			strings.EqualFold(strings.TrimSpace(mappingScalarValue(entry, "path")), wantPath) {
			return true
		}
	}
	return false
}

func firstPathInSequence(seq *yaml.Node) string {
	if seq == nil || seq.Kind != yaml.SequenceNode || len(seq.Content) == 0 {
		return ""
	}
	return strings.TrimSpace(mappingScalarValue(seq.Content[0], "path"))
}
