package topic

import (
	"fmt"
	"path/filepath"
	"strings"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
)

type TopicSetter interface {
	SetTopic(channel, topic string, encrypt bool) error
}

type Plugin struct {
	staffChannels []string
	staffHosts    []string
	replyTarget   string
	encryptTopic  bool
	allowedTarget []string
	theme         *tmpl.Theme
	setter        TopicSetter
}

func New() *Plugin {
	return &Plugin{
		staffChannels: []string{"#goftpd-staff"},
		staffHosts:    []string{},
		replyTarget:   "notice",
		encryptTopic:  true,
		allowedTarget: []string{},
	}
}

func (p *Plugin) Name() string { return "Topic" }

func (p *Plugin) SetTopicSetter(setter TopicSetter) {
	p.setter = setter
}

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "topic")
	if raw, ok := configValueOK(cfg, config, "staff_channels", "topic_staff_channels"); ok {
		p.staffChannels = plugin.ToStringSlice(raw, p.staffChannels)
	}
	if raw, ok := configValueOK(cfg, config, "staff_hosts", "topic_staff_hosts"); ok {
		p.staffHosts = plugin.ToStringSlice(raw, p.staffHosts)
	}
	if raw, ok := configValueOK(cfg, config, "allowed_targets", "topic_allowed_targets"); ok {
		p.allowedTarget = plugin.ToStringSlice(raw, p.allowedTarget)
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "topic_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if b, ok := boolConfig(configValue(cfg, config, "encrypt_topic", "topic_encrypt_topic")); ok {
		p.encryptTopic = b
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	cmd := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
	if cmd != "topic" {
		return nil, nil
	}
	if !p.canStaff(evt) {
		return p.reply(evt, p.render("TOPICCMD_DENIED", map[string]string{"user": evt.User}, "TOPIC: staff command only.")), nil
	}
	if p.setter == nil {
		return p.reply(evt, p.render("TOPICCMD_ERROR", map[string]string{"response": "topic setter is not configured"}, "TOPIC: topic setter is not configured.")), nil
	}

	target, topicText := parseTopicArgs(evt.Data["args"])
	if target == "" || topicText == "" {
		return p.reply(evt, p.render("TOPICCMD_USAGE", map[string]string{}, "TOPIC: Usage: !topic #channel topic text")), nil
	}
	if !p.targetAllowed(target) {
		return p.reply(evt, p.render("TOPICCMD_ERROR", map[string]string{"response": fmt.Sprintf("target %s is not allowed", target)}, "TOPIC: target channel is not allowed.")), nil
	}
	if err := p.setter.SetTopic(target, topicText, p.encryptTopic); err != nil {
		return p.reply(evt, p.render("TOPICCMD_ERROR", map[string]string{
			"channel":  target,
			"response": err.Error(),
		}, "TOPIC: failed to set topic: "+err.Error())), nil
	}
	return p.reply(evt, p.render("TOPICCMD_OK", map[string]string{
		"channel": target,
		"topic":   topicText,
	}, fmt.Sprintf("TOPIC: set %s", target))), nil
}

func parseTopicArgs(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return "", ""
	}
	return strings.TrimSpace(fields[0]), strings.TrimSpace(strings.TrimPrefix(raw, fields[0]))
}

func (p *Plugin) targetAllowed(target string) bool {
	if len(p.allowedTarget) == 0 {
		return true
	}
	target = strings.TrimSpace(target)
	for _, allowed := range p.allowedTarget {
		allowed = strings.TrimSpace(allowed)
		if strings.EqualFold(allowed, target) {
			return true
		}
	}
	return false
}

func (p *Plugin) canStaff(evt *event.Event) bool {
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

func wildcardMatch(pattern, value string) bool {
	if pattern == "" || value == "" {
		return false
	}
	if ok, _ := filepath.Match(pattern, value); ok {
		return true
	}
	return pattern == value
}

func (p *Plugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	switch {
	case strings.HasPrefix(p.replyTarget, "#"):
		target = p.replyTarget
	case p.replyTarget == "notice":
		target = evt.User
		noticeReply = true
	default:
		if target == "" {
			target = evt.User
		}
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

func (p *Plugin) reply(evt *event.Event, text string) []plugin.Output {
	return p.replies(evt, text)
}

func (p *Plugin) render(key string, vars map[string]string, fallback string) string {
	if p.theme != nil {
		if raw, ok := p.theme.Announces[key]; ok && raw != "" {
			return tmpl.Render(raw, vars)
		}
	}
	return fallback
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

func boolConfig(raw interface{}) (bool, bool) {
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1", "on":
			return true, true
		case "false", "no", "0", "off":
			return false, true
		}
	}
	return false, false
}
