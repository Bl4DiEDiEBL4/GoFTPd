package releaseguard

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"goftpd/internal/plugin"
	"goftpd/internal/user"
)

type rule struct {
	Path    string
	Pattern string
	re      *regexp.Regexp
}

type Plugin struct {
	svc                  *plugin.Services
	skipPaths            []string
	denySameNameInParent bool
	denyCaseConflicts    bool
	denyGroups           []rule
	denyDirs             []rule
	allowDirs            []rule
	nukedPrefixes        []string
	checkNukedNames      bool
	debug                bool
}

func New() *Plugin {
	return &Plugin{
		denySameNameInParent: true,
		denyCaseConflicts:    true,
		checkNukedNames:      true,
		nukedPrefixes:        []string{"[NUKED]-"},
	}
}

func (p *Plugin) Name() string { return "releaseguard" }

func (p *Plugin) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	p.svc = svc
	p.skipPaths = pathList(cfg["skip_paths"])
	if v, ok := cfg["deny_same_name_in_parent"].(bool); ok {
		p.denySameNameInParent = v
	}
	if v, ok := cfg["deny_case_conflicts"].(bool); ok {
		p.denyCaseConflicts = v
	}
	if v, ok := cfg["check_nuked_names"].(bool); ok {
		p.checkNukedNames = v
	}
	if v, ok := cfg["debug"].(bool); ok {
		p.debug = v
	}
	if prefixes := stringSlice(cfg["nuked_prefixes"]); len(prefixes) > 0 {
		p.nukedPrefixes = prefixes
	}
	var err error
	if p.denyGroups, err = parseRules(cfg["deny_groups"]); err != nil {
		return err
	}
	if p.denyDirs, err = parseRules(cfg["deny_dirs"]); err != nil {
		return err
	}
	if p.allowDirs, err = parseRules(cfg["allow_dirs"]); err != nil {
		return err
	}
	return nil
}

func (p *Plugin) OnEvent(evt *plugin.Event) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

func (p *Plugin) SiteCommands() []string { return []string{"BANNED"} }

func (p *Plugin) HandleSiteCommand(ctx plugin.SiteContext, command string, args []string) bool {
	if !strings.EqualFold(strings.TrimSpace(command), "BANNED") {
		return false
	}
	return p.handleBanned(ctx, args)
}

func (p *Plugin) ValidateMKDir(u *user.User, targetPath string) error {
	if p == nil || p.svc == nil || p.svc.Bridge == nil {
		return nil
	}

	targetPath = cleanAbs(targetPath)
	parent := path.Dir(targetPath)
	name := path.Base(targetPath)

	if parent == "/" || parent == "." || isSceneSubfolder(name) {
		return nil
	}
	if matchesAnyPath(targetPath, p.skipPaths) {
		return nil
	}
	if strings.TrimSpace(name) == "" {
		return nil
	}

	entries := p.svc.Bridge.PluginListDir(parent)
	for _, entry := range entries {
		entryName := strings.TrimSpace(entry.Name)
		if entryName == "" {
			continue
		}
		if p.denyCaseConflicts && strings.EqualFold(entryName, name) && entryName != name {
			return fmt.Errorf("release name clashes with existing dir %q (case differs)", entryName)
		}
		if p.denySameNameInParent && strings.EqualFold(entryName, name) {
			return fmt.Errorf("release %q already exists in %s", name, parent)
		}
		if p.checkNukedNames {
			for _, prefix := range p.nukedPrefixes {
				prefix = strings.TrimSpace(prefix)
				if prefix != "" && strings.EqualFold(entryName, prefix+name) {
					return fmt.Errorf("release %q already exists as a nuked entry in %s", name, parent)
				}
			}
		}
	}

	for _, r := range p.denyGroups {
		if r.matches(targetPath, name) {
			return fmt.Errorf("release group banned by rule %q", r.Pattern)
		}
	}
	for _, r := range p.denyDirs {
		if r.matches(targetPath, name) {
			return fmt.Errorf("release name banned by rule %q", r.Pattern)
		}
	}
	if len(p.allowDirs) > 0 {
		scoped := false
		allowed := false
		for _, r := range p.allowDirs {
			if !pathScopeMatches(r.Path, targetPath) {
				continue
			}
			scoped = true
			if r.re.MatchString(name) {
				allowed = true
				break
			}
		}
		if scoped && !allowed {
			return fmt.Errorf("release name does not match allowed patterns for this section")
		}
	}

	return nil
}

func (p *Plugin) handleBanned(ctx plugin.SiteContext, args []string) bool {
	mode := "deny"
	filter := ""
	if len(args) > 0 {
		switch strings.ToLower(strings.TrimSpace(args[0])) {
		case "allow", "allows", "allowdirs":
			mode = "allow"
			filter = strings.ToLower(strings.TrimSpace(strings.Join(args[1:], " ")))
		case "deny", "denies", "denydirs", "groups", "dirs", "all":
			mode = strings.ToLower(strings.TrimSpace(args[0]))
			if mode == "groups" || mode == "dirs" || mode == "all" {
				mode = "deny"
			}
			filter = strings.ToLower(strings.TrimSpace(strings.Join(args[1:], " ")))
		default:
			filter = strings.ToLower(strings.TrimSpace(strings.Join(args, " ")))
		}
	}

	lines := []string{}
	if mode == "allow" {
		lines = p.describeRules("ALLOW", p.allowDirs, filter)
		if len(lines) == 0 {
			ctx.Reply("200 No releaseguard allow rules configured.\r\n")
			return true
		}
		p.replyMultiline(ctx, lines)
		return true
	}

	groupLines := p.describeRules("GROUP", p.denyGroups, filter)
	dirLines := p.describeRules("DIR", p.denyDirs, filter)
	if len(groupLines) == 0 && len(dirLines) == 0 {
		if filter != "" {
			ctx.Reply("200 No releaseguard deny rules matched %q.\r\n", filter)
		} else {
			ctx.Reply("200 No releaseguard deny rules configured.\r\n")
		}
		return true
	}
	lines = append(lines, groupLines...)
	lines = append(lines, dirLines...)
	p.replyMultiline(ctx, lines)
	return true
}

func (p *Plugin) describeRules(kind string, rules []rule, filter string) []string {
	lines := make([]string, 0, len(rules))
	for _, r := range rules {
		line := fmt.Sprintf("%s %s -> %s", kind, r.Path, r.Pattern)
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func (p *Plugin) replyMultiline(ctx plugin.SiteContext, lines []string) {
	for i, line := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		ctx.Reply("200%s %s\r\n", sep, line)
	}
}

func parseRules(raw interface{}) ([]rule, error) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, nil
	}
	out := make([]rule, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		pathValue, _ := m["path"].(string)
		pattern, _ := m["pattern"].(string)
		pathValue = strings.TrimSpace(pathValue)
		pattern = strings.TrimSpace(pattern)
		if pathValue == "" {
			pathValue = "/"
		}
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid releaseguard regex %q: %w", pattern, err)
		}
		out = append(out, rule{
			Path:    cleanAbs(pathValue),
			Pattern: pattern,
			re:      re,
		})
	}
	return out, nil
}

func (r rule) matches(targetPath, name string) bool {
	return pathScopeMatches(r.Path, targetPath) && r.re.MatchString(name)
}

func pathList(raw interface{}) []string {
	return stringSlice(raw)
}

func stringSlice(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, cleanAbs(item))
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, cleanAbs(s))
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, cleanAbs(part))
			}
		}
		return out
	default:
		return nil
	}
}

func matchesAnyPath(targetPath string, patterns []string) bool {
	for _, pattern := range patterns {
		if pathScopeMatches(pattern, targetPath) {
			return true
		}
	}
	return false
}

func pathScopeMatches(scope, target string) bool {
	scope = cleanAbs(scope)
	target = filepath.ToSlash(filepath.Clean(target))
	if scope == "/" {
		return true
	}
	if strings.ContainsAny(scope, "*?[") {
		ok, _ := path.Match(scope, target)
		return ok
	}
	scope = strings.TrimRight(scope, "/")
	return target == scope || strings.HasPrefix(target, scope+"/")
}

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return filepath.ToSlash(path.Clean(p))
}

func isSceneSubfolder(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "sample", "samples", "proof", "proofs", "subs", "sub", "subtitles",
		"cover", "covers", "covers-back", "covers-front", "covers-side",
		"extras", "extra", "featurettes", "nfo":
		return true
	}
	if ok, _ := regexp.MatchString(`^(cd|dvd|disc|disk)\d+$`, lower); ok {
		return true
	}
	return false
}
