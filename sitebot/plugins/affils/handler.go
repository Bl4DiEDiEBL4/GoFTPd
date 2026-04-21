package affils

import (
	"fmt"
	"sort"
	"strings"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
)

type Plugin struct {
	affils      []Affil
	replyTarget string
	showPredirs bool
}

type Affil struct {
	Group  string
	Predir string
}

func New() *Plugin {
	return &Plugin{
		replyTarget: "channel",
	}
}

func (p *Plugin) Name() string { return "Affils" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	cfg := plugin.ConfigSection(config, "affils")
	if s, ok := stringConfig(cfg, config, "reply_target", "affils_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if b, ok := boolConfig(configValue(cfg, config, "show_predirs", "affils_show_predirs")); ok {
		p.showPredirs = b
	}
	p.affils = affilsConfig(configValue(cfg, config, "groups", "affils"))
	sort.Slice(p.affils, func(i, j int) bool {
		return strings.ToLower(p.affils[i].Group) < strings.ToLower(p.affils[j].Group)
	})
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	cmd := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
	switch cmd {
	case "affils", "affil":
		return p.showAffils(evt), nil
	case "pre":
		return p.reply(evt, "PRE: use SITE PRE <releasename> <section> in FTP."), nil
	default:
		return nil, nil
	}
}

func (p *Plugin) showAffils(evt *event.Event) []plugin.Output {
	if len(p.affils) == 0 {
		return p.reply(evt, "AFFILS: No affils configured.")
	}
	if p.showPredirs {
		lines := make([]string, 0, len(p.affils))
		for _, affil := range p.affils {
			lines = append(lines, fmt.Sprintf("AFFIL: %s - %s", affil.Group, affil.Predir))
		}
		return p.replies(evt, lines...)
	}
	groups := make([]string, 0, len(p.affils))
	for _, affil := range p.affils {
		groups = append(groups, affil.Group)
	}
	return p.reply(evt, "AFFILS: "+strings.Join(groups, ", "))
}

func (p *Plugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	if strings.HasPrefix(p.replyTarget, "#") {
		target = p.replyTarget
	} else if p.replyTarget == "notice" || target == "" {
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

func (p *Plugin) reply(evt *event.Event, text string) []plugin.Output {
	return p.replies(evt, text)
}

func affilsConfig(raw interface{}) []Affil {
	switch v := raw.(type) {
	case []interface{}:
		out := make([]Affil, 0, len(v))
		for _, item := range v {
			switch m := item.(type) {
			case map[string]interface{}:
				group, _ := m["group"].(string)
				predir, _ := m["predir"].(string)
				group = strings.TrimSpace(group)
				predir = strings.TrimSpace(predir)
				if group != "" {
					out = append(out, Affil{Group: group, Predir: predir})
				}
			case map[interface{}]interface{}:
				group, _ := m["group"].(string)
				predir, _ := m["predir"].(string)
				group = strings.TrimSpace(group)
				predir = strings.TrimSpace(predir)
				if group != "" {
					out = append(out, Affil{Group: group, Predir: predir})
				}
			case string:
				if group := strings.TrimSpace(m); group != "" {
					out = append(out, Affil{Group: group})
				}
			}
		}
		return out
	case []string:
		out := make([]Affil, 0, len(v))
		for _, group := range v {
			if group = strings.TrimSpace(group); group != "" {
				out = append(out, Affil{Group: group})
			}
		}
		return out
	case string:
		out := []Affil{}
		for _, group := range strings.Split(v, ",") {
			if group = strings.TrimSpace(group); group != "" {
				out = append(out, Affil{Group: group})
			}
		}
		return out
	default:
		return nil
	}
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
