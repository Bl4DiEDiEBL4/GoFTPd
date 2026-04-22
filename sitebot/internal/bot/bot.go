package bot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/irc"
	"goftpd/sitebot/internal/plugin"
	admincommanderplugin "goftpd/sitebot/plugins/admincommander"
	affilsplugin "goftpd/sitebot/plugins/affils"
	announceplugin "goftpd/sitebot/plugins/announce"
	freeplugin "goftpd/sitebot/plugins/free"
	imdbplugin "goftpd/sitebot/plugins/imdb"
	newsplugin "goftpd/sitebot/plugins/news"
	requestplugin "goftpd/sitebot/plugins/request"
	tvmazeplugin "goftpd/sitebot/plugins/tvmaze"
)

type Bot struct {
	Config    *Config
	IRC       *irc.Bot
	Plugins   *plugin.Manager
	EventChan chan *event.Event
	Done      chan bool
	Mutex     sync.RWMutex
	Debug     bool
}

func NewBot(cfg *Config) *Bot {
	return &Bot{Config: cfg, Plugins: plugin.NewManager(), EventChan: make(chan *event.Event, 1024), Done: make(chan bool), Debug: cfg.Debug}
}

func (b *Bot) Start() error {
	log.Println("[Bot] Starting GoSitebot")
	b.IRC = irc.NewBot(b.Config.IRC.Host, b.Config.IRC.Port, b.Config.IRC.Nick, b.Config.IRC.User, b.Config.IRC.RealName)
	b.IRC.SSL = b.Config.IRC.SSL
	b.IRC.Password = b.Config.IRC.Password
	b.IRC.Debug = b.Debug
	if err := b.IRC.Connect(); err != nil {
		return fmt.Errorf("failed to connect to IRC: %w", err)
	}
	for ch, key := range b.Config.Encryption.Keys {
		_ = b.IRC.SetChannelKey(ch, key)
	}
	if err := b.initializePlugins(); err != nil {
		return err
	}
	go b.listenIRC()
	go b.readEvents()
	go b.processEvents()
	return nil
}

func (b *Bot) initializePlugins() error {
	if enabled, ok := b.Config.Plugins.Enabled["Announce"]; !ok || enabled {
		announce := announceplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := announce.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(announce); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["TVMaze"]; !ok || enabled {
		tv := tvmazeplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := tv.Initialize(cfg); err != nil {
			return err
		}
		// Provide an async emitter so TV lookups can post to IRC after the
		// HTTP call returns, without blocking the event loop.
		tv.SetAsyncEmitter(func(outType, text, section, relpath string) {
			fakeEvt := &event.Event{Type: event.EventMKDir, Section: section, Path: relpath}
			channels := b.routeChannels(fakeEvt, outType)
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimRight(line, "\r")
				if line == "" {
					continue
				}
				for _, ch := range channels {
					_ = b.IRC.SendMessage(ch, line)
				}
			}
		})
		if err := b.Plugins.Register(tv); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["IMDB"]; !ok || enabled {
		im := imdbplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := im.Initialize(cfg); err != nil {
			return err
		}
		im.SetAsyncEmitter(func(outType, text, section, relpath string) {
			fakeEvt := &event.Event{Type: event.EventMKDir, Section: section, Path: relpath}
			channels := b.routeChannels(fakeEvt, outType)
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimRight(line, "\r")
				if line == "" {
					continue
				}
				for _, ch := range channels {
					_ = b.IRC.SendMessage(ch, line)
				}
			}
		})
		if err := b.Plugins.Register(im); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["News"]; !ok || enabled {
		news := newsplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := news.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(news); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["Free"]; ok && enabled {
		free := freeplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := free.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(free); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["Affils"]; ok && enabled {
		affils := affilsplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := affils.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(affils); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["Request"]; ok && enabled {
		requests := requestplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := requests.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(requests); err != nil {
			return err
		}
	}
	if enabled, ok := b.Config.Plugins.Enabled["AdminCommander"]; ok && enabled {
		adminCommander := admincommanderplugin.New()
		cfg := map[string]interface{}{"debug": b.Debug, "theme_file": b.Config.Announce.ThemeFile}
		for k, v := range b.Config.Plugins.Config {
			cfg[k] = v
		}
		if err := adminCommander.Initialize(cfg); err != nil {
			return err
		}
		if err := b.Plugins.Register(adminCommander); err != nil {
			return err
		}
	}
	log.Printf("[Bot] Loaded %d plugins: %v", len(b.Plugins.List()), b.Plugins.List())
	return nil
}

func uniqueChannels(cfg *Config) []string {
	set := map[string]bool{}
	add := func(ch string) {
		ch = strings.TrimSpace(ch)
		if ch != "" {
			set[ch] = true
		}
	}
	for _, ch := range cfg.IRC.Channels {
		add(ch)
	}
	add(cfg.Announce.DefaultChannel)
	for _, arr := range cfg.Announce.TypeRoutes {
		for _, ch := range arr {
			add(ch)
		}
	}
	for _, s := range cfg.Sections {
		for _, ch := range s.Channels {
			add(ch)
		}
	}
	out := []string{}
	for ch := range set {
		out = append(out, ch)
	}
	return out
}

func (b *Bot) listenIRC() {
	var registeredOnce sync.Once
	handler := func(line string) {
		if strings.Contains(line, " 004 ") || strings.Contains(line, " 376 ") || strings.Contains(line, " 422 ") {
			registeredOnce.Do(func() {
				b.onRegistered()
			})
			return
		}

		if i := strings.Index(line, " INVITE "); i != -1 {
			parts := strings.Split(line, " ")
			if len(parts) >= 4 {
				channel := strings.TrimPrefix(parts[3], ":")
				if channel != "" {
					if b.Debug {
						log.Printf("[Bot] INVITE received, joining %s", channel)
					}
					_ = b.IRC.Join(channel)
				}
			}
		}
		if evt := b.commandEventFromPrivmsg(line); evt != nil {
			select {
			case b.EventChan <- evt:
			case <-b.Done:
			}
		}
	}
	if err := b.IRC.Listen(handler); err != nil {
		log.Printf("[Bot] IRC listen error: %v", err)
	}
}

func (b *Bot) commandEventFromPrivmsg(line string) *event.Event {
	if !strings.HasPrefix(line, ":") || !strings.Contains(line, " PRIVMSG ") {
		return nil
	}
	withoutPrefix := strings.TrimPrefix(line, ":")
	prefix, rest, ok := strings.Cut(withoutPrefix, " PRIVMSG ")
	if !ok {
		return nil
	}
	sender := prefix
	host := ""
	if nick, userHost, ok := strings.Cut(prefix, "!"); ok {
		sender = nick
		host = userHost
	}
	target, msg, ok := strings.Cut(rest, " :")
	if !ok {
		return nil
	}
	target = strings.TrimSpace(target)
	msg = strings.TrimSpace(msg)
	if enc, ok := b.IRC.Keys[target]; ok && strings.HasPrefix(msg, "+OK ") {
		ciphertext := strings.TrimSpace(strings.TrimPrefix(msg, "+OK "))
		ciphertext = strings.TrimPrefix(ciphertext, "*")
		if plain, err := enc.Decrypt(ciphertext); err == nil {
			msg = cleanIRCText(plain)
		} else if b.Debug {
			log.Printf("[Bot] Failed to decrypt command from %s in %s: %v", sender, target, err)
		}
	}
	if !strings.HasPrefix(target, "#") || !strings.HasPrefix(msg, "!") {
		return nil
	}

	fields := strings.Fields(strings.TrimPrefix(msg, "!"))
	if len(fields) == 0 {
		return nil
	}
	command := strings.ToLower(fields[0])
	args := ""
	if len(fields) > 1 {
		args = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(msg, "!"), fields[0]))
	}
	evt := event.NewEvent(event.EventCommand, sender, "", target, command)
	evt.Data["command"] = command
	evt.Data["args"] = args
	evt.Data["channel"] = target
	evt.Data["host"] = host
	evt.Data["raw"] = msg
	if b.Debug {
		log.Printf("[Bot] IRC command %q from %s in %s", command, sender, target)
	}
	return evt
}

func cleanIRCText(s string) string {
	return strings.Trim(strings.TrimSpace(s), "\x00")
}

func (b *Bot) onRegistered() {
	opered := false
	operUser := strings.TrimSpace(b.Config.IRC.OperUser)
	if operUser == "" {
		operUser = b.Config.IRC.Nick
	}
	if b.Config.IRC.AutoOper && strings.TrimSpace(b.Config.IRC.OperPassword) != "" {
		if err := b.IRC.SendRaw(fmt.Sprintf("OPER %s %s", operUser, b.Config.IRC.OperPassword)); err != nil {
			if b.Debug {
				log.Printf("[Bot] OPER failed: %v", err)
			}
		} else {
			opered = true
		}
		// Give server time to process OPER and apply oper privileges before
		// we try SAJOIN. UnrealIRCd's OPER → +o is near-instant but still
		// needs a roundtrip. Default 1500ms, overridable via autojoin_delay_ms.
		delay := b.Config.IRC.AutoJoinDelay
		if delay <= 0 {
			delay = 1500
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
	}

	if modes := strings.TrimSpace(b.Config.IRC.UserModes); modes != "" {
		modeArg := modes
		if !strings.HasPrefix(modeArg, "+") && !strings.HasPrefix(modeArg, "-") {
			modeArg = "+" + modeArg
		}
		if err := b.IRC.SendRaw(fmt.Sprintf("MODE %s %s", b.Config.IRC.Nick, modeArg)); err != nil && b.Debug {
			log.Printf("[Bot] MODE failed: %v", err)
		}
	}

	// Join all configured channels. When oper'd, use SAJOIN to bypass +i
	// (invite-only) and +R (registered-only) modes — this is how scene
	// bots get into staff/ops channels without needing a permanent invite.
	// Follow up with SAMODE +o so the bot has channel ops and can kick/ban
	// / manage the channel later if needed.
	for _, ch := range uniqueChannels(b.Config) {
		if opered {
			_ = b.IRC.SendRaw(fmt.Sprintf("SAJOIN %s %s", b.Config.IRC.Nick, ch))
			// Small gap so SAJOIN lands before SAMODE hits the same channel.
			time.Sleep(150 * time.Millisecond)
			_ = b.IRC.SendRaw(fmt.Sprintf("SAMODE %s +o %s", ch, b.Config.IRC.Nick))
		} else {
			_ = b.IRC.Join(ch)
		}
		if d := b.Config.IRC.AutoJoinDelay; d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
	}
}

func (b *Bot) readEvents() {
	log.Printf("[Bot] FIFO reader starting, watching %s", b.Config.EventFIFO)
	waited := false
	for {
		for {
			if _, err := os.Stat(b.Config.EventFIFO); err == nil {
				if waited {
					log.Printf("[Bot] FIFO appeared at %s", b.Config.EventFIFO)
				}
				break
			}
			if !waited {
				log.Printf("[Bot] FIFO not present at %s, waiting...", b.Config.EventFIFO)
				waited = true
			}
			time.Sleep(time.Second)
		}
		f, err := os.Open(b.Config.EventFIFO)
		if err != nil {
			log.Printf("[Bot] Failed to open FIFO: %v", err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("[Bot] FIFO opened, reading events from %s", b.Config.EventFIFO)
		s := bufio.NewScanner(f)
		for s.Scan() {
			line := s.Text()
			evt, err := parseEvent(line)
			if err != nil {
				log.Printf("[Bot] FIFO parse error: %v (raw=%q)", err, line)
				continue
			}
			log.Printf("[Bot] FIFO got event %s section=%s path=%s file=%s", evt.Type, evt.Section, evt.Path, evt.Filename)
			select {
			case b.EventChan <- evt:
			case <-b.Done:
				_ = f.Close()
				return
			}
		}
		if err := s.Err(); err != nil {
			log.Printf("[Bot] FIFO scanner error: %v", err)
		} else {
			log.Printf("[Bot] FIFO writer closed (EOF), reopening...")
		}
		_ = f.Close()
		waited = false
	}
}
func (b *Bot) processEvents() {
	for {
		select {
		case evt := <-b.EventChan:
			b.handleEvent(evt)
		case <-b.Done:
			return
		}
	}
}

func pathMatches(pattern, p string) bool {
	if pattern == "" || p == "" {
		return false
	}
	if ok, _ := filepath.Match(strings.TrimSpace(pattern), p); ok {
		return true
	}
	return strings.HasPrefix(strings.ToLower(p), strings.ToLower(strings.TrimRight(pattern, "*")))
}
func (b *Bot) routeChannels(evt *event.Event, outType string) []string {
	if chs := b.Config.Announce.TypeRoutes[outType]; len(chs) > 0 {
		return nonEmptyChannels(chs)
	}
	for _, sec := range b.Config.Sections {
		if strings.EqualFold(sec.Name, evt.Section) {
			if chs := nonEmptyChannels(sec.Channels); len(chs) > 0 {
				return chs
			}
		}
		for _, pat := range sec.Paths {
			if pathMatches(pat, evt.Path) {
				if chs := nonEmptyChannels(sec.Channels); len(chs) > 0 {
					return chs
				}
			}
		}
	}
	if strings.TrimSpace(b.Config.Announce.DefaultChannel) != "" {
		return []string{b.Config.Announce.DefaultChannel}
	}
	return nonEmptyChannels(b.Config.IRC.Channels)
}

func nonEmptyChannels(channels []string) []string {
	out := make([]string, 0, len(channels))
	for _, ch := range channels {
		ch = strings.TrimSpace(ch)
		if ch != "" {
			out = append(out, ch)
		}
	}
	return out
}
func (b *Bot) handleEvent(evt *event.Event) {
	// Special case: INVITE events don't go to plugins — we send an IRC
	// INVITE command directly for each channel the user is allowed into.
	if evt.Type == event.EventInvite {
		b.handleInviteEvent(evt)
		return
	}
	log.Printf("[Bot] handleEvent %s section=%s path=%s file=%s user=%s", evt.Type, evt.Section, evt.Path, evt.Filename, evt.User)
	outs, err := b.Plugins.ProcessEvent(evt)
	if err != nil {
		log.Printf("[Bot] Plugin processing error for %s: %v", evt.Type, err)
		if len(outs) == 0 {
			return
		}
	}
	if len(outs) == 0 {
		log.Printf("[Bot] %s produced no plugin output", evt.Type)
		return
	}
	log.Printf("[Bot] %s produced %d plugin outputs", evt.Type, len(outs))
	for _, out := range outs {
		if strings.TrimSpace(out.Target) != "" {
			for _, line := range strings.Split(out.Text, "\n") {
				line = strings.TrimRight(line, "\r")
				if line == "" {
					continue
				}
				kind := "PRIVMSG"
				if out.Notice {
					kind = "NOTICE"
				}
				log.Printf("[Bot] -> %s %s: %s", kind, out.Target, line)
				if out.Notice {
					if err := b.IRC.SendNotice(out.Target, line); err != nil {
						log.Printf("[Bot] NOTICE %s failed: %v", out.Target, err)
					}
				} else {
					if err := b.IRC.SendMessage(out.Target, line); err != nil {
						log.Printf("[Bot] PRIVMSG %s failed: %v", out.Target, err)
					}
				}
			}
			continue
		}
		channels := b.routeChannels(evt, out.Type)
		if len(channels) == 0 {
			log.Printf("[Bot] %s output dropped — routeChannels returned empty for section=%s outType=%s", evt.Type, evt.Section, out.Type)
			continue
		}
		for _, line := range strings.Split(out.Text, "\n") {
			line = strings.TrimRight(line, "\r")
			if line == "" {
				continue
			}
			for _, ch := range channels {
				log.Printf("[Bot] -> %s %s: %s", out.Type, ch, line)
				if err := b.IRC.SendMessage(ch, line); err != nil {
					log.Printf("[Bot] %s to %s failed: %v", out.Type, ch, err)
				}
			}
		}
	}
}
func (b *Bot) Stop() error {
	close(b.Done)
	if b.IRC != nil {
		_ = b.IRC.Quit("Shutting down")
		_ = b.IRC.Close()
	}
	_ = b.Plugins.Close()
	return nil
}

// handleInviteEvent processes an INVITE event from goftpd. It sends an IRC
// INVITE command for each channel in evt.Data["channels"] (comma-separated).
// Requires the bot to have ops in those channels (or them to permit non-op
// invites). The bot is typically oper'd via auto_oper, so it can invite
// anywhere network-wide.
func (b *Bot) handleInviteEvent(evt *event.Event) {
	if b.IRC == nil {
		return
	}
	nick := strings.TrimSpace(evt.Data["nick"])
	channels := strings.TrimSpace(evt.Data["channels"])
	if nick == "" || channels == "" {
		if b.Debug {
			log.Printf("[Bot] INVITE event missing nick or channels: %+v", evt.Data)
		}
		return
	}
	for _, ch := range strings.Split(channels, ",") {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			continue
		}
		if err := b.IRC.Invite(nick, ch); err != nil {
			log.Printf("[Bot] INVITE %s %s failed: %v", nick, ch, err)
		} else if b.Debug {
			log.Printf("[Bot] Sent SAJOIN/INVITE %s to %s", nick, ch)
		}
		// Small pacing gap — SAJOIN+INVITE per channel is 2 lines, over 3
		// channels that's 6 rapid commands. IRC servers often throttle or
		// drop bursts. 300ms keeps us well under any reasonable flood limit.
		time.Sleep(300 * time.Millisecond)
	}
}

func parseEvent(line string) (*event.Event, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("empty")
	}
	if strings.HasPrefix(line, "{") {
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return nil, err
		}
		if evt.Data == nil {
			evt.Data = map[string]string{}
		}
		return &evt, nil
	}
	parts := strings.Split(line, ":")
	if len(parts) < 4 {
		return nil, fmt.Errorf("invalid")
	}
	evt := event.NewEvent(event.EventType(strings.ToUpper(parts[0])), parts[1], parts[2], parts[3], "")
	if len(parts) > 4 {
		evt.Filename = parts[4]
	}
	if len(parts) > 5 {
		fmt.Sscanf(parts[5], "%d", &evt.Size)
	}
	if len(parts) > 6 {
		fmt.Sscanf(parts[6], "%f", &evt.Speed)
	}
	if len(parts) > 7 {
		evt.Path = parts[7]
	}
	return evt, nil
}
