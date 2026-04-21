package pre

import (
	"fmt"
	"log"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"goftpd/internal/plugin"
)

type Plugin struct {
	svc           *plugin.Services
	base          string
	sections      []string
	datedSections []string
	bwDuration    int
	bwIntervalMs  int
	affils        []AffilRule
	debug         bool
}

type AffilRule struct {
	Group  string
	Predir string
}

type userSnapshot struct {
	Bytes int64
	Files int
}

func New() *Plugin {
	return &Plugin{
		base:         "/PRE",
		bwDuration:   30,
		bwIntervalMs: 500,
	}
}

func (p *Plugin) Name() string { return "pre" }

func (p *Plugin) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	p.svc = svc
	if s := stringConfig(cfg, "base", ""); strings.TrimSpace(s) != "" {
		p.base = cleanAbs(s)
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
	p.affils = affilRulesConfig(cfg["affils"])
	for i := range p.affils {
		if strings.TrimSpace(p.affils[i].Predir) == "" && strings.TrimSpace(p.affils[i].Group) != "" {
			p.affils[i].Predir = path.Join(p.base, p.affils[i].Group)
		}
		p.affils[i].Predir = cleanAbs(p.affils[i].Predir)
	}
	return nil
}

func (p *Plugin) OnEvent(evt *plugin.Event) error { return nil }

func (p *Plugin) Stop() error { return nil }

func (p *Plugin) SiteCommands() []string { return []string{"PRE"} }

func (p *Plugin) HandleSiteCommand(ctx plugin.SiteContext, command string, args []string) bool {
	if p.svc == nil || p.svc.Bridge == nil {
		ctx.Reply("451 Master bridge unavailable.\r\n")
		return true
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
		destSection = path.Join(destSection, time.Now().Format("0102"))
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

func (p *Plugin) findUserAffil(userGroups []string) *AffilRule {
	if len(userGroups) == 0 {
		return nil
	}
	groupSet := map[string]bool{}
	for _, group := range userGroups {
		groupSet[strings.ToLower(strings.TrimSpace(group))] = true
	}
	for i := range p.affils {
		if strings.TrimSpace(p.affils[i].Group) == "" || strings.TrimSpace(p.affils[i].Predir) == "" {
			continue
		}
		if groupSet[strings.ToLower(p.affils[i].Group)] {
			return &p.affils[i]
		}
	}
	return nil
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
