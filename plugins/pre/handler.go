package pre

import (
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"goftpd/internal/plugin"
	"goftpd/internal/timeutil"
	"gopkg.in/yaml.v3"
)

type Plugin struct {
	svc             *plugin.Services
	base            string
	sections        []string
	datedSections   []string
	bwDuration      int
	bwIntervalMs    int
	affils          []AffilRule
	affilsFile      string
	permissionsFile string
	groupFile       string
	aclBase         string
	adminFlags      string
	debug           bool
}

type AffilRule struct {
	Group       string                 `yaml:"group"`
	Predir      string                 `yaml:"predir"`
	Permissions map[string]interface{} `yaml:"permissions"`
}

type affilsFileConfig struct {
	Base   string      `yaml:"base"`
	Groups []AffilRule `yaml:"groups"`
}

type permissionsFileConfig struct {
	Rules []permissionRule `yaml:"rules"`
}

type permissionRule struct {
	Type     string `yaml:"type"`
	Path     string `yaml:"path"`
	Required string `yaml:"required"`
}

type userSnapshot struct {
	Bytes int64
	Files int
}

func New() *Plugin {
	return &Plugin{
		base:            "/PRE",
		bwDuration:      30,
		bwIntervalMs:    500,
		affilsFile:      "etc/affils.yml",
		permissionsFile: "etc/permissions.yml",
		groupFile:       "etc/group",
		aclBase:         "/site",
		adminFlags:      "1",
	}
}

func (p *Plugin) Name() string { return "pre" }

func (p *Plugin) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	p.svc = svc
	if s := stringConfig(cfg, "base", ""); strings.TrimSpace(s) != "" {
		p.base = cleanAbs(s)
	}
	if s := stringConfig(cfg, "affils_file", ""); strings.TrimSpace(s) != "" {
		p.affilsFile = strings.TrimSpace(s)
	}
	if s := stringConfig(cfg, "permissions_file", ""); strings.TrimSpace(s) != "" {
		p.permissionsFile = strings.TrimSpace(s)
	}
	if s := stringConfig(cfg, "group_file", ""); strings.TrimSpace(s) != "" {
		p.groupFile = strings.TrimSpace(s)
	}
	if s := stringConfig(cfg, "acl_base_path", ""); strings.TrimSpace(s) != "" {
		p.aclBase = cleanAbs(s)
	}
	if s := stringConfig(cfg, "admin_flags", ""); strings.TrimSpace(s) != "" {
		p.adminFlags = strings.TrimSpace(s)
	}
	p.sections = stringSliceConfig(cfg["sections"])
	p.datedSections = stringSliceConfig(cfg["dated_sections"])
	if n := intConfig(cfg["bw_duration"], 0); n > 0 {
		p.bwDuration = n
	}
	if n := intConfig(cfg["bw_interval_ms"], 0); n > 0 {
		p.bwIntervalMs = n
	}
	if b, ok := cfg["debug"].(bool); ok {
		p.debug = b
	}
	p.affils = normalizeAffils(affilRulesConfig(cfg["affils"]), p.base)
	return nil
}

func (p *Plugin) OnEvent(evt *plugin.Event) error { return nil }

func (p *Plugin) Stop() error { return nil }

func (p *Plugin) SiteCommands() []string { return []string{"PRE", "ADDAFFIL", "DELAFFIL", "AFFILS"} }

func (p *Plugin) HandleSiteCommand(ctx plugin.SiteContext, command string, args []string) bool {
	if p.svc == nil || p.svc.Bridge == nil {
		ctx.Reply("451 Master bridge unavailable.\r\n")
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(command)) {
	case "ADDAFFIL":
		return p.handleAddAffil(ctx, args)
	case "DELAFFIL":
		return p.handleDelAffil(ctx, args)
	case "AFFILS":
		return p.handleAffils(ctx)
	}
	if len(args) < 2 {
		ctx.Reply("501 Usage: SITE PRE <releasename> <section>\r\n")
		return true
	}

	relname := strings.TrimSpace(args[0])
	section := strings.TrimSpace(args[1])
	if relname == "" || strings.ContainsAny(relname, "/\\") {
		ctx.Reply("501 Invalid release name.\r\n")
		return true
	}
	if !sectionAllowed(p.sections, section) {
		ctx.Reply("501 Section %q is not a valid pre section.\r\n", section)
		return true
	}

	affil := p.findUserAffil(ctx.UserGroups())
	if affil == nil {
		ctx.Reply("550 You are not in any affil group.\r\n")
		return true
	}

	src := path.Join(affil.Predir, relname)
	destSection := cleanAbs(section)
	if sectionIsDated(p.datedSections, section) {
		destSection = path.Join(destSection, timeutil.Now().Format("0102"))
	}
	dst := path.Join(destSection, relname)

	if !p.childDirExists(affil.Predir, relname) {
		ctx.Reply("550 Release %q not found in %s.\r\n", relname, affil.Predir)
		return true
	}
	if !p.dirExists(destSection) {
		ctx.Reply("550 Destination %s does not exist.\r\n", destSection)
		return true
	}
	if p.childDirExists(destSection, relname) || p.svc.Bridge.FileExists(dst) {
		ctx.Reply("550 Destination %s already exists.\r\n", dst)
		return true
	}

	_, _, totalBytes, present, _ := p.svc.Bridge.PluginGetVFSRaceStats(src)
	mbytes := float64(totalBytes) / 1024.0 / 1024.0

	p.svc.Bridge.RenameFile(src, destSection, relname)
	p.logf("%s pre'd %s to %s (%d files, %.0f MB)", ctx.UserName(), relname, dst, present, mbytes)

	p.emit("PRE", dst, relname, section, totalBytes, 0, map[string]string{
		"relname":  relname,
		"section":  section,
		"group":    affil.Group,
		"user":     ctx.UserName(),
		"t_files":  fmt.Sprintf("%d", present),
		"t_mbytes": fmt.Sprintf("%.0fMB", mbytes),
	})

	go p.runBWSampler(dst, relname, section, affil.Group)

	ctx.Reply("200 %s pre'd to %s successfully.\r\n", relname, dst)
	return true
}

func (p *Plugin) handleAffils(ctx plugin.SiteContext) bool {
	affils := p.currentAffils()
	if len(affils) == 0 {
		ctx.Reply("200 No affils configured.\r\n")
		return true
	}
	names := make([]string, 0, len(affils))
	for _, affil := range affils {
		names = append(names, affil.Group)
	}
	sort.Strings(names)
	ctx.Reply("200 Affils: %s\r\n", strings.Join(names, ", "))
	return true
}

func (p *Plugin) handleAddAffil(ctx plugin.SiteContext, args []string) bool {
	if !p.canAdmin(ctx) {
		ctx.Reply("550 Permission denied.\r\n")
		return true
	}
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		ctx.Reply("501 Usage: SITE ADDAFFIL <group> [predir]\r\n")
		return true
	}
	group := strings.TrimSpace(args[0])
	if !validAffilGroup(group) {
		ctx.Reply("501 Invalid affil group name.\r\n")
		return true
	}

	cfg := p.currentAffilsFileConfig()
	base := p.base
	if strings.TrimSpace(cfg.Base) != "" {
		base = cleanAbs(cfg.Base)
	} else {
		cfg.Base = base
	}
	predir := ""
	if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
		predir = cleanAbs(args[1])
	} else {
		predir = path.Join(base, group)
	}

	for _, affil := range cfg.Groups {
		if strings.EqualFold(affil.Group, group) {
			if err := ensureGroupFile(p.groupFile, group); err != nil {
				ctx.Reply("550 Affil %s already exists, but could not ensure group files: %v\r\n", group, err)
				return true
			}
			predir := affil.Predir
			if strings.TrimSpace(predir) == "" {
				predir = path.Join(base, group)
			}
			if err := ensureAffilPermissions(p.permissionsFile, p.aclPath(predir), group); err != nil {
				ctx.Reply("550 Affil %s already exists, but could not repair permissions: %v\r\n", group, err)
				return true
			}
			ctx.Reply("550 Affil %s already exists.\r\n", group)
			return true
		}
	}

	cfg.Groups = append(cfg.Groups, AffilRule{
		Group:  group,
		Predir: predir,
		Permissions: map[string]interface{}{
			"privpath":    p.aclPath(predir),
			"owner_group": group,
			"mode":        "0777",
		},
	})
	sort.Slice(cfg.Groups, func(i, j int) bool {
		return strings.ToLower(cfg.Groups[i].Group) < strings.ToLower(cfg.Groups[j].Group)
	})

	if err := saveAffilsFile(p.affilsFile, cfg); err != nil {
		ctx.Reply("451 Could not update %s: %v\r\n", p.affilsFile, err)
		return true
	}

	parent := path.Dir(predir)
	if !p.dirExists(parent) {
		p.svc.Bridge.MakeDir(parent, ctx.UserName(), ctx.UserPrimaryGroup())
	}
	p.svc.Bridge.MakeDir(predir, ctx.UserName(), group)
	_ = p.svc.Bridge.Chmod(predir, 0777)

	if err := ensureGroupFile(p.groupFile, group); err != nil {
		ctx.Reply("200- Affil %s added with predir %s\r\n", group, predir)
		ctx.Reply("200- WARNING: could not update %s: %v\r\n", p.groupFile, err)
		ctx.Reply("200 Continue checking permissions update.\r\n")
	}

	if err := ensureAffilPermissions(p.permissionsFile, p.aclPath(predir), group); err != nil {
		ctx.Reply("200- Affil %s added with predir %s\r\n", group, predir)
		ctx.Reply("200- WARNING: could not update %s: %v\r\n", p.permissionsFile, err)
		ctx.Reply("200 Run SITE REHASH or restart sessions that need new ACL state.\r\n")
		return true
	}

	ctx.Reply("200- Affil %s added with predir %s\r\n", group, predir)
	ctx.Reply("200 Updated %s, %s, and %s. Run SITE REHASH or restart sessions that need new ACL state.\r\n", p.affilsFile, p.permissionsFile, p.groupFile)
	return true
}

func (p *Plugin) handleDelAffil(ctx plugin.SiteContext, args []string) bool {
	if !p.canAdmin(ctx) {
		ctx.Reply("550 Permission denied.\r\n")
		return true
	}
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		ctx.Reply("501 Usage: SITE DELAFFIL <group>\r\n")
		return true
	}
	group := strings.TrimSpace(args[0])

	cfg := p.currentAffilsFileConfig()
	kept := make([]AffilRule, 0, len(cfg.Groups))
	var removed *AffilRule
	for _, affil := range cfg.Groups {
		if strings.EqualFold(affil.Group, group) {
			copy := affil
			removed = &copy
			continue
		}
		kept = append(kept, affil)
	}
	if removed == nil {
		ctx.Reply("550 Affil %s not found.\r\n", group)
		return true
	}
	cfg.Groups = kept
	if err := saveAffilsFile(p.affilsFile, cfg); err != nil {
		ctx.Reply("451 Could not update %s: %v\r\n", p.affilsFile, err)
		return true
	}
	if err := removeAffilPermissions(p.permissionsFile, p.aclPath(removed.Predir), removed.Group); err != nil {
		ctx.Reply("200- Affil %s removed from %s\r\n", removed.Group, p.affilsFile)
		ctx.Reply("200- WARNING: could not update %s: %v\r\n", p.permissionsFile, err)
		ctx.Reply("200 Predir %s was left on disk.\r\n", removed.Predir)
		return true
	}
	ctx.Reply("200 Affil %s removed. Predir %s was left on disk.\r\n", removed.Group, removed.Predir)
	return true
}

func (p *Plugin) canAdmin(ctx plugin.SiteContext) bool {
	flags := ctx.UserFlags()
	for _, required := range p.adminFlags {
		if required <= 32 {
			continue
		}
		if strings.ContainsRune(flags, required) {
			return true
		}
	}
	return false
}

func (p *Plugin) aclPath(vpath string) string {
	vpath = cleanAbs(vpath)
	base := cleanAbs(p.aclBase)
	if strings.EqualFold(base, "/") {
		return vpath
	}
	if strings.HasPrefix(strings.ToLower(vpath), strings.ToLower(base)+"/") || strings.EqualFold(vpath, base) {
		return vpath
	}
	return path.Join(base, vpath)
}

func (p *Plugin) findUserAffil(userGroups []string) *AffilRule {
	if len(userGroups) == 0 {
		return nil
	}
	affils := p.currentAffils()
	groupSet := map[string]bool{}
	for _, group := range userGroups {
		groupSet[strings.ToLower(strings.TrimSpace(group))] = true
	}
	for i := range affils {
		if strings.TrimSpace(affils[i].Group) == "" || strings.TrimSpace(affils[i].Predir) == "" {
			continue
		}
		if groupSet[strings.ToLower(affils[i].Group)] {
			return &affils[i]
		}
	}
	return nil
}

func (p *Plugin) currentAffils() []AffilRule {
	cfg, err := loadAffilsFile(p.affilsFile)
	if err != nil {
		if p.debug && strings.TrimSpace(p.affilsFile) != "" {
			p.logf("could not read %s: %v", p.affilsFile, err)
		}
		return append([]AffilRule(nil), p.affils...)
	}
	base := p.base
	if strings.TrimSpace(cfg.Base) != "" {
		base = cleanAbs(cfg.Base)
	}
	if len(cfg.Groups) == 0 {
		return append([]AffilRule(nil), p.affils...)
	}
	return normalizeAffils(cfg.Groups, base)
}

func (p *Plugin) dirExists(dirPath string) bool {
	dirPath = cleanAbs(dirPath)
	if dirPath == "/" {
		return true
	}
	parent := path.Dir(dirPath)
	name := path.Base(dirPath)
	return p.childDirExists(parent, name)
}

func (p *Plugin) childDirExists(parent, name string) bool {
	parent = cleanAbs(parent)
	for _, entry := range p.svc.Bridge.PluginListDir(parent) {
		if strings.EqualFold(entry.Name, name) && entry.IsDir {
			return true
		}
	}
	return false
}

func (p *Plugin) runBWSampler(dst, relname, section, group string) {
	duration := p.bwDuration
	if duration <= 0 {
		duration = 30
	}
	intervalMs := p.bwIntervalMs
	if intervalMs <= 0 {
		intervalMs = 500
	}
	poll := time.Duration(intervalMs) * time.Millisecond
	slots := (duration * 1000) / intervalMs
	if slots < 1 {
		slots = 1
	}

	caps := []int{2, 3, 5, 10, 10}
	perSec := make([]int64, slots+1)
	type userAgg struct {
		peakBps int64
		sumBps  int64
		samples int
		bytes   int64
		files   int
	}
	userAggs := map[string]*userAgg{}
	prev := map[string]userSnapshot{}
	idleSlots := 0
	const idleBreak = 20

	for slot := 1; slot <= slots; slot++ {
		time.Sleep(poll)

		users, _, _, _, _ := p.svc.Bridge.PluginGetVFSRaceStats(dst)
		slotTotalBps := int64(0)
		anyActivity := false

		for _, u := range users {
			cur := userSnapshot{Bytes: u.Bytes, Files: u.Files}
			old, had := prev[u.Name]
			deltaBytes := int64(0)
			if had {
				deltaBytes = cur.Bytes - old.Bytes
				if deltaBytes < 0 {
					deltaBytes = 0
				}
			}
			prev[u.Name] = cur

			bps := int64(float64(deltaBytes) * 1000.0 / float64(intervalMs))
			if bps > 0 {
				anyActivity = true
			}
			slotTotalBps += bps

			agg := userAggs[u.Name]
			if agg == nil {
				agg = &userAgg{}
				userAggs[u.Name] = agg
			}
			if bps > agg.peakBps {
				agg.peakBps = bps
			}
			agg.sumBps += bps
			agg.samples++
			agg.bytes = u.Bytes
			agg.files = u.Files
		}

		perSec[slot] = slotTotalBps
		if !anyActivity {
			idleSlots++
			if idleSlots*intervalMs/1000 >= idleBreak {
				perSec = perSec[:slot+1]
				break
			}
		} else {
			idleSlots = 0
		}
	}

	actualSlots := len(perSec) - 1
	if actualSlots < 1 {
		actualSlots = 1
	}
	var sum, peak int64
	for i := 1; i <= actualSlots && i < len(perSec); i++ {
		sum += perSec[i]
		if perSec[i] > peak {
			peak = perSec[i]
		}
	}
	avg := int64(0)
	if actualSlots > 0 {
		avg = sum / int64(actualSlots)
	}

	intervalSnaps := make([][2]interface{}, 0, len(caps))
	cum := 0
	for _, cap := range caps {
		cum += cap
		idx := (cum * 1000) / intervalMs
		if idx > actualSlots {
			idx = actualSlots
		}
		bps := int64(0)
		if idx < len(perSec) {
			bps = perSec[idx]
		}
		intervalSnaps = append(intervalSnaps, [2]interface{}{bps, cum})
	}

	var grandBytes int64
	for _, u := range userAggs {
		grandBytes += u.bytes
	}

	trafV, trafU := fmtSize(grandBytes)
	avgV, avgU := fmtBps(avg)
	peakV, peakU := fmtBps(peak)
	p.emit("PREBW", dst, relname, section, grandBytes, 0, map[string]string{
		"relname":      relname,
		"section":      section,
		"group":        group,
		"traffic_val":  trafV,
		"traffic_unit": trafU,
		"avg_val":      avgV,
		"avg_unit":     avgU,
		"peak_val":     peakV,
		"peak_unit":    peakU,
	})

	intervalData := map[string]string{
		"relname":   relname,
		"section":   section,
		"group":     group,
		"peak_val":  peakV,
		"peak_unit": peakU,
	}
	for i, snap := range intervalSnaps {
		bps := snap[0].(int64)
		tm := snap[1].(int)
		v, u := fmtBps(bps)
		intervalData[fmt.Sprintf("b%d", i+1)] = v
		intervalData[fmt.Sprintf("u%d", i+1)] = u
		intervalData[fmt.Sprintf("t%d", i+1)] = fmt.Sprintf("%d", tm)
	}
	p.emit("PREBWINTERVAL", dst, relname, section, 0, 0, intervalData)

	type userRow struct {
		name  string
		bytes int64
		files int
		avg   int64
		peak  int64
	}
	rows := make([]userRow, 0, len(userAggs))
	for name, a := range userAggs {
		if a.files == 0 {
			continue
		}
		avgUser := int64(0)
		if a.samples > 0 {
			avgUser = a.sumBps / int64(a.samples)
		}
		rows = append(rows, userRow{name: name, bytes: a.bytes, files: a.files, avg: avgUser, peak: a.peakBps})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].bytes > rows[j].bytes })
	for _, r := range rows {
		sv, su := fmtSize(r.bytes)
		avgV, avgU := fmtBps(r.avg)
		peakV, peakU := fmtBps(r.peak)
		p.emit("PREBWUSER", dst, relname, section, r.bytes, 0, map[string]string{
			"relname":        relname,
			"section":        section,
			"group":          group,
			"user":           r.name,
			"size_val":       sv,
			"size_unit":      su,
			"cnt_files":      fmt.Sprintf("%d", r.files),
			"avg_val_user":   avgV,
			"avg_unit_user":  avgU,
			"peak_val_user":  peakV,
			"peak_unit_user": peakU,
		})
	}
}

func (p *Plugin) emit(eventType, eventPath, filename, section string, size int64, speed float64, data map[string]string) {
	if p.svc == nil || p.svc.EmitEvent == nil {
		return
	}
	p.svc.EmitEvent(eventType, eventPath, filename, section, size, speed, data)
}

func (p *Plugin) logf(format string, args ...interface{}) {
	if p.svc != nil && p.svc.Logger != nil {
		p.svc.Logger.Printf("[PRE] "+format, args...)
		return
	}
	if p.debug {
		log.Printf("[PRE] "+format, args...)
	}
}

func sectionAllowed(allowed []string, section string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, value := range allowed {
		if strings.EqualFold(value, section) {
			return true
		}
	}
	return false
}

func sectionIsDated(dated []string, section string) bool {
	for _, value := range dated {
		if strings.EqualFold(value, section) {
			return true
		}
	}
	return false
}

func cleanAbs(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func fmtSize(b int64) (string, string) {
	if b >= 1<<30 {
		return fmt.Sprintf("%.2f", float64(b)/float64(1<<30)), "GB"
	}
	return fmt.Sprintf("%.1f", float64(b)/float64(1<<20)), "MB"
}

func fmtBps(bps int64) (string, string) {
	mb := float64(bps) / float64(1<<20)
	if mb >= 1024 {
		return fmt.Sprintf("%.1f", mb/1024.0), "GB/s"
	}
	return fmt.Sprintf("%.1f", mb), "MB/s"
}

func stringConfig(cfg map[string]interface{}, key, fallback string) string {
	if raw, ok := cfg[key]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return fallback
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

func stringSliceConfig(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func affilRulesConfig(raw interface{}) []AffilRule {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]AffilRule, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		group, _ := m["group"].(string)
		predir, _ := m["predir"].(string)
		group = strings.TrimSpace(group)
		predir = strings.TrimSpace(predir)
		if group == "" {
			continue
		}
		out = append(out, AffilRule{Group: group, Predir: predir})
	}
	return out
}

func normalizeAffils(in []AffilRule, base string) []AffilRule {
	out := make([]AffilRule, 0, len(in))
	for _, affil := range in {
		affil.Group = strings.TrimSpace(affil.Group)
		affil.Predir = strings.TrimSpace(affil.Predir)
		if affil.Group == "" {
			continue
		}
		if affil.Predir == "" {
			affil.Predir = path.Join(base, affil.Group)
		}
		affil.Predir = cleanAbs(affil.Predir)
		out = append(out, affil)
	}
	return out
}

func loadAffilsFile(filePath string) (affilsFileConfig, error) {
	var cfg affilsFileConfig
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return cfg, fmt.Errorf("empty affils file path")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func saveAffilsFile(filePath string, cfg affilsFileConfig) error {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return fmt.Errorf("empty affils file path")
	}
	if strings.TrimSpace(cfg.Base) == "" {
		cfg.Base = "/PRE"
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, data, 0644)
}

func (p *Plugin) currentAffilsFileConfig() affilsFileConfig {
	cfg, err := loadAffilsFile(p.affilsFile)
	if err == nil {
		if strings.TrimSpace(cfg.Base) == "" {
			cfg.Base = p.base
		}
		cfg.Groups = normalizeAffils(cfg.Groups, cleanAbs(cfg.Base))
		return cfg
	}
	return affilsFileConfig{
		Base:   p.base,
		Groups: normalizeAffils(append([]AffilRule(nil), p.affils...), p.base),
	}
}

func validAffilGroup(group string) bool {
	if group == "" || strings.ContainsAny(group, `/\:*?"<>|`) {
		return false
	}
	for _, r := range group {
		if r <= 32 {
			return false
		}
	}
	return true
}

func ensureAffilPermissions(filePath, aclPredir, group string) error {
	cfg, err := loadPermissionsFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	required := "=" + group + " =SiteOP"
	rule := permissionRule{Type: "privpath", Path: aclPredir, Required: required}
	cfg.Rules = removeLegacyAffilRules(cfg.Rules, aclPredir, group)
	if !hasPermissionRule(cfg.Rules, rule) {
		cfg.Rules = append([]permissionRule{rule}, cfg.Rules...)
	}
	return savePermissionsFile(filePath, cfg)
}

func removeAffilPermissions(filePath, aclPredir, group string) error {
	cfg, err := loadPermissionsFile(filePath)
	if err != nil {
		return err
	}
	required := "=" + group
	requiredWithSiteOP := "=" + group + " =SiteOP"
	kept := make([]permissionRule, 0, len(cfg.Rules))
	for _, rule := range cfg.Rules {
		ruleRequired := strings.TrimSpace(rule.Required)
		if (strings.EqualFold(ruleRequired, required) || strings.EqualFold(ruleRequired, requiredWithSiteOP)) &&
			(strings.EqualFold(rule.Path, aclPredir) || strings.HasPrefix(strings.ToLower(rule.Path), strings.ToLower(aclPredir)+"/")) {
			continue
		}
		kept = append(kept, rule)
	}
	cfg.Rules = kept
	return savePermissionsFile(filePath, cfg)
}

func removeLegacyAffilRules(rules []permissionRule, aclPredir, group string) []permissionRule {
	required := "=" + group
	actionPath := path.Join(aclPredir, "*")
	kept := make([]permissionRule, 0, len(rules))
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule.Required), required) {
			rulePath := strings.TrimSpace(rule.Path)
			if strings.EqualFold(strings.TrimSpace(rule.Type), "privpath") && strings.EqualFold(rulePath, aclPredir) {
				continue
			}
			if strings.EqualFold(rulePath, actionPath) && isLegacyAffilActionRule(rule.Type) {
				continue
			}
		}
		kept = append(kept, rule)
	}
	return kept
}

func isLegacyAffilActionRule(ruleType string) bool {
	switch strings.ToLower(strings.TrimSpace(ruleType)) {
	case "upload", "resume", "download", "makedir", "delete", "rename", "dirlog", "nodupecheck":
		return true
	default:
		return false
	}
}

func loadPermissionsFile(filePath string) (permissionsFileConfig, error) {
	var cfg permissionsFileConfig
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return cfg, fmt.Errorf("empty permissions file path")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func savePermissionsFile(filePath string, cfg permissionsFileConfig) error {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return fmt.Errorf("empty permissions file path")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(renderPermissionsFile(cfg)), 0644)
}

func renderPermissionsFile(cfg permissionsFileConfig) string {
	var b strings.Builder
	b.WriteString(`# GoFTPd ACL rules.
#
# How matching works:
#   - Rules are checked top to bottom inside the requested type.
#   - The first matching rule decides access.
#   - If no action rule matches, matching privpath rules are checked.
#   - If nothing matches, flag 1 users are allowed by default.
#
# path:
#   - Paths are virtual paths under acl_base_path, normally /site.
#   - "*" is a wildcard. /site/* matches everything below /site.
#   - A trailing slash limits wildcard matches to one directory level,
#     glftpd-style:
#       /site/series/*/ matches /site/series/Release/
#       but not /site/series/Release/Sample/
#
# required:
#   "*"           anyone
#   "1"           user must have flag 1
#   "A"           user must have flag A
#   "=Admin"      user must be a member of group Admin
#   "@Nick"       user must be FTP user Nick
#   "=GROUP =SiteOP" user must be in GROUP OR SiteOP
#   "!4"          user must NOT have flag 4
#   "!=Group"     user must NOT be in group Group
#   "!@Nick"      user must NOT be FTP user Nick
#   "!*"          nobody; explicit deny
#   "1 =SiteOP"   user must have flag 1 AND be in group SiteOP
#   "1 A =NUKERS" user must have flags 1 and A AND be in group NUKERS
#
# rule types used here:
#   privpath      hides/blocks paths unless the user matches required
#   upload        STOR permission
#   resume        resume permission placeholder for clients/configs
#   download      RETR permission
#   makedir       MKD and RMD permission
#   dirlog        visibility for SITE SEARCH / dirlog-like listings
#   rename        rename permission
#   renameown     owner-only rename policy
#   nuke          SITE NUKE permission
#   unnuke        SITE UNNUKE permission
#   delete        DELE permission
#   deleteown     owner-only delete policy
#   filemove      cross-directory/manual move permission placeholder
#   nodupecheck   marks paths intended to skip dupe-db checks; overwrite
#                 protection still rejects uploads to an existing filename
#   sitecmd       controls who may run SITE subcommands by command name
#
rules:
`)
	sections := []struct {
		Name  string
		About string
		Types []string
	}{
		{
			Name:  "SITE commands",
			About: "Command-level ACLs. Use a specific command before the wildcard for exceptions.",
			Types: []string{"sitecmd"},
		},
		{
			Name:  "Private paths and affil predirs",
			About: "Private paths hide/block dirs unless the user matches required. Affil PRE dirs should normally be only one privpath rule, e.g. =GROUP =SiteOP.",
			Types: []string{"privpath"},
		},
		{
			Name:  "Transfers",
			About: "Upload, resume, and download access. Speedtest credits are handled in code; these rules only grant access.",
			Types: []string{"upload", "resume", "download"},
		},
		{
			Name:  "Directory creation/removal",
			About: "MKD and RMD checks use makedir rules.",
			Types: []string{"makedir"},
		},
		{
			Name:  "Directory logs/search",
			About: "Controls SITE SEARCH and dirlog-like visibility.",
			Types: []string{"dirlog"},
		},
		{
			Name:  "Rename",
			About: "Rename rules control RNFR/RNTO. renameown allows matching users to rename only their own entries.",
			Types: []string{"rename", "renameown"},
		},
		{
			Name:  "Nuke/unnuke",
			About: "Separate permissions for SITE NUKE and SITE UNNUKE.",
			Types: []string{"nuke", "unnuke"},
		},
		{
			Name:  "Delete",
			About: "Delete rules control DELE. deleteown allows matching users to delete only their own entries.",
			Types: []string{"delete", "deleteown"},
		},
		{
			Name:  "File moves",
			About: "Explicit filemove support, usually Admin-only.",
			Types: []string{"filemove"},
		},
		{
			Name:  "Dupe-check exclusions",
			About: "Paths intended to skip dupe-db checks. Existing-file overwrite protection still applies.",
			Types: []string{"nodupecheck"},
		},
	}
	used := make([]bool, len(cfg.Rules))
	for _, section := range sections {
		wroteHeader := false
		for i, rule := range cfg.Rules {
			if used[i] || !containsStringFold(section.Types, rule.Type) {
				continue
			}
			if !wroteHeader {
				writePermissionSectionHeader(&b, section.Name, section.About)
				wroteHeader = true
			}
			writePermissionRule(&b, rule)
			used[i] = true
		}
	}
	wroteOther := false
	for i, rule := range cfg.Rules {
		if used[i] {
			continue
		}
		if !wroteOther {
			writePermissionSectionHeader(&b, "Other rules", "Rules not recognized by the standard example sections are preserved here.")
			wroteOther = true
		}
		writePermissionRule(&b, rule)
	}
	return b.String()
}

func writePermissionSectionHeader(b *strings.Builder, title, about string) {
	b.WriteString("\n  # ---------------------------------------------------------------------------\n")
	b.WriteString("  # " + title + "\n")
	if strings.TrimSpace(about) != "" {
		b.WriteString("  # " + about + "\n")
	}
	b.WriteString("  # ---------------------------------------------------------------------------\n")
}

func writePermissionRule(b *strings.Builder, rule permissionRule) {
	b.WriteString("  - type: ")
	b.WriteString(strconv.Quote(strings.TrimSpace(rule.Type)))
	b.WriteByte('\n')
	b.WriteString("    path: ")
	b.WriteString(strconv.Quote(strings.TrimSpace(rule.Path)))
	b.WriteByte('\n')
	b.WriteString("    required: ")
	b.WriteString(strconv.Quote(strings.TrimSpace(rule.Required)))
	b.WriteByte('\n')
}

func containsStringFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}

func hasPermissionRule(rules []permissionRule, needle permissionRule) bool {
	for _, rule := range rules {
		if strings.EqualFold(rule.Type, needle.Type) &&
			strings.EqualFold(rule.Path, needle.Path) &&
			strings.EqualFold(strings.TrimSpace(rule.Required), strings.TrimSpace(needle.Required)) {
			return true
		}
	}
	return false
}

func ensureGroupFile(filePath, group string) error {
	filePath = strings.TrimSpace(filePath)
	group = strings.TrimSpace(group)
	if filePath == "" || group == "" {
		return fmt.Errorf("missing group file or group")
	}
	data, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	maxGID := 999
	found := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) >= 1 && strings.EqualFold(parts[0], group) {
			found = true
		}
		if len(parts) >= 3 {
			if gid, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil && gid > maxGID {
				maxGID = gid
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}
	if !found {
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(f, "%s:%s:%d:\n", group, group, maxGID+1); err != nil {
			_ = f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
	}
	return ensurePerGroupFile(filepath.Join(filepath.Dir(filePath), "groups"), group)
}

func ensurePerGroupFile(groupsDir, group string) error {
	groupsDir = strings.TrimSpace(groupsDir)
	group = strings.TrimSpace(group)
	if groupsDir == "" || group == "" {
		return fmt.Errorf("missing groups directory or group")
	}
	target := filepath.Join(groupsDir, group)
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(groupsDir, 0755); err != nil {
		return err
	}
	content, err := renderDefaultGroupFile(groupsDir, group)
	if err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0644)
}

func renderDefaultGroupFile(groupsDir, group string) (string, error) {
	for _, templatePath := range []string{
		filepath.Join(groupsDir, "default.group"),
		filepath.Join(groupsDir, "default.groups"),
	} {
		data, err := os.ReadFile(templatePath)
		if err == nil {
			content := strings.ReplaceAll(string(data), "%group", group)
			content = strings.ReplaceAll(content, "{group}", group)
			content = strings.ReplaceAll(content, "{{group}}", group)
			if !hasGroupHeader(content) {
				content = fmt.Sprintf("GROUP %s\n%s", group, content)
			}
			if !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			return content, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
	}
	return fmt.Sprintf("GROUP %s\nSLOTS -1 0 0 0\nGROUPNFO\nSIMULT 0\n", group), nil
}

func hasGroupHeader(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.HasPrefix(strings.ToUpper(line), "GROUP ")
	}
	return false
}
