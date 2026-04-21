package affils

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	"gopkg.in/yaml.v3"
)

type Plugin struct {
	affils      []Affil
	file        string
	replyTarget string
	showPredirs bool
}

type Affil struct {
	Group       string                 `yaml:"group"`
	Predir      string                 `yaml:"predir"`
	Permissions map[string]interface{} `yaml:"permissions"`
}

type affilsFileConfig struct {
	Base   string  `yaml:"base"`
	Groups []Affil `yaml:"groups"`
}

func New() *Plugin {
	return &Plugin{
		file:        "etc/affils.yml",
		replyTarget: "channel",
	}
}

func (p *Plugin) Name() string { return "Affils" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	cfg := plugin.ConfigSection(config, "affils")
	if s, ok := stringConfig(cfg, config, "reply_target", "affils_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if s, ok := stringConfig(cfg, config, "file", "affils_file"); ok && strings.TrimSpace(s) != "" {
		p.file = strings.TrimSpace(s)
	}
	if b, ok := boolConfig(configValue(cfg, config, "show_predirs", "affils_show_predirs")); ok {
		p.showPredirs = b
	}
	p.affils = sortedAffils(affilsConfig(configValue(cfg, config, "groups", "affils")))
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
	affils := p.currentAffils()
	if len(affils) == 0 {
		return p.reply(evt, "AFFILS: No affils configured.")
	}
	if p.showPredirs {
		lines := make([]string, 0, len(affils))
		for _, affil := range affils {
			lines = append(lines, fmt.Sprintf("AFFIL: %s - %s", affil.Group, affil.Predir))
		}
		return p.replies(evt, lines...)
	}
	groups := make([]string, 0, len(affils))
	for _, affil := range affils {
		groups = append(groups, affil.Group)
	}
	return p.reply(evt, "AFFILS: "+strings.Join(groups, ", "))
}

func (p *Plugin) currentAffils() []Affil {
	cfg, err := loadAffilsFile(p.file)
	if err != nil {
		return append([]Affil(nil), p.affils...)
	}
	if len(cfg.Groups) == 0 {
		return append([]Affil(nil), p.affils...)
	}
	return sortedAffils(cfg.Groups)
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

func loadAffilsFile(filePath string) (affilsFileConfig, error) {
	var cfg affilsFileConfig
	for _, candidate := range affilsFileCandidates(filePath) {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, err
		}
		return cfg, nil
	}
	return cfg, fmt.Errorf("affils file not found: %s", filePath)
}

func affilsFileCandidates(filePath string) []string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		filePath = "etc/affils.yml"
	}
	out := []string{filePath}
	clean := filepath.ToSlash(filePath)
	if strings.HasPrefix(clean, "etc/") {
		out = append(out, "../"+clean)
	}
	return out
}

func sortedAffils(in []Affil) []Affil {
	out := make([]Affil, 0, len(in))
	for _, affil := range in {
		affil.Group = strings.TrimSpace(affil.Group)
		affil.Predir = strings.TrimSpace(affil.Predir)
		if affil.Group != "" {
			out = append(out, affil)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Group) < strings.ToLower(out[j].Group)
	})
	return out
}
