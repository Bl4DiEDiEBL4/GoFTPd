package bot

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/irc"
)

type recordingConn struct {
	writes []string
}

func (c *recordingConn) Read(_ []byte) (int, error) { return 0, io.EOF }
func (c *recordingConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, string(b))
	return len(b), nil
}
func (c *recordingConn) Close() error                       { return nil }
func (c *recordingConn) LocalAddr() net.Addr                { return nil }
func (c *recordingConn) RemoteAddr() net.Addr               { return nil }
func (c *recordingConn) SetDeadline(_ time.Time) error      { return nil }
func (c *recordingConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(_ time.Time) error { return nil }

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

func TestRouteChannelsPrefersBestPathMatchOverDuplicateSectionName(t *testing.T) {
	b := &Bot{
		Config: &Config{
			Sections: []SectionRoute{
				{Name: "REQUESTS", Channels: []string{"#weaveftpd-chat"}, Paths: []string{"/REQUESTS/*/*"}},
				{Name: "REQUESTS", Channels: []string{"#weaveftpd"}, Paths: []string{"/REQUESTS_SPECIAL/*/*"}},
			},
		},
	}

	evt := &event.Event{
		Section: "REQUESTS",
		Path:    "/REQUESTS_SPECIAL/Test.Release-GRP/file.r00",
	}

	got := b.routeChannels(evt, "RACE")
	if len(got) != 1 || got[0] != "#weaveftpd" {
		t.Fatalf("routeChannels() = %#v, want %#v", got, []string{"#weaveftpd"})
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

func TestCommandEventFromPrivmsgDecryptsChannelCommandWithPlainCBCKey(t *testing.T) {
	cfg := &Config{
		IRC: IRCConfig{Nick: "GoSitebot"},
	}
	b := &Bot{
		Config: cfg,
		IRC:    irc.NewBot("irc.example.net", 6697, "GoSitebot", "sitebot", "GoSitebot"),
	}
	if err := b.IRC.SetChannelKey("#weaveftpd", "SuperSecretChannelKey123"); err != nil {
		t.Fatalf("SetChannelKey: %v", err)
	}
	enc, err := irc.NewBlowfishEncryptor("cbc:SuperSecretChannelKey123")
	if err != nil {
		t.Fatalf("NewBlowfishEncryptor: %v", err)
	}
	line := ":Alice!user@example PRIVMSG #weaveftpd :+OK *" + enc.Encrypt("!refresh")
	evt := b.commandEventFromPrivmsg(line)
	if evt == nil {
		t.Fatalf("expected decrypted channel command event")
	}
	if got := evt.Data["command"]; got != "refresh" {
		t.Fatalf("command = %q, want %q", got, "refresh")
	}
	if got := evt.Data["channel"]; got != "#weaveftpd" {
		t.Fatalf("channel = %q, want %q", got, "#weaveftpd")
	}
}

func TestCommandEventFromPrivmsgDoesNotPanicOnMalformedEncryptedChannelCommand(t *testing.T) {
	cfg := &Config{
		IRC: IRCConfig{Nick: "GoSitebot"},
	}
	b := &Bot{
		Config: cfg,
		IRC:    irc.NewBot("irc.example.net", 6697, "GoSitebot", "sitebot", "GoSitebot"),
	}
	if err := b.IRC.SetChannelKey("#weaveftpd", "SuperSecretChannelKey123"); err != nil {
		t.Fatalf("SetChannelKey: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("commandEventFromPrivmsg panicked: %v", r)
		}
	}()
	line := ":Alice!user@example PRIVMSG #weaveftpd :+OK *AAAA"
	if evt := b.commandEventFromPrivmsg(line); evt != nil {
		t.Fatalf("expected malformed encrypted line to be ignored")
	}
}

func TestLoadConfigPreservesAnnouncePretimeConfig(t *testing.T) {
	tmp := t.TempDir()
	announceCfg := filepath.Join(tmp, "announce.yml")
	mainCfg := filepath.Join(tmp, "sitebot.yml")

	if err := os.WriteFile(announceCfg, []byte("default_channel: \"#weaveftpd\"\npretime:\n  mode: \"inline\"\n"), 0o644); err != nil {
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

func TestLoadConfigReadsVersionFromAncestorEtcFile(t *testing.T) {
	tmp := t.TempDir()
	rootEtc := filepath.Join(tmp, "etc")
	cfgDir := filepath.Join(tmp, "sitebot", "etc")
	cfgPath := filepath.Join(cfgDir, "config.yml")
	if err := os.MkdirAll(rootEtc, 0o755); err != nil {
		t.Fatalf("MkdirAll(rootEtc): %v", err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(cfgDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootEtc, "version"), []byte("7.7.7\n"), 0o644); err != nil {
		t.Fatalf("write version: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("irc:\n  host: \"irc.example.net\"\n  port: 6697\n  nick: \"GoSitebot\"\n  user: \"sitebot\"\n  realname: \"GoSitebot\"\nannounce:\nplugins:\n  enabled: {}\n  config: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Version != "7.7.7" {
		t.Fatalf("cfg.Version = %q, want %q", cfg.Version, "7.7.7")
	}
	if cfg.IRC.RealName != "GoSitebot v7.7.7" {
		t.Fatalf("cfg.IRC.RealName = %q, want %q", cfg.IRC.RealName, "GoSitebot v7.7.7")
	}
}

func TestOnRegisteredNickServIdentifyBeforeJoin(t *testing.T) {
	conn := &recordingConn{}
	b := &Bot{
		Config: &Config{
			IRC: IRCConfig{
				Nick:          "GoSitebot",
				Channels:      []string{"#weaveftpd"},
				AutoJoinDelay: 0,
				NickServ: NickServConfig{
					Enabled:  true,
					Password: "secret",
					DelayMS:  1,
				},
			},
		},
		IRC: irc.NewBot("irc.example.net", 6697, "GoSitebot", "sitebot", "GoSitebot"),
	}
	b.IRC.Conn = conn
	b.IRC.Connected = true
	b.IRC.Debug = false

	b.onRegistered()

	if len(conn.writes) < 2 {
		t.Fatalf("expected at least IDENTIFY and JOIN, got %d writes", len(conn.writes))
	}
	if got := strings.TrimSpace(conn.writes[0]); got != "PRIVMSG NickServ :IDENTIFY secret" {
		t.Fatalf("first write = %q", got)
	}
	if got := strings.TrimSpace(conn.writes[1]); got != "JOIN #weaveftpd" {
		t.Fatalf("second write = %q", got)
	}
}

func TestOnRegisteredNickServRegisterThenIdentify(t *testing.T) {
	conn := &recordingConn{}
	b := &Bot{
		Config: &Config{
			IRC: IRCConfig{
				Nick:          "GoSitebot",
				Channels:      []string{"#weaveftpd"},
				AutoJoinDelay: 0,
				NickServ: NickServConfig{
					Enabled:      true,
					Account:      "GoSitebot",
					Password:     "secret",
					Email:        "bot@example.net",
					AutoRegister: true,
					DelayMS:      1,
				},
			},
		},
		IRC: irc.NewBot("irc.example.net", 6697, "GoSitebot", "sitebot", "GoSitebot"),
	}
	b.IRC.Conn = conn
	b.IRC.Connected = true
	b.IRC.Debug = false

	b.onRegistered()

	if len(conn.writes) < 3 {
		t.Fatalf("expected REGISTER, IDENTIFY, JOIN; got %d writes", len(conn.writes))
	}
	if got := strings.TrimSpace(conn.writes[0]); got != "PRIVMSG NickServ :REGISTER secret bot@example.net" {
		t.Fatalf("first write = %q", got)
	}
	if got := strings.TrimSpace(conn.writes[1]); got != "PRIVMSG NickServ :IDENTIFY GoSitebot secret" {
		t.Fatalf("second write = %q", got)
	}
	if got := strings.TrimSpace(conn.writes[2]); got != "JOIN #weaveftpd" {
		t.Fatalf("third write = %q", got)
	}
}
