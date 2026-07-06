package news

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
)

type NewsPlugin struct {
	mu            sync.Mutex
	file          string
	maxShown      int
	defaultShown  int
	searchDefault int
	channels      []string
	staffChannels []string
	staffHosts    []string
	dateFormat    string
	replyTarget   string
}

type item struct {
	Time int64  `json:"time"`
	User string `json:"user"`
	Text string `json:"text"`
}

func New() *NewsPlugin {
	return &NewsPlugin{
		file:          "./data/news.jsonl",
		maxShown:      20,
		defaultShown:  5,
		searchDefault: 4,
		staffChannels: []string{"#weaveftpd-staff"},
		dateFormat:    "02 Jan 15:04",
		replyTarget:   "channel",
	}
}

func (p *NewsPlugin) Name() string { return "News" }

func (p *NewsPlugin) Initialize(config map[string]interface{}) error {
	newsConfig := plugin.ConfigSection(config, "news")
	if s, ok := stringConfig(newsConfig, config, "file", "news_file"); ok && strings.TrimSpace(s) != "" {
		p.file = strings.TrimSpace(s)
	}
	if n := intConfig(configValue(newsConfig, config, "max_shown", "news_max_shown"), p.maxShown); n > 0 {
		p.maxShown = n
	}
	if n := intConfig(configValue(newsConfig, config, "default_shown", "news_default_shown"), p.defaultShown); n > 0 {
		p.defaultShown = n
	}
	if n := intConfig(configValue(newsConfig, config, "search_default", "news_search_default"), p.searchDefault); n > 0 {
		p.searchDefault = n
	}
	if raw, ok := configValueOK(newsConfig, config, "channels", "news_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if raw, ok := configValueOK(newsConfig, config, "staff_channels", "news_staff_channels"); ok {
		p.staffChannels = plugin.ToStringSlice(raw, p.staffChannels)
	}
	if raw, ok := configValueOK(newsConfig, config, "staff_hosts", "news_staff_hosts"); ok {
		p.staffHosts = plugin.ToStringSlice(raw, p.staffHosts)
	}
	if s, ok := stringConfig(newsConfig, config, "date_format", "news_date_format"); ok && strings.TrimSpace(s) != "" {
		p.dateFormat = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(newsConfig, config, "reply_target", "news_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	return os.MkdirAll(filepath.Dir(p.file), 0755)
}

func (p *NewsPlugin) Close() error { return nil }

func (p *NewsPlugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	command := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
	args := strings.TrimSpace(evt.Data["args"])
	switch command {
	case "news":
		return p.show(evt, args)
	case "addnews":
		return p.add(evt, args)
	case "delnews":
		return p.delete(evt, args)
	default:
		return nil, nil
	}
}

func (p *NewsPlugin) show(evt *event.Event, args string) ([]plugin.Output, error) {
	p.mu.Lock()
	items, err := p.read()
	p.mu.Unlock()
	if err != nil {
		return p.reply(evt, "News is unavailable right now."), nil
	}
	if strings.EqualFold(args, "help") {
		return p.replies(evt,
			"!news [number] [search]",
			"!news - show latest news",
			"!news 10 - show 10 latest news items",
			"!news affils - search latest news for affils",
			"!addnews <text> and !delnews <number> are staff commands",
		), nil
	}

	count, search := p.parseShowArgs(args)
	if search != "" {
		filtered := make([]item, 0, len(items))
		q := strings.ToLower(search)
		for _, it := range items {
			if strings.Contains(strings.ToLower(it.Text), q) || strings.Contains(strings.ToLower(it.User), q) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	if len(items) == 0 {
		if search != "" {
			return p.reply(evt, fmt.Sprintf("No news found for %q.", search)), nil
		}
		return p.reply(evt, "No news has been added yet."), nil
	}
	if count > len(items) {
		count = len(items)
	}

	out := make([]string, 0, count)
	for i := len(items) - 1; i >= 0 && len(out) < count; i-- {
		it := items[i]
		when := time.Unix(it.Time, 0).Format(p.dateFormat)
		out = append(out, fmt.Sprintf("[%s] %s - %s", when, it.User, it.Text))
	}
	return p.replies(evt, out...), nil
}

func (p *NewsPlugin) add(evt *event.Event, text string) ([]plugin.Output, error) {
	if !p.canStaff(evt) {
		return p.directReplies(evt, "Sorry, only staff can add news."), nil
	}
	if strings.TrimSpace(text) == "" {
		return p.directReplies(evt, "Usage: !addnews <news>"), nil
	}
	p.mu.Lock()
	err := p.append(item{Time: time.Now().Unix(), User: evt.User, Text: text})
	p.mu.Unlock()
	if err != nil {
		return p.directReplies(evt, "Could not add news right now."), nil
	}
	channel := strings.TrimSpace(evt.Data["channel"])
	if channel == "" {
		return p.directReplies(evt, "News added."), nil
	}
	targets := p.channels
	if len(targets) == 0 {
		targets = []string{channel}
	}
	outs := p.directReplies(evt, "News added.")
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		outs = append(outs, plugin.Output{Type: "COMMAND", Target: target, Text: fmt.Sprintf("News added by %s: %s", evt.User, text)})
	}
	return outs, nil
}

func (p *NewsPlugin) delete(evt *event.Event, args string) ([]plugin.Output, error) {
	if !p.canStaff(evt) {
		return p.directReplies(evt, "Sorry, only staff can delete news."), nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(args))
	if err != nil || n < 1 {
		return p.directReplies(evt, "Usage: !delnews <number> where 1 is the newest news item"), nil
	}

	p.mu.Lock()
	items, err := p.read()
	if err == nil {
		idx := len(items) - n
		if idx < 0 || idx >= len(items) {
			err = fmt.Errorf("news item not found")
		} else {
			items = append(items[:idx], items[idx+1:]...)
			err = p.write(items)
		}
	}
	p.mu.Unlock()
	if err != nil {
		return p.directReplies(evt, "Could not delete that news item."), nil
	}
	return p.directReplies(evt, "News deleted."), nil
}

func (p *NewsPlugin) parseShowArgs(args string) (int, string) {
	args = strings.TrimSpace(args)
	count := p.defaultShown
	search := ""
	if args == "" {
		return count, search
	}
	fields := strings.Fields(args)
	if n, err := strconv.Atoi(fields[0]); err == nil {
		count = n
		search = strings.Join(fields[1:], " ")
	} else if n, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
		count = n
		search = strings.Join(fields[:len(fields)-1], " ")
	} else {
		count = p.searchDefault
		search = args
	}
	if count > p.maxShown {
		count = p.maxShown
	}
	if count < 1 {
		count = 1
	}
	return count, search
}

func (p *NewsPlugin) canStaff(evt *event.Event) bool {
	channel := strings.ToLower(strings.TrimSpace(evt.Data["channel"]))
	for _, ch := range p.staffChannels {
		if strings.EqualFold(strings.TrimSpace(ch), channel) {
			return true
		}
	}
	host := strings.ToLower(strings.TrimSpace(evt.Data["host"]))
	for _, pattern := range p.staffHosts {
		if wildcardMatch(strings.ToLower(strings.TrimSpace(pattern)), host) {
			return true
		}
	}
	return false
}

func (p *NewsPlugin) read() ([]item, error) {
	f, err := os.Open(p.file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	items := []item{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var it item
		if err := json.Unmarshal([]byte(line), &it); err == nil && it.Text != "" {
			items = append(items, it)
			continue
		}
		if legacy, ok := parseLegacy(line); ok {
			items = append(items, legacy)
		}
	}
	return items, scanner.Err()
}

func (p *NewsPlugin) append(it item) error {
	if err := os.MkdirAll(filepath.Dir(p.file), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(p.file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(it)
}

func (p *NewsPlugin) write(items []item) error {
	if err := os.MkdirAll(filepath.Dir(p.file), 0755); err != nil {
		return err
	}
	tmp := p.file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, p.file)
}

func parseLegacy(line string) (item, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return item{}, false
	}
	ts, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return item{}, false
	}
	return item{Time: ts, User: fields[1], Text: strings.Join(fields[2:], " ")}, true
}

func notices(target string, lines ...string) []plugin.Output {
	out := make([]plugin.Output, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, plugin.Output{Type: "COMMAND", Target: target, Notice: true, Text: line})
	}
	return out
}

func notice(target, text string) []plugin.Output {
	return notices(target, text)
}

func (p *NewsPlugin) directReplies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.User)
	out := make([]plugin.Output, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || target == "" {
			continue
		}
		out = append(out, plugin.Output{Type: "COMMAND", Target: target, Notice: false, Text: line})
	}
	return out
}

func (p *NewsPlugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	if p.replyTarget == "notice" || target == "" {
		target = evt.User
		noticeReply = true
	}
	out := make([]plugin.Output, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, plugin.Output{Type: "COMMAND", Target: target, Notice: noticeReply, Text: line})
	}
	return out
}

func (p *NewsPlugin) reply(evt *event.Event, text string) []plugin.Output {
	return p.replies(evt, text)
}

func configValue(section, flat map[string]interface{}, sectionKey, flatKey string) interface{} {
	raw, _ := configValueOK(section, flat, sectionKey, flatKey)
	return raw
}

func configValueOK(section, flat map[string]interface{}, sectionKey, flatKey string) (interface{}, bool) {
	if raw, ok := section[sectionKey]; ok {
		return raw, true
	}
	raw, ok := flat[flatKey]
	return raw, ok
}

func stringConfig(section, flat map[string]interface{}, sectionKey, flatKey string) (string, bool) {
	raw, ok := configValueOK(section, flat, sectionKey, flatKey)
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
}

func intConfig(raw interface{}, fallback int) int {
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func wildcardMatch(pattern, value string) bool {
	if pattern == "" || value == "" {
		return false
	}
	if ok, _ := filepath.Match(pattern, value); ok {
		return true
	}
	return pattern == value
}
