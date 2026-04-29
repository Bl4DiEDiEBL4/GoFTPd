package autonuke

import "testing"

func TestLoadConfigNormalizesSectionRoots(t *testing.T) {
	cfg := loadConfig(map[string]interface{}{
		"sections": []interface{}{
			"/GAMES",
			"/FOREIGN/TV-FR",
		},
	})

	if len(cfg.SectionRoots) != 2 {
		t.Fatalf("expected 2 section roots, got %d", len(cfg.SectionRoots))
	}
	if cfg.SectionRoots[0] != "/GAMES" {
		t.Fatalf("unexpected first section root: %q", cfg.SectionRoots[0])
	}
	if cfg.SectionRoots[1] != "/FOREIGN/TV-FR" {
		t.Fatalf("unexpected second section root: %q", cfg.SectionRoots[1])
	}
}

func TestIsSectionRootMatchesNestedForeignSection(t *testing.T) {
	h := &Handler{
		cfg: config{
			SectionRoots: []string{"/GAMES", "/FOREIGN/TV-FR"},
		},
	}

	if !h.isSectionRoot("/GAMES") {
		t.Fatalf("expected /GAMES to be treated as a section root")
	}
	if !h.isSectionRoot("/FOREIGN/TV-FR") {
		t.Fatalf("expected /FOREIGN/TV-FR to be treated as a section root")
	}
	if h.isSectionRoot("/FOREIGN/TV-FR/Some.Release-GRP") {
		t.Fatalf("release dir should not be treated as the section root itself")
	}
}
