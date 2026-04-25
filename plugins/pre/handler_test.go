package pre

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
