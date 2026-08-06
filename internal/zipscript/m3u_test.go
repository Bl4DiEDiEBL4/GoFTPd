package zipscript

import "testing"

func TestBuildReleaseM3UFromSFV(t *testing.T) {
	cfg := Config{Enabled: true}
	cfg.Audio.Extensions = []string{"mp3", "flac"}

	m3uName, body, ok := BuildReleaseM3U(cfg, "Release.SFV", []string{
		"02-Track.MP3",
		"03-Track.FLAC",
		"cover.jpg",
		"01-track.mp3",
		"notlisted.mp3",
	}, map[string]uint32{
		"01-track.mp3":  0x11111111,
		"02-track.mp3":  0x22222222,
		"03-track.flac": 0x33333333,
		"cover.jpg":     0x44444444,
	})
	if !ok {
		t.Fatalf("expected m3u to be generated")
	}
	if m3uName != "Release.m3u" {
		t.Fatalf("m3u name = %q, want Release.m3u", m3uName)
	}
	if string(body) != "01-track.mp3\r\n02-Track.MP3\r\n03-Track.FLAC\r\n" {
		t.Fatalf("m3u body = %q", string(body))
	}
}

func TestBuildReleaseM3USkipsExistingPlaylist(t *testing.T) {
	cfg := Config{Enabled: true}
	cfg.Audio.Extensions = []string{"mp3"}

	_, _, ok := BuildReleaseM3U(cfg, "release.sfv", []string{
		"01-track.mp3",
		"release.M3U",
	}, map[string]uint32{
		"01-track.mp3": 0x11111111,
	})
	if ok {
		t.Fatalf("expected existing m3u to disable generation")
	}
}

func TestCreateM3UEnabled(t *testing.T) {
	cfg := Config{Enabled: true}
	cfg.Audio.Sections = []string{"MP3", "FLAC"}

	if !CreateM3UEnabled(cfg, "/MP3/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected MP3 section to generate m3u")
	}
	if !CreateM3UEnabled(cfg, "/FLAC/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected FLAC section to generate m3u")
	}
	if CreateM3UEnabled(cfg, "/TV-720P/Show.S01E01-GRP") {
		t.Fatalf("expected non-audio section to skip m3u")
	}

	cfg.Audio.Sections = []string{"LOSSLESS"}
	if !CreateM3UEnabled(cfg, "/LOSSLESS/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected configured audio section to generate m3u")
	}
	if CreateM3UEnabled(cfg, "/FLAC/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected unconfigured FLAC section to skip m3u")
	}

	disabled := false
	cfg.Audio.CreateM3U = &disabled
	cfg.Audio.Sections = []string{"MP3", "FLAC"}
	if CreateM3UEnabled(cfg, "/MP3/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected explicit create_m3u=false to skip generation")
	}
	if CreateM3UEnabled(cfg, "/FLAC/0618/Artist-Album-2026-GRP") {
		t.Fatalf("expected explicit create_m3u=false to skip FLAC generation")
	}
}
