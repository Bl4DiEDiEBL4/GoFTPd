package quota

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
	"gopkg.in/yaml.v3"
)

const (
	statusTrial    = "trial"
	statusQuota    = "quota"
	statusDisabled = "disabled"
)

type trackedUser struct {
	Status         string `yaml:"status"`
	TrialStartUnix int64  `yaml:"trial_start_unix"`
	TrialDays      int    `yaml:"trial_days"`
	DaysRemaining  int    `yaml:"days_remaining"`
	IRCNick        string `yaml:"irc_nick,omitempty"`
	LastQuotaWeek  string `yaml:"last_quota_week,omitempty"`
}

type persistedState struct {
	Users map[string]*trackedUser `yaml:"users"`
}

type userSnapshot struct {
	Username   string
	Group      string
	Flags      string
	Ratio      int
	WkUpBytes  int64
	WkUpFiles  int64
	DayUpBytes int64
	DayFiles   int64
}

type displayUser struct {
	Username      string
	Group         string
	WkUpBytes     int64
	WkUpFiles     int64
	DaysRemaining int
}

type disableAction struct {
	Nick   string
	Reason string
}

type Plugin struct {
	debug            bool
	usersDir         string
	byeDir           string
	stateFile        string
	statusCommands   []string
	staffCommand     string
	replyTarget      string
	channels         []string
	staffChannels    []string
	staffHosts       []string
	staffUsers       []string
	includedGroups   []string
	skipUsers        []string
	trialEnabled     bool
	quotaEnabled     bool
	trialDaysDefault int
	trialQuotaBytes  int64
	quotaBytes       int64
	quotaFailback    bool
	disabledFlag     string
	scanInterval     time.Duration
	kickOnDisable    bool
	kickChannels     []string
	theme            *tmpl.Theme
	kicker           func(channel, nick, reason string)

	mu      sync.Mutex
	state   persistedState
	stop    chan struct{}
	stopped chan struct{}
}

func New() *Plugin {
	return &Plugin{
		usersDir:         "../etc/users",
		byeDir:           "../etc/byefiles",
		stateFile:        "plugins/quota/state.yml",
		statusCommands:   []string{"quota"},
		staffCommand:     "quotactl",
		replyTarget:      "channel",
		staffChannels:    []string{"#goftpd-staff"},
		trialEnabled:     true,
		quotaEnabled:     true,
		trialDaysDefault: 7,
		trialQuotaBytes:  gibToBytes(150),
		quotaBytes:       gibToBytes(250),
		quotaFailback:    true,
		disabledFlag:     "6",
		scanInterval:     5 * time.Minute,
		kickOnDisable:    true,
		state:            persistedState{Users: map[string]*trackedUser{}},
	}
}

func (p *Plugin) Name() string { return "Quota" }

func (p *Plugin) SetKicker(fn func(channel, nick, reason string)) {
	p.kicker = fn
}

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		if th, err := tmpl.LoadTheme(themeFile); err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "quota")
	if s, ok := stringConfig(cfg, config, "users_dir", "quota_users_dir"); ok && strings.TrimSpace(s) != "" {
		p.usersDir = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(cfg, config, "bye_dir", "quota_bye_dir"); ok && strings.TrimSpace(s) != "" {
		p.byeDir = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(cfg, config, "state_file", "quota_state_file"); ok && strings.TrimSpace(s) != "" {
		p.stateFile = strings.TrimSpace(s)
	}
	if raw, ok := configValueOK(cfg, config, "status_commands", "quota_status_commands"); ok {
		if cmds := normalizeCommands(plugin.ToStringSlice(raw, nil)); len(cmds) > 0 {
			p.statusCommands = cmds
		}
	}
	if s, ok := stringConfig(cfg, config, "status_command", "quota_status_command"); ok && strings.TrimSpace(s) != "" {
		p.statusCommands = normalizeCommands([]string{strings.TrimSpace(s)})
	}
	if s, ok := stringConfig(cfg, config, "staff_command", "quota_staff_command"); ok && strings.TrimSpace(s) != "" {
		p.staffCommand = strings.ToLower(strings.TrimSpace(s))
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "quota_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if raw, ok := configValueOK(cfg, config, "channels", "quota_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if raw, ok := configValueOK(cfg, config, "staff_channels", "quota_staff_channels"); ok {
		p.staffChannels = plugin.ToStringSlice(raw, p.staffChannels)
	}
	if raw, ok := configValueOK(cfg, config, "staff_hosts", "quota_staff_hosts"); ok {
		p.staffHosts = plugin.ToStringSlice(raw, p.staffHosts)
	}
	if raw, ok := configValueOK(cfg, config, "staff_users", "quota_staff_users"); ok {
		p.staffUsers = plugin.ToStringSlice(raw, p.staffUsers)
	}
	if raw, ok := configValueOK(cfg, config, "included_groups", "quota_included_groups"); ok {
		p.includedGroups = plugin.ToStringSlice(raw, p.includedGroups)
	}
	if raw, ok := configValueOK(cfg, config, "skip_users", "quota_skip_users"); ok {
		p.skipUsers = plugin.ToStringSlice(raw, p.skipUsers)
	}
	if b, ok := boolConfig(configValue(cfg, config, "trial_enabled", "quota_trial_enabled")); ok {
		p.trialEnabled = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "quota_enabled", "quota_quota_enabled")); ok {
		p.quotaEnabled = b
	}
	if n := intConfig(configValue(cfg, config, "trial_days_default", "quota_trial_days_default"), p.trialDaysDefault); n > 0 {
		p.trialDaysDefault = n
	}
	if n := intConfig(configValue(cfg, config, "scan_interval_seconds", "quota_scan_interval_seconds"), 0); n > 0 {
		p.scanInterval = time.Duration(n) * time.Second
	}
	if s, ok := stringConfig(cfg, config, "disabled_flag", "quota_disabled_flag"); ok && strings.TrimSpace(s) != "" {
		p.disabledFlag = strings.TrimSpace(s)
	}
	if b, ok := boolConfig(configValue(cfg, config, "quota_fail_back_to_trial", "quota_fail_back_to_trial")); ok {
		p.quotaFailback = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "kick_on_disable", "quota_kick_on_disable")); ok {
		p.kickOnDisable = b
	}
	if raw, ok := configValueOK(cfg, config, "kick_channels", "quota_kick_channels"); ok {
		p.kickChannels = plugin.ToStringSlice(raw, p.kickChannels)
	}
	if len(p.kickChannels) == 0 {
		if raw, ok := config["irc_channels"]; ok {
			p.kickChannels = plugin.ToStringSlice(raw, nil)
		}
	}
	if v, ok := floatConfig(configValue(cfg, config, "trial_quota_gb", "quota_trial_quota_gb")); ok && v >= 0 {
		p.trialQuotaBytes = gibToBytes(v)
	}
	if v, ok := floatConfig(configValue(cfg, config, "quota_gb", "quota_quota_gb")); ok && v >= 0 {
		p.quotaBytes = gibToBytes(v)
	}

	if err := p.loadState(); err != nil {
		return err
	}
	if err := p.scanAndProcess(); err != nil && p.debug {
		log.Printf("[Quota] initial scan failed: %v", err)
	}
	p.startLoop()
	return nil
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	stop := p.stop
	stopped := p.stopped
	p.stop = nil
	p.stopped = nil
	p.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if stopped != nil {
		<-stopped
	}
	return nil
}

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	switch evt.Type {
	case event.EventInvite:
		return nil, p.handleInvite(evt)
	case event.EventCommand:
		return p.handleCommand(evt)
	default:
		return nil, nil
	}
}

func (p *Plugin) handleInvite(evt *event.Event) error {
	inviter := strings.TrimSpace(evt.Data["inviter"])
	if inviter == "" {
		inviter = strings.TrimSpace(evt.User)
	}
	nick := strings.TrimSpace(evt.Data["nick"])
	if inviter == "" || nick == "" {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureStateUserLocked(inviter)
	if state.IRCNick == nick {
		return nil
	}
	state.IRCNick = nick
	return p.saveStateLocked()
}

func (p *Plugin) handleCommand(evt *event.Event) ([]plugin.Output, error) {
	cmd := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
	switch {
	case matchesCommand(cmd, p.statusCommands):
		if !p.channelAllowed(evt) && !p.canStaff(evt) {
			return p.reply(evt, p.render("QUOTACMD_DENIED", map[string]string{
				"response": "command not enabled in this channel.",
				"user":     evt.User,
			}, "QUOTA: command not enabled in this channel.")), nil
		}
		lines, err := p.buildStatusLines()
		if err != nil {
			return p.reply(evt, p.render("QUOTACMD_ERROR", map[string]string{
				"response": err.Error(),
				"user":     evt.User,
			}, "QUOTA: "+err.Error())), nil
		}
		return p.replies(evt, lines...), nil
	case cmd == p.staffCommand:
		if !p.canStaff(evt) {
			return p.reply(evt, p.render("QUOTACMD_DENIED", map[string]string{
				"response": "staff command only.",
				"user":     evt.User,
			}, "QUOTA: staff command only.")), nil
		}
		return p.handleStaffCommand(evt)
	default:
		return nil, nil
	}
}

func (p *Plugin) handleStaffCommand(evt *event.Event) ([]plugin.Output, error) {
	args := strings.Fields(strings.TrimSpace(evt.Data["args"]))
	if len(args) < 2 {
		return p.reply(evt, p.render("QUOTACMD_USAGE", map[string]string{
			"response": p.staffUsage(),
		}, "QUOTA: usage: "+p.staffUsage())), nil
	}

	action := strings.ToLower(strings.TrimSpace(args[0]))
	username := strings.TrimSpace(args[1])
	if username == "" {
		return p.reply(evt, p.render("QUOTACMD_USAGE", map[string]string{
			"response": p.staffUsage(),
		}, "QUOTA: usage: "+p.staffUsage())), nil
	}

	var line string
	var err error
	switch action {
	case "trial":
		days := p.trialDaysDefault
		if len(args) >= 3 {
			days, err = strconv.Atoi(args[2])
			if err != nil || days <= 0 {
				return p.reply(evt, p.render("QUOTACMD_ERROR", map[string]string{
					"response": "days must be a positive number",
				}, "QUOTA: days must be a positive number")), nil
			}
		}
		err = p.setUserTrial(username, days)
		line = fmt.Sprintf("QUOTA: %s set to TRIAL (%d days).", username, days)
	case "quota":
		err = p.setUserQuota(username)
		line = fmt.Sprintf("QUOTA: %s set to QUOTA.", username)
	case "extend":
		if len(args) < 3 {
			return p.reply(evt, p.render("QUOTACMD_USAGE", map[string]string{
				"response": p.staffExtendUsage(),
			}, "QUOTA: usage: "+p.staffExtendUsage())), nil
		}
		days, convErr := strconv.Atoi(args[2])
		if convErr != nil || days <= 0 {
			return p.reply(evt, p.render("QUOTACMD_ERROR", map[string]string{
				"response": "days must be a positive number",
			}, "QUOTA: days must be a positive number")), nil
		}
		err = p.extendTrial(username, days)
		line = fmt.Sprintf("QUOTA: %s trial reset to %d days.", username, days)
	case "delete":
		reason := "Manual Removal"
		if len(args) > 2 {
			reason = strings.TrimSpace(strings.Join(args[2:], " "))
		}
		err = p.disableUser(username, reason, 0)
		line = fmt.Sprintf("QUOTA: %s disabled.", username)
	default:
		return p.reply(evt, p.render("QUOTACMD_USAGE", map[string]string{
			"response": p.staffUsage(),
		}, "QUOTA: usage: "+p.staffUsage())), nil
	}
	if err != nil {
		return p.reply(evt, p.render("QUOTACMD_ERROR", map[string]string{
			"response": err.Error(),
		}, "QUOTA: "+err.Error())), nil
	}
	return p.reply(evt, p.render("QUOTACMD_OK", map[string]string{
		"response": line,
	}, line)), nil
}

func (p *Plugin) buildStatusLines() ([]string, error) {
	if err := p.scanAndProcess(); err != nil && p.debug {
		log.Printf("[Quota] refresh before status command failed: %v", err)
	}
	snapshots, err := p.loadSnapshots()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	quotas := make([]displayUser, 0)
	trials := make([]displayUser, 0)
	for username, state := range p.state.Users {
		snap, ok := snapshots[username]
		if !ok {
			continue
		}
		entry := displayUser{
			Username:      username,
			Group:         snap.Group,
			WkUpBytes:     snap.WkUpBytes,
			WkUpFiles:     snap.WkUpFiles,
			DaysRemaining: state.DaysRemaining,
		}
		switch state.Status {
		case statusQuota:
			quotas = append(quotas, entry)
		case statusTrial:
			trials = append(trials, entry)
		}
	}

	sortDisplayUsers(quotas)
	sortDisplayUsers(trials)

	lines := make([]string, 0, len(quotas)+len(trials)+2)
	if len(quotas) == 0 && len(trials) == 0 {
		lines = append(lines, p.render("QUOTACMD_EMPTY", map[string]string{
			"response": "No trial or quota users tracked.",
		}, "QUOTA: no trial or quota users tracked."))
		return lines, nil
	}

	lines = append(lines, p.render("QUOTACMD_QUOTA_HEADER", map[string]string{
		"count":       strconv.Itoa(len(quotas)),
		"quota_gb":    humanGB(p.quotaBytes),
		"trial_count": strconv.Itoa(len(trials)),
		"trial_quota": humanGB(p.trialQuotaBytes),
		"response":    "quota",
	}, fmt.Sprintf("WEEKLY QUOTA: [ %d Users - (Min %s) ]", len(quotas), humanGB(p.quotaBytes))))
	for idx, entry := range quotas {
		lines = append(lines, p.render("QUOTACMD_QUOTA_ENTRY", map[string]string{
			"rank":           fmt.Sprintf("%02d", idx+1),
			"user":           entry.Username,
			"group":          entry.Group,
			"size":           formatBytes(entry.WkUpBytes),
			"status":         passLabel(entry.WkUpBytes >= p.quotaBytes),
			"status_plain":   passWord(entry.WkUpBytes >= p.quotaBytes),
			"days_remaining": strconv.Itoa(entry.DaysRemaining),
		}, fmt.Sprintf("[ %02d ] %s/%s ( %s Up ) is currently %s.", idx+1, entry.Username, entry.Group, formatBytes(entry.WkUpBytes), passWord(entry.WkUpBytes >= p.quotaBytes))))
	}

	lines = append(lines, p.render("QUOTACMD_TRIAL_HEADER", map[string]string{
		"count":    strconv.Itoa(len(trials)),
		"quota_gb": humanGB(p.trialQuotaBytes),
		"response": "trial",
	}, fmt.Sprintf("TRIAL QUOTA: [ Trial List - %d Trialing - (Min %s) ]", len(trials), humanGB(p.trialQuotaBytes))))
	for idx, entry := range trials {
		lines = append(lines, p.render("QUOTACMD_TRIAL_ENTRY", map[string]string{
			"rank":           fmt.Sprintf("%02d", idx+1),
			"user":           entry.Username,
			"group":          entry.Group,
			"size":           formatBytes(entry.WkUpBytes),
			"status":         passLabel(entry.WkUpBytes >= p.trialQuotaBytes),
			"status_plain":   passWord(entry.WkUpBytes >= p.trialQuotaBytes),
			"days_remaining": strconv.Itoa(entry.DaysRemaining),
		}, fmt.Sprintf("[ %02d ] %s/trial ( %s Up ) is currently %s. (%d days left)", idx+1, entry.Username, formatBytes(entry.WkUpBytes), passWord(entry.WkUpBytes >= p.trialQuotaBytes), entry.DaysRemaining)))
	}

	return lines, nil
}

func (p *Plugin) loadSnapshots() (map[string]userSnapshot, error) {
	entries, err := os.ReadDir(p.usersDir)
	if err != nil {
		return nil, err
	}
	snapshots := make(map[string]userSnapshot, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || entry.IsDir() || strings.HasPrefix(name, ".") || strings.HasSuffix(strings.ToLower(name), ".lock") {
			continue
		}
		if containsFold(p.skipUsers, name) {
			continue
		}
		snap, err := parseUserSnapshot(filepath.Join(p.usersDir, name), name)
		if err != nil {
			if p.debug {
				log.Printf("[Quota] skipping %s: %v", name, err)
			}
			continue
		}
		if len(p.includedGroups) > 0 && !containsFold(p.includedGroups, snap.Group) {
			continue
		}
		snapshots[name] = snap
	}
	return snapshots, nil
}

func (p *Plugin) scanAndProcess() error {
	snapshots, err := p.loadSnapshots()
	if err != nil {
		return err
	}
	now := time.Now()
	weekKey := isoWeekKey(now)
	kicks := make([]disableAction, 0)

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Users == nil {
		p.state.Users = map[string]*trackedUser{}
	}

	changed := false
	for username, snap := range snapshots {
		state := p.state.Users[username]
		if state == nil {
			if p.hasDisabledFlag(snap.Flags) {
				continue
			}
			state = &trackedUser{
				Status:         statusTrial,
				TrialStartUnix: now.Unix(),
				TrialDays:      p.trialDaysDefault,
				DaysRemaining:  p.trialDaysDefault,
			}
			p.state.Users[username] = state
			changed = true
		}

		if state.Status != statusDisabled && p.hasDisabledFlag(snap.Flags) {
			state.Status = statusDisabled
			state.DaysRemaining = 0
			changed = true
			continue
		}

		switch state.Status {
		case statusTrial:
			if !p.trialEnabled {
				break
			}
			if state.TrialDays <= 0 {
				state.TrialDays = p.trialDaysDefault
				changed = true
			}
			if state.TrialStartUnix == 0 {
				state.TrialStartUnix = now.Unix()
				changed = true
			}
			left := trialDaysRemaining(state.TrialStartUnix, state.TrialDays, now)
			if left != state.DaysRemaining {
				state.DaysRemaining = left
				changed = true
			}
			if left <= 0 {
				if snap.WkUpBytes >= p.trialQuotaBytes {
					state.Status = statusQuota
					state.LastQuotaWeek = ""
					state.DaysRemaining = daysUntilSunday(now)
					changed = true
				} else {
					if err := p.applyDisabledSideEffectsLocked(username, state, "Trial Failure", snap.WkUpBytes); err != nil {
						if p.debug {
							log.Printf("[Quota] disable %s failed: %v", username, err)
						}
					} else {
						kicks = append(kicks, p.kickActionForLocked(state, "Trial Failed. Account Disabled.")...)
						changed = true
					}
				}
			}
		case statusQuota:
			if !p.quotaEnabled {
				break
			}
			daysLeft := daysUntilSunday(now)
			if daysLeft != state.DaysRemaining {
				state.DaysRemaining = daysLeft
				changed = true
			}
			if now.Weekday() == time.Sunday && state.LastQuotaWeek != weekKey {
				state.LastQuotaWeek = weekKey
				changed = true
				if snap.WkUpBytes < p.quotaBytes {
					if p.quotaFailback {
						state.Status = statusTrial
						state.TrialStartUnix = now.Unix()
						state.TrialDays = p.trialDaysDefault
						state.DaysRemaining = p.trialDaysDefault
					} else {
						if err := p.applyDisabledSideEffectsLocked(username, state, "Weekly Quota Failure", snap.WkUpBytes); err != nil {
							if p.debug {
								log.Printf("[Quota] disable %s failed: %v", username, err)
							}
						} else {
							kicks = append(kicks, p.kickActionForLocked(state, "Weekly Quota Failed.")...)
						}
					}
				}
			}
		}
	}

	if changed {
		if err := p.saveStateLocked(); err != nil {
			return err
		}
	}
	p.emitKicks(kicks)
	return nil
}

func (p *Plugin) setUserTrial(username string, days int) error {
	if err := p.ensureUserFile(username); err != nil {
		return err
	}
	if err := p.setDisabledFlag(username, false); err != nil {
		return err
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureStateUserLocked(username)
	state.Status = statusTrial
	state.TrialStartUnix = now.Unix()
	state.TrialDays = days
	state.DaysRemaining = days
	state.LastQuotaWeek = ""
	return p.saveStateLocked()
}

func (p *Plugin) setUserQuota(username string) error {
	if err := p.ensureUserFile(username); err != nil {
		return err
	}
	if err := p.setDisabledFlag(username, false); err != nil {
		return err
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureStateUserLocked(username)
	state.Status = statusQuota
	state.TrialStartUnix = 0
	state.TrialDays = p.trialDaysDefault
	state.DaysRemaining = daysUntilSunday(now)
	state.LastQuotaWeek = ""
	return p.saveStateLocked()
}

func (p *Plugin) extendTrial(username string, days int) error {
	if err := p.ensureUserFile(username); err != nil {
		return err
	}
	if err := p.setDisabledFlag(username, false); err != nil {
		return err
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.ensureStateUserLocked(username)
	state.Status = statusTrial
	state.TrialStartUnix = now.Unix()
	state.TrialDays = days
	state.DaysRemaining = days
	state.LastQuotaWeek = ""
	return p.saveStateLocked()
}

func (p *Plugin) disableUser(username, reason string, bytesUp int64) error {
	if err := p.ensureUserFile(username); err != nil {
		return err
	}
	p.mu.Lock()
	state := p.ensureStateUserLocked(username)
	if err := p.applyDisabledSideEffectsLocked(username, state, reason, bytesUp); err != nil {
		p.mu.Unlock()
		return err
	}
	kicks := p.kickActionForLocked(state, reason)
	if err := p.saveStateLocked(); err != nil {
		p.mu.Unlock()
		return err
	}
	p.mu.Unlock()
	p.emitKicks(kicks)
	return nil
}

func (p *Plugin) applyDisabledSideEffectsLocked(username string, state *trackedUser, reason string, bytesUp int64) error {
	if err := p.setDisabledFlag(username, true); err != nil {
		return err
	}
	if err := p.writeByeFile(username, reason, bytesUp); err != nil && p.debug {
		log.Printf("[Quota] bye file for %s failed: %v", username, err)
	}
	state.Status = statusDisabled
	state.DaysRemaining = 0
	state.LastQuotaWeek = ""
	return nil
}

func (p *Plugin) kickActionForLocked(state *trackedUser, reason string) []disableAction {
	if !p.kickOnDisable || p.kicker == nil {
		return nil
	}
	nick := strings.TrimSpace(state.IRCNick)
	if nick == "" {
		return nil
	}
	return []disableAction{{Nick: nick, Reason: reason}}
}

func (p *Plugin) emitKicks(actions []disableAction) {
	if p.kicker == nil || !p.kickOnDisable {
		return
	}
	for _, action := range actions {
		for _, channel := range p.kickChannels {
			channel = strings.TrimSpace(channel)
			if channel == "" {
				continue
			}
			p.kicker(channel, action.Nick, action.Reason)
		}
	}
}

func (p *Plugin) setDisabledFlag(username string, disabled bool) error {
	path := filepath.Join(p.usersDir, username)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	wrote := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "FLAGS ") {
			continue
		}
		fields := strings.Fields(trimmed)
		flags := ""
		if len(fields) > 1 {
			flags = fields[1]
		}
		if disabled {
			flags = addFlags(flags, p.disabledFlag)
		} else {
			flags = removeFlags(flags, p.disabledFlag)
		}
		lines[i] = "FLAGS " + flags
		wrote = true
		break
	}
	if !wrote {
		flags := ""
		if disabled {
			flags = addFlags("", p.disabledFlag)
		}
		lines = append(lines, "FLAGS "+flags)
	}
	output := strings.Join(lines, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return os.WriteFile(path, []byte(output), 0644)
}

func (p *Plugin) writeByeFile(username, reason string, bytesUp int64) error {
	if strings.TrimSpace(p.byeDir) == "" {
		return nil
	}
	if err := os.MkdirAll(p.byeDir, 0755); err != nil {
		return err
	}
	content := fmt.Sprintf("Account Disabled.\nStats: %s Up\nReason: %s\n", formatBytes(bytesUp), strings.TrimSpace(reason))
	return os.WriteFile(filepath.Join(p.byeDir, username+".bye"), []byte(content), 0644)
}

func (p *Plugin) ensureUserFile(username string) error {
	path := filepath.Join(p.usersDir, username)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("user %s does not exist", username)
		}
		return err
	}
	return nil
}

func (p *Plugin) ensureStateUserLocked(username string) *trackedUser {
	if p.state.Users == nil {
		p.state.Users = map[string]*trackedUser{}
	}
	if state, ok := p.state.Users[username]; ok && state != nil {
		return state
	}
	state := &trackedUser{
		Status:         statusTrial,
		TrialStartUnix: time.Now().Unix(),
		TrialDays:      p.trialDaysDefault,
		DaysRemaining:  p.trialDaysDefault,
	}
	p.state.Users[username] = state
	return state
}

func (p *Plugin) loadState() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = persistedState{Users: map[string]*trackedUser{}}
	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := yaml.Unmarshal(data, &p.state); err != nil {
		return err
	}
	if p.state.Users == nil {
		p.state.Users = map[string]*trackedUser{}
	}
	return nil
}

func (p *Plugin) saveStateLocked() error {
	if err := os.MkdirAll(filepath.Dir(p.stateFile), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(&p.state)
	if err != nil {
		return err
	}
	return os.WriteFile(p.stateFile, data, 0644)
}

func (p *Plugin) startLoop() {
	p.mu.Lock()
	if p.stop != nil || p.scanInterval <= 0 {
		p.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	p.stop = stop
	p.stopped = stopped
	interval := p.scanInterval
	p.mu.Unlock()

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.scanAndProcess(); err != nil && p.debug {
					log.Printf("[Quota] scan loop failed: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()
}

func (p *Plugin) channelAllowed(evt *event.Event) bool {
	if len(p.channels) == 0 {
		return true
	}
	channel := strings.TrimSpace(evt.Data["channel"])
	for _, allowed := range p.channels {
		if strings.EqualFold(strings.TrimSpace(allowed), channel) {
			return true
		}
	}
	return false
}

func (p *Plugin) canStaff(evt *event.Event) bool {
	channel := strings.TrimSpace(evt.Data["channel"])
	for _, allowed := range p.staffChannels {
		if strings.EqualFold(strings.TrimSpace(allowed), channel) {
			return true
		}
	}
	for _, allowed := range p.staffUsers {
		if strings.EqualFold(strings.TrimSpace(allowed), evt.User) {
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

func (p *Plugin) replies(evt *event.Event, lines ...string) []plugin.Output {
	target := strings.TrimSpace(evt.Data["channel"])
	noticeReply := false
	switch {
	case strings.HasPrefix(p.replyTarget, "#"):
		target = p.replyTarget
	case p.replyTarget == "notice":
		target = evt.User
		noticeReply = true
	case p.replyTarget == "pm":
		target = evt.User
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

func (p *Plugin) staffUsage() string {
	return "!" + p.staffCommand + " <trial|quota|extend|delete> <username> [days]"
}

func (p *Plugin) staffExtendUsage() string {
	return "!" + p.staffCommand + " extend <username> <days>"
}

func parseUserSnapshot(path, username string) (userSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return userSnapshot{}, err
	}
	snap := userSnapshot{
		Username: username,
		Group:    "Unknown",
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "GROUP":
			if len(fields) >= 2 && snap.Group == "Unknown" {
				snap.Group = fields[1]
			}
		case "FLAGS":
			if len(fields) >= 2 {
				snap.Flags = fields[1]
			}
		case "RATIO":
			if len(fields) >= 2 {
				snap.Ratio, _ = strconv.Atoi(fields[1])
			}
		case "WKUP":
			if len(fields) >= 3 {
				snap.WkUpFiles, _ = strconv.ParseInt(fields[1], 10, 64)
				snap.WkUpBytes, _ = strconv.ParseInt(fields[2], 10, 64)
			}
		case "DAYUP":
			if len(fields) >= 3 {
				snap.DayFiles, _ = strconv.ParseInt(fields[1], 10, 64)
				snap.DayUpBytes, _ = strconv.ParseInt(fields[2], 10, 64)
			}
		}
	}
	return snap, nil
}

func addFlags(current, add string) string {
	for _, ch := range add {
		if !strings.ContainsRune(current, ch) {
			current = string(ch) + current
		}
	}
	return current
}

func removeFlags(current, remove string) string {
	filtered := strings.Builder{}
	for _, ch := range current {
		if !strings.ContainsRune(remove, ch) {
			filtered.WriteRune(ch)
		}
	}
	return filtered.String()
}

func (p *Plugin) hasDisabledFlag(flags string) bool {
	for _, ch := range p.disabledFlag {
		if ch != ' ' && strings.ContainsRune(flags, ch) {
			return true
		}
	}
	return false
}

func normalizeCommands(commands []string) []string {
	out := make([]string, 0, len(commands))
	for _, cmd := range commands {
		cmd = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(cmd, "!")))
		if cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

func matchesCommand(cmd string, commands []string) bool {
	for _, allowed := range commands {
		if strings.EqualFold(cmd, allowed) {
			return true
		}
	}
	return false
}

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func sortDisplayUsers(users []displayUser) {
	sort.Slice(users, func(i, j int) bool {
		if users[i].WkUpBytes != users[j].WkUpBytes {
			return users[i].WkUpBytes > users[j].WkUpBytes
		}
		if users[i].WkUpFiles != users[j].WkUpFiles {
			return users[i].WkUpFiles > users[j].WkUpFiles
		}
		return strings.ToLower(users[i].Username) < strings.ToLower(users[j].Username)
	})
}

func trialDaysRemaining(startUnix int64, totalDays int, now time.Time) int {
	if startUnix <= 0 || totalDays <= 0 {
		return 0
	}
	elapsed := int((now.Unix() - startUnix) / 86400)
	remaining := totalDays - elapsed
	if remaining < 0 {
		return 0
	}
	return remaining
}

func daysUntilSunday(now time.Time) int {
	return (7 + int(time.Sunday) - int(now.Weekday())) % 7
}

func isoWeekKey(now time.Time) string {
	year, week := now.ISOWeek()
	return fmt.Sprintf("%04d-%02d", year, week)
}

func passLabel(passed bool) string {
	if passed {
		return "PASSING"
	}
	return "FAILING"
}

func passWord(passed bool) string {
	if passed {
		return "PASSING"
	}
	return "FAILING"
}

func formatBytes(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	const (
		kb = 1024
		mb = 1024 * 1024
		gb = 1024 * 1024 * 1024
		tb = 1024 * 1024 * 1024 * 1024
	)
	value := float64(bytes)
	switch {
	case bytes >= tb:
		return fmt.Sprintf("%.2f TB", value/float64(tb))
	case bytes >= gb:
		return fmt.Sprintf("%.2f GB", value/float64(gb))
	case bytes >= mb:
		return fmt.Sprintf("%.2f MB", value/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.2f KB", value/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func humanGB(bytes int64) string {
	if bytes <= 0 {
		return "0 GB"
	}
	return fmt.Sprintf("%.0fGB", float64(bytes)/float64(1024*1024*1024))
}

func gibToBytes(gb float64) int64 {
	return int64(gb * 1024 * 1024 * 1024)
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

func floatConfig(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
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
