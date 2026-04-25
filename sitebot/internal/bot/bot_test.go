package bot

import "testing"

func TestResolveSectionTreatsForeignChildAsSection(t *testing.T) {
	b := &Bot{
		Config: &Config{
			Sections: []SectionRoute{
				{Name: "TV-NL", Paths: []string{"/FOREIGN/TV-NL/*"}},
			},
		},
	}

	if got := b.resolveSection("/FOREIGN/TV-NL", "FOREIGN"); got != "TV-NL" {
		t.Fatalf("resolveSection(%q) = %q, want %q", "/FOREIGN/TV-NL", got, "TV-NL")
	}

	if got := b.resolveSection("/FOREIGN/TV-NL/Some.Release-GRP", "FOREIGN"); got != "TV-NL" {
		t.Fatalf("resolveSection(%q) = %q, want %q", "/FOREIGN/TV-NL/Some.Release-GRP", got, "TV-NL")
	}
}

func TestPathMatchesIncludesSectionDirectoryForWildcardChildPattern(t *testing.T) {
	if !pathMatches("/FOREIGN/TV-NL/*", "/FOREIGN/TV-NL") {
		t.Fatalf("expected wildcard child pattern to match the section directory itself")
	}
}
