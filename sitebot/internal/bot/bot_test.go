package bot

import (
	"os"
	"path/filepath"
	"testing"

	"goftpd/sitebot/internal/irc"
)

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

func TestCommandEventFromPrivmsgDecryptsNegotiatedPMKey(t *testing.T) {
	cfg := &Config{
		IRC: IRCConfig{Nick: "GoSitebot"},
	}
	b := &Bot{
		Config: cfg,
		IRC:    irc.NewBot("irc.example.net", 6697, "GoSitebot", "sitebot", "GoSitebot"),
	}
	if err := b.IRC.SetPrivateUserKey("Alice", "cbc:SuperSecretPMKey123"); err != nil {
		t.Fatalf("SetPrivateUserKey: %v", err)
	}
	enc := b.IRC.PMKeys["alice"]
	line := ":Alice!user@example PRIVMSG GoSitebot :+OK *" + enc.Encrypt("!help")
	evt := b.commandEventFromPrivmsg(line)
	if evt == nil {
		t.Fatalf("expected decrypted command event")
	}
	if got := evt.Data["command"]; got != "help" {
		t.Fatalf("command = %q, want %q", got, "help")
	}
	if got := evt.User; got != "Alice" {
		t.Fatalf("user = %q, want %q", got, "Alice")
	}
}

func TestLoadConfigPreservesAnnouncePretimeConfig(t *testing.T) {
	tmp := t.TempDir()
	announceCfg := filepath.Join(tmp, "announce.yml")
	mainCfg := filepath.Join(tmp, "sitebot.yml")

	if err := os.WriteFile(announceCfg, []byte("default_channel: \"#goftpd\"\npretime:\n  mode: \"inline\"\n"), 0o644); err != nil {
		t.Fatalf("write announce config: %v", err)
	}
	if err := os.WriteFile(mainCfg, []byte("irc:\n  host: \"irc.example.net\"\n  port: 6697\n  nick: \"GoSitebot\"\n  user: \"sitebot\"\n  realname: \"GoSitebot\"\nannounce:\n  config_file: \""+filepath.Base(announceCfg)+"\"\nplugins:\n  enabled:\n    Announce: true\n  config: {}\n"), 0o644); err != nil {
		t.Fatalf("write main config: %v", err)
	}

	cfg, err := LoadConfig(mainCfg)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	pretime, ok := cfg.Announce.Pretime["mode"].(string)
	if !ok || pretime != "inline" {
		t.Fatalf("announce.pretime.mode = %#v, want inline", cfg.Announce.Pretime["mode"])
	}
}
