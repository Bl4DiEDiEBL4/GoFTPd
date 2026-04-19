package plugin

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"goftpd/sitebot/internal/event"
	tmpl "goftpd/sitebot/internal/template"
)

type releaseState struct {
	FirstUser     string
	Users         map[string]bool
	HasSFV        bool
	FirstRar      bool
	Created       bool
	HalfwayDone   bool
	CurrentLeader string
	LastSeen      time.Time
}

type AnnouncePlugin struct {
	debug bool
	theme *tmpl.Theme
	mu    sync.Mutex
	state map[string]*releaseState
}

func NewAnnouncePlugin() *AnnouncePlugin { return &AnnouncePlugin{state: map[string]*releaseState{}} }
func (p *AnnouncePlugin) Name() string   { return "Announce" }
func (p *AnnouncePlugin) Initialize(config map[string]interface{}) error {
	if debug, ok := config["debug"].(bool); ok {
		p.debug = debug
	}
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}
	return nil
}
func (p *AnnouncePlugin) Close() error { return nil }

func releaseName(evt *event.Event) string {
	if evt.Path == "" {
		return evt.Filename
	}
	clean := path.Clean(evt.Path)
	base := path.Base(clean)
	// For MKDIR/RMDIR/RACEEND/PRE, Path IS the release directory itself.
	if evt.Type == event.EventMKDir || evt.Type == event.EventRMDir || evt.Type == event.EventRaceEnd ||
		evt.Type == event.EventPre || evt.Type == event.EventPreBW ||
		evt.Type == event.EventPreBWUser || evt.Type == event.EventPreBWInterval {
		return base
	}
	name := strings.ToLower(evt.Filename)
	if strings.Contains(name, ".") {
		return path.Base(path.Dir(clean))
	}
	return base
}
func classifyFile(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".sfv"):
		return "sfv"
	case strings.HasSuffix(l, ".nfo"):
		return "nfo"
	case strings.Contains(l, "/sample/") || strings.Contains(l, ".sample."):
		return "sample"
	case regexpRAR(l):
		return "rar"
	default:
		return "other"
	}
}
func regexpRAR(name string) bool {
	if strings.HasSuffix(name, ".rar") {
		return true
	}
	if len(name) >= 4 && name[len(name)-4] == '.' && name[len(name)-3] == 'r' {
		return name[len(name)-2] >= '0' && name[len(name)-2] <= '9' && name[len(name)-1] >= '0' && name[len(name)-1] <= '9'
	}
	return false
}
func speedMB(evt *event.Event) string { return fmt.Sprintf("%.2fMB/s", evt.Speed) }
func mb(size int64) string            { return fmt.Sprintf("%.0fMB", float64(size)/1024.0/1024.0) }

func (p *AnnouncePlugin) vars(evt *event.Event) map[string]string {
	rel := releaseName(evt)
	v := map[string]string{
		"section":  evt.Section,
		"relname":  rel,
		"reldir":   rel,
		"u_name":   evt.User,
		"g_name":   evt.Group,
		"filename": evt.Filename,
		"path":     evt.Path,
		"u_speed":  speedMB(evt),
		"t_mbytes": mb(evt.Size),
	}
	for k, val := range evt.Data {
		v[k] = val
	}
	if v["relname"] == "" {
		v["relname"] = rel
	}
	if v["reldir"] == "" {
		v["reldir"] = v["relname"]
	}
	return v
}
func (p *AnnouncePlugin) render(key string, vars map[string]string, fallback string) string {
	if p.theme != nil {
		if raw, ok := p.theme.Announces[key]; ok && raw != "" {
			return tmpl.Render(raw, vars)
		}
	}
	return fallback
}

func (p *AnnouncePlugin) OnEvent(evt *event.Event) ([]Output, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rel := releaseName(evt)
	if rel == "." || rel == "/" || rel == "" {
		rel = evt.Filename
	}
	st := p.state[rel]
	if st == nil {
		st = &releaseState{Users: map[string]bool{}, LastSeen: time.Now()}
		p.state[rel] = st
	}
	st.LastSeen = time.Now()
	vars := p.vars(evt)
	section := vars["section"]
	if section == "" {
		section = "DEFAULT"
		vars["section"] = section
	}
	fileType := classifyFile(strings.ToLower(evt.Path))
	outs := []Output{}

	switch evt.Type {
	case event.EventMKDir:
		// only top-level release dirs
		if path.Base(path.Dir(path.Clean(evt.Path))) == strings.ToUpper(section) || strings.EqualFold(path.Dir(path.Clean(evt.Path)), "/"+section) {
			if !st.Created {
				st.Created = true
				outs = append(outs, Output{Type: "NEW", Text: p.render("NEWDIR", vars, fmt.Sprintf("NEW : [%s] %s by %s", section, rel, evt.User))})
			}
		}
	case event.EventUpload:
		switch fileType {
		case "nfo", "sample":
			return nil, nil
		case "sfv":
			if !st.HasSFV {
				st.HasSFV = true
				outs = append(outs, Output{Type: "RACE", Text: p.render("SFV_RAR", vars, fmt.Sprintf("RACE: [%s] Got SFV for %s uploaded by %s.", section, rel, evt.User))})
			}
		case "rar":
			if evt.User != "" && !st.Users[evt.User] {
				st.Users[evt.User] = true
				if !st.FirstRar {
					st.FirstRar = true
					outs = append(outs, Output{Type: "RACE", Text: p.render("UPDATE_RAR", vars, fmt.Sprintf("RACE: [%s] %s got its first rar file from %s at %s.", section, rel, evt.User, speedMB(evt)))})
				} else {
					outs = append(outs, Output{Type: "RACE", Text: p.render("RACE_RAR", vars, fmt.Sprintf("RACE: [%s] %s - %s joined the race at %s.", section, rel, evt.User, speedMB(evt)))})
				}
			}
			// NEW LEADER: announce when the leading user changes (skip single-user races)
			if leader := vars["leader_name"]; leader != "" && leader != st.CurrentLeader && len(st.Users) > 1 {
				if st.CurrentLeader != "" {
					outs = append(outs, Output{Type: "RACE", Text: p.render("NEWLEADER", vars, fmt.Sprintf("NEW LEADER: [%s] %s - %s takes the lead - %sMB/%sF/%s%%/%s", section, rel, leader, vars["leader_mb"], vars["leader_files"], vars["leader_pct"], vars["leader_speed"]))})
				}
				st.CurrentLeader = leader
			}
			// HALFWAY: announce when race passes 50% of file count
			if !st.HalfwayDone {
				if present, total := vars["t_present"], vars["t_files"]; present != "" && total != "" {
					var p1, t1 int
					fmt.Sscanf(present, "%d", &p1)
					fmt.Sscanf(total, "%d", &t1)
					if t1 > 0 && p1*2 >= t1 && p1 < t1 {
						st.HalfwayDone = true
						left := t1 - p1
						vars["t_filesleft"] = fmt.Sprintf("%d", left)
						outs = append(outs, Output{Type: "RACE", Text: p.render("HALFWAY", vars, fmt.Sprintf("HALFWAY: [%s] %s - leader: %s [%sMB/%sF/%s%%/%s] - %d files left.", section, rel, vars["leader_name"], vars["leader_mb"], vars["leader_files"], vars["leader_pct"], vars["leader_speed"], left))})
					}
				}
			}
		}
	case event.EventRaceEnd:
		outs = append(outs, Output{Type: "COMPLETE", Text: p.render("COMPLETE", vars, fmt.Sprintf("COMPLETE: [%s] %s by %s racers - %s/%s/%s/%s", section, rel, vars["u_count"], vars["t_mbytes"], vars["t_files"], vars["t_avgspeed"], vars["t_duration"]))})
	case event.EventRaceStats:
		if line := strings.TrimSpace(p.render("STATS_HOF", vars, "STATS: Users Hall Of Fame")); line != "" {
			outs = append(outs, Output{Type: "STATS", Text: line})
		}
		if line := strings.TrimSpace(p.render("STATS_SPEEDS", vars, fmt.Sprintf("STATS: Slowest: %s at %s - Fastest: %s at %s.", vars["u_slowest_name"], vars["u_slowest_speed"], vars["u_fastest_name"], vars["u_fastest_speed"]))); line != "" {
			outs = append(outs, Output{Type: "STATS", Text: line})
		}
	case event.EventRaceUser:
		perVars := map[string]string{}
		for k, v := range vars {
			perVars[k] = v
		}
		perVars["u_name"] = vars["u_racer_name"]
		perVars["u_group"] = vars["u_racer_group"]
		perVars["u_files"] = vars["u_racer_files"]
		perVars["u_mb"] = vars["u_racer_mb"]
		perVars["u_pct"] = vars["u_racer_pct"]
		perVars["u_speed"] = vars["u_racer_speed"]
		fallback := fmt.Sprintf("STATS: [%s] %s/%s %sMB %s%% %s", vars["u_rank"], perVars["u_name"], perVars["u_group"], perVars["u_mb"], perVars["u_pct"], perVars["u_speed"])
		if line := strings.TrimSpace(p.render("STATS_USER", perVars, fallback)); line != "" {
			outs = append(outs, Output{Type: "STATS", Text: line})
		}
	case event.EventRaceFooter:
		if line := strings.TrimSpace(p.render("STATS_END", vars, "STATS: -----------====>>>>           END          <<<<====-----------")); line != "" {
			outs = append(outs, Output{Type: "STATS", Text: line})
		}
	case event.EventNuke:
		outs = append(outs, Output{Type: "NUKE", Text: p.render("NUKE", vars, fmt.Sprintf("NUKE: [%s] %s by %s", section, rel, evt.User))})
	case event.EventUnnuke:
		outs = append(outs, Output{Type: "UNNUKE", Text: p.render("UNNUKE", vars, fmt.Sprintf("UNNUKE: [%s] %s by %s", section, rel, evt.User))})
	case event.EventPre:
		group := vars["group"]
		user := vars["user"]
		if group == "" {
			group = evt.Group
		}
		if user == "" {
			user = evt.User
		}
		fallback := fmt.Sprintf("PRE: [%s] %s by %s (%s) - %s/%s", section, rel, group, user, vars["t_mbytes"], vars["t_files"])
		outs = append(outs, Output{Type: "PRE", Text: p.render("PRE", vars, fallback)})
	case event.EventPreBW:
		fallback := fmt.Sprintf("PREBW: [%s] %s :: %s%s @ %s%s :: Highest %s%s",
			section, rel,
			vars["traffic_val"], vars["traffic_unit"],
			vars["avg_val"], vars["avg_unit"],
			vars["peak_val"], vars["peak_unit"])
		outs = append(outs, Output{Type: "PREBW", Text: p.render("PREBW", vars, fallback)})
	case event.EventPreBWInterval:
		fallback := fmt.Sprintf("PREBW: [%s] %s :: %s%s@%ss :: %s%s@%ss :: %s%s@%ss :: %s%s@%ss :: %s%s@%ss :: Highest %s%s",
			section, rel,
			vars["b1"], vars["u1"], vars["t1"],
			vars["b2"], vars["u2"], vars["t2"],
			vars["b3"], vars["u3"], vars["t3"],
			vars["b4"], vars["u4"], vars["t4"],
			vars["b5"], vars["u5"], vars["t5"],
			vars["peak_val"], vars["peak_unit"])
		outs = append(outs, Output{Type: "PREBW", Text: p.render("PREBWINTERVAL", vars, fallback)})
	case event.EventPreBWUser:
		fallback := fmt.Sprintf("PREBW: [%s] %s :: %s%s/%sF @ %s%s :: Highest %s%s",
			section, vars["user"],
			vars["size_val"], vars["size_unit"], vars["cnt_files"],
			vars["avg_val_user"], vars["avg_unit_user"],
			vars["peak_val_user"], vars["peak_unit_user"])
		outs = append(outs, Output{Type: "PREBW", Text: p.render("PREBWUSER", vars, fallback)})
	}
	return outs, nil
}
