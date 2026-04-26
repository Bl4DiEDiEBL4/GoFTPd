package zipscript

import "testing"

func TestUsesReleaseCheckEntryRequiresExactReleaseDepth(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			ReleaseCheck: []string{"/APPS/*", "/MP3/*/*"},
		},
	}

	if UsesReleaseCheckEntry(cfg, "/APPS") {
		t.Fatalf("section root should not count as a release entry")
	}
	if !UsesReleaseCheckEntry(cfg, "/APPS/Some.Release-GRP") {
		t.Fatalf("direct release path should match")
	}
	if UsesReleaseCheckEntry(cfg, "/APPS/Some.Release-GRP/CD1") {
		t.Fatalf("descendant path should not count as the release entry itself")
	}
	if UsesReleaseCheckEntry(cfg, "/MP3/0424") {
		t.Fatalf("dated container should not count as a release entry")
	}
	if !UsesReleaseCheckEntry(cfg, "/MP3/0424/Artist-Album-2026-GRP") {
		t.Fatalf("dated release path should match")
	}
}

func TestAudioSortLinksSeparateBySectionByDefault(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Audio: AudioConfig{
			Enabled:    true,
			GenrePath:  "/music.by.genre",
			ArtistPath: "/music.by.artist",
			YearPath:   "/music.by.year",
			GroupPath:  "/music.by.group",
			Sort: AudioSortConfig{
				Genre:  true,
				Artist: true,
				Year:   true,
				Group:  true,
			},
		},
	}
	cfg.ApplyDefaults()
	fields := map[string]string{
		"genre":  "House",
		"artist": "Some Artist",
		"year":   "2026",
	}

	links := AudioSortLinks(cfg, "/MP3/0426/Artist-Album-2026-GRP", fields)
	want := map[string]bool{
		"/music.by.genre/MP3/House/Artist-Album-2026-GRP":        true,
		"/music.by.artist/MP3/Some_Artist/Artist-Album-2026-GRP": true,
		"/music.by.year/MP3/2026/Artist-Album-2026-GRP":          true,
		"/music.by.group/MP3/GRP/Artist-Album-2026-GRP":          true,
	}

	if len(links) != len(want) {
		t.Fatalf("len(links) = %d, want %d", len(links), len(want))
	}
	for _, link := range links {
		if !want[link.LinkPath] {
			t.Fatalf("unexpected link path %q", link.LinkPath)
		}
	}
}

func TestAudioSortLinksCanDisableSectionBuckets(t *testing.T) {
	separate := false
	cfg := Config{
		Enabled: true,
		Audio: AudioConfig{
			Enabled:   true,
			GenrePath: "/music.by.genre",
			Sort: AudioSortConfig{
				Genre:             true,
				SeparateBySection: &separate,
			},
		},
	}
	fields := map[string]string{"genre": "House"}

	links := AudioSortLinks(cfg, "/FLAC/0426/Artist-Album-2026-GRP", fields)
	if len(links) != 1 {
		t.Fatalf("len(links) = %d, want 1", len(links))
	}
	if got, want := links[0].LinkPath, "/music.by.genre/House/Artist-Album-2026-GRP"; got != want {
		t.Fatalf("LinkPath = %q, want %q", got, want)
	}
}
