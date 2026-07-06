package request

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"weaveftpd/internal/plugin"
	"weaveftpd/internal/timeutil"
)

type Plugin struct {
	svc                       *plugin.Services
	dir                       string
	requestHead               string
	filledHead                string
	allowSpace                bool
	replaceWith               string
	badChars                  string
	maxRequests               int
	showOnRequest             bool
	showOnFill                bool
	doNotCreateDirUntilFilled bool
	reqwipeFlags              string
	reqTopLimit               int
	proxyUsers                []string
	storageSlave              string
	stateFile                 string
	sitename                  string
	debug                     bool
	stopCh                    chan struct{}
}

type requestEntry struct {
	Num     int
	Release string
	By      string
	Mode    string
	For     string
	Date    string
}

type requestFillEntry struct {
	Release     string
	RequestedBy string
	FilledBy    string
	Date        string
}

type requestStateFile struct {
	Version  int                        `json:"version"`
	Dir      string                     `json:"dir"`
	Requests []plugin.RequestRecord     `json:"requests"`
	Fills    []plugin.RequestFillRecord `json:"fills"`
}

const defaultRequestStateFile = "userdata/request_state.json"

func New() *Plugin {
	return &Plugin{
		dir:          "/REQUESTS",
		requestHead:  "REQ-",
		filledHead:   "FILLED-",
		replaceWith:  "_",
		badChars:     `*{^~/,+&\`,
		maxRequests:  30,
		reqwipeFlags: "1",
		reqTopLimit:  10,
		proxyUsers:   []string{"weaveftpd"},
		sitename:     "WeaveFTPd",
		stopCh:       make(chan struct{}),
	}
}

func (p *Plugin) Name() string { return "request" }

func (p *Plugin) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	p.svc = svc
	if s := stringConfig(cfg, "dir", ""); strings.TrimSpace(s) != "" {
		p.dir = cleanAbs(s)
	}
	if s := stringConfig(cfg, "request_head", ""); s != "" {
		p.requestHead = s
	}
	if s := stringConfig(cfg, "filled_head", ""); s != "" {
		p.filledHead = s
	}
	if s := stringConfig(cfg, "replace_with", ""); s != "" {
		p.replaceWith = s
	}
	if s := stringConfig(cfg, "bad_chars", ""); s != "" {
		p.badChars = s
	}
	if s := stringConfig(cfg, "reqwipe_flags", ""); strings.TrimSpace(s) != "" {
		p.reqwipeFlags = strings.TrimSpace(s)
	}
	if raw, ok := cfg["proxy_users"]; ok {
		p.proxyUsers = toStringSlice(raw)
	}
	if s := stringConfig(cfg, "storage_slave", ""); strings.TrimSpace(s) != "" {
		p.storageSlave = strings.TrimSpace(s)
	}
	if s := stringConfig(cfg, "state_file", ""); strings.TrimSpace(s) != "" {
		p.stateFile = strings.TrimSpace(s)
	} else {
		p.stateFile = defaultRequestStateFile
	}
	if s := stringConfig(cfg, "sitename", ""); strings.TrimSpace(s) != "" {
		p.sitename = strings.TrimSpace(s)
	}
	if n := intConfig(cfg["max_requests"], 0); n >= 0 {
		p.maxRequests = n
	}
	if n := intConfig(cfg["reqtop_limit"], 0); n > 0 {
		p.reqTopLimit = n
	}
	if b, ok := boolConfig(cfg["allow_space"]); ok {
		p.allowSpace = b
	}
	if b, ok := boolConfig(cfg["show_on_request"]); ok {
		p.showOnRequest = b
	}
	if b, ok := boolConfig(cfg["show_on_fill"]); ok {
		p.showOnFill = b
	}
	if b, ok := boolConfig(cfg["do_not_create_dir_until_filled"]); ok {
		p.doNotCreateDirUntilFilled = b
	}
	if b, ok := boolConfig(cfg["debug"]); ok {
		p.debug = b
	}
	if err := p.hydrateRequestState(); err != nil {
		log.Printf("[REQUEST] could not load request state from %s: %v", p.stateFile, err)
	}
	go p.ensureBaseDirOnStartup()
	return nil
}

func (p *Plugin) OnEvent(evt *plugin.Event) error { return nil }

func (p *Plugin) Stop() error {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
	return nil
}

func (p *Plugin) SiteCommands() []string {
	return []string{"REQUEST", "REQUESTS", "REQFILL", "REQFILLED", "REQTOP", "REQDEL", "REQWIPE"}
}

func (p *Plugin) HandleSiteCommand(ctx plugin.SiteContext, command string, args []string) bool {
	if p.svc == nil || p.svc.Bridge == nil {
		ctx.Reply("451 Master bridge unavailable.\r\n")
		return true
	}
	switch strings.ToUpper(strings.TrimSpace(command)) {
	case "REQUEST":
		return p.handleRequest(ctx, args)
	case "REQUESTS":
		return p.handleStatus(ctx)
	case "REQFILL", "REQFILLED":
		return p.handleReqFill(ctx, args)
	case "REQTOP":
		return p.handleReqTop(ctx, args)
	case "REQDEL":
		return p.handleReqDel(ctx, args)
	case "REQWIPE":
		return p.handleReqWipe(ctx, args)
	}
	return false
}

func (p *Plugin) handleRequest(ctx plugin.SiteContext, args []string) bool {
	release, forUser, byUser, errMsg := p.parseRequestArgs(args)
	if errMsg != "" {
		p.reply(ctx, "501", errMsg)
		return true
	}
	if release == "" {
		p.reply(ctx, "501", "Usage: SITE REQUEST <request> [-for:<user>] [-hide]")
		return true
	}
	if byUser != "" && !p.canProxyUser(ctx) {
		p.reply(ctx, "550", "Permission denied: -by is only available to configured request proxy users.")
		return true
	}
	requestedBy := firstNonEmpty(byUser, ctx.UserName())

	entries := p.loadRequests()
	if idx := p.findRequest(entries, release); idx >= 0 {
		if p.requestHead != "" && !p.doNotCreateDirUntilFilled {
			if err := p.ensureRequestDir(ctx, release, entries[idx].By); err != nil {
				p.reply(ctx, "451", fmt.Sprintf("Failed to repair request dir: %v", err))
				return true
			}
		}
		if err := p.saveRequests(entries); err != nil {
			p.reply(ctx, "451", fmt.Sprintf("Failed to save request list: %v", err))
			return true
		}
		p.reply(ctx, "200", fmt.Sprintf("%s has already been requested.", release))
		return true
	}
	if p.dirExists(p.requestDir(release)) || p.dirExists(p.filledDir(release)) {
		p.reply(ctx, "200", fmt.Sprintf("%s has already been requested.", release))
		return true
	}
	if p.maxRequests > 0 && len(entries) >= p.maxRequests {
		p.reply(ctx, "200", "No more requests allowed. Fill some up.")
		return true
	}

	if p.requestHead != "" && !p.doNotCreateDirUntilFilled {
		if err := p.ensureRequestDir(ctx, release, requestedBy); err != nil {
			p.reply(ctx, "451", fmt.Sprintf("Failed to create request dir: %v", err))
			return true
		}
	} else if err := p.ensureBaseDir(ctx); err != nil {
		p.reply(ctx, "451", fmt.Sprintf("Failed to create request base dir: %v", err))
		return true
	}

	entries = append(entries, requestEntry{
		Release: release,
		By:      requestedBy,
		Mode:    "gl",
		For:     forUser,
		Date:    timeutil.Now().Format("2006-01-02 15:04"),
	})
	if err := p.saveRequests(entries); err != nil {
		p.reply(ctx, "451", fmt.Sprintf("Failed to save request list: %v", err))
		return true
	}
	added := p.numberedEntry(entries[len(entries)-1])
	if byUser == "" {
		p.emitRequestCreated(entries[len(entries)-1], added)
	}
	if p.showOnRequest {
		return p.replyStatus(ctx)
	}
	p.reply(ctx, "200", fmt.Sprintf("Ok, %s %s added to requests.", added, release))
	return true
}

func (p *Plugin) handleStatus(ctx plugin.SiteContext) bool {
	return p.replyStatus(ctx)
}

func (p *Plugin) handleReqFill(ctx plugin.SiteContext, args []string) bool {
	key, byUser := p.parseKeyArgs(args)
	if key == "" {
		p.reply(ctx, "501", "Usage: SITE REQFILL <number|request> [-by:<user>]")
		return true
	}
	if byUser != "" && !p.canProxyUser(ctx) {
		p.reply(ctx, "550", "Permission denied: -by is only available to configured request proxy users.")
		return true
	}
	filledBy := firstNonEmpty(byUser, ctx.UserName())
	entries := p.loadRequests()
	idx := p.findRequest(entries, key)
	if idx < 0 {
		p.reply(ctx, "200", fmt.Sprintf("The request %s does not exist.", key))
		return true
	}
	entry := entries[idx]
	reqDir := p.requestDir(entry.Release)
	if p.requestHead != "" && !p.doNotCreateDirUntilFilled {
		if err := p.ensureRequestDir(ctx, entry.Release, entry.By); err != nil {
			p.reply(ctx, "451", fmt.Sprintf("Failed to repair request dir: %v", err))
			return true
		}
	}
	if targetSlave := p.dirSlave(reqDir); targetSlave != "" {
		// Older request trees may already be mixed across slaves. Normalize them
		// onto the wrapper dir's owner before the fill rename so normal RETR keeps
		// working after the request becomes FILLED-*.
		if err := p.relocateRequestTreeToSlave(reqDir, targetSlave); err != nil {
			p.reply(ctx, "451", fmt.Sprintf("Failed to prepare request dir for fill: %v", err))
			return true
		}
	}
	if p.requestHead != "" && p.dirExists(reqDir) {
		if p.dirEmpty(reqDir) {
			p.reply(ctx, "200", "That request is empty. Can not reqfill it.")
			return true
		}
		toDir, toName := p.availableFilledTarget(entry.Release)
		if err := p.svc.Bridge.RenameFile(reqDir, toDir, toName); err != nil {
			p.reply(ctx, "451", fmt.Sprintf("Failed to rename request dir: %v", err))
			return true
		}
	} else if p.doNotCreateDirUntilFilled {
		if err := p.makeDir(p.filledDir(entry.Release), ctx.UserName(), ctx.UserPrimaryGroup()); err != nil && !p.dirExists(p.filledDir(entry.Release)) {
			p.reply(ctx, "451", fmt.Sprintf("Failed to create filled request dir: %v", err))
			return true
		}
		_ = p.svc.Bridge.Chmod(p.filledDir(entry.Release), 0777)
	}
	entries = removeEntry(entries, idx)
	if err := p.saveRequests(entries); err != nil {
		p.reply(ctx, "451", fmt.Sprintf("Failed to save request list: %v", err))
		return true
	}
	if err := p.recordFill(entry, filledBy); err != nil {
		if byUser == "" {
			p.emitRequestFilled(entry, filledBy, key)
		}
		p.reply(ctx, "200", fmt.Sprintf("%s : %s has been filled. Thank you. Fill stats were not saved: %v", key, entry.Release, err))
		return true
	}
	if byUser == "" {
		p.emitRequestFilled(entry, filledBy, key)
	}
	if p.showOnFill {
		return p.replyStatus(ctx)
	}
	p.reply(ctx, "200", fmt.Sprintf("%s : %s has been filled. Thank you.", key, entry.Release))
	return true
}

func (p *Plugin) handleReqTop(ctx plugin.SiteContext, args []string) bool {
	limit := p.reqTopLimit
	if len(args) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(args[0])); err == nil && n > 0 {
			limit = n
		}
	}
	if limit <= 0 {
		limit = 10
	}
	stats := p.loadFillStats()
	if len(stats) == 0 {
		p.reply(ctx, "200", "No filled request stats yet.")
		return true
	}
	type fillCount struct {
		User  string
		Count int
	}
	counts := map[string]*fillCount{}
	for _, stat := range stats {
		user := strings.TrimSpace(stat.FilledBy)
		if user == "" {
			continue
		}
		key := strings.ToLower(user)
		if counts[key] == nil {
			counts[key] = &fillCount{User: user}
		}
		counts[key].Count++
	}
	rows := make([]fillCount, 0, len(counts))
	for _, count := range counts {
		rows = append(rows, *count)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return strings.ToLower(rows[i].User) < strings.ToLower(rows[j].User)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	lines := []string{"Request Fill Leaderboard:"}
	for i, row := range rows {
		lines = append(lines, fmt.Sprintf("[%02d] %s - %d fill(s)", i+1, row.User, row.Count))
	}
	p.replyMultiline(ctx, "200", lines)
	return true
}

func (p *Plugin) handleReqDel(ctx plugin.SiteContext, args []string) bool {
	key, byUser := p.parseKeyArgs(args)
	if key == "" {
		p.reply(ctx, "501", "Usage: SITE REQDEL <number|request>")
		return true
	}
	if byUser != "" && !p.canProxyUser(ctx) {
		p.reply(ctx, "550", "Permission denied: -by is only available to configured request proxy users.")
		return true
	}
	entries := p.loadRequests()
	idx := p.findRequest(entries, key)
	if idx < 0 {
		p.reply(ctx, "200", fmt.Sprintf("The request %s does not exist.", key))
		return true
	}
	entry := entries[idx]
	if byUser != "" {
		if !strings.EqualFold(entry.By, byUser) {
			p.reply(ctx, "550", fmt.Sprintf("Permission denied: request belongs to %s.", entry.By))
			return true
		}
	} else if !strings.EqualFold(entry.By, ctx.UserName()) && !p.canReqWipe(ctx) {
		p.reply(ctx, "550", fmt.Sprintf("Permission denied: request belongs to %s.", entry.By))
		return true
	}
	if p.requestHead != "" && p.dirExists(p.requestDir(entry.Release)) {
		_ = p.svc.Bridge.DeleteFile(p.requestDir(entry.Release))
	}
	entries = removeEntry(entries, idx)
	if err := p.saveRequests(entries); err != nil {
		p.reply(ctx, "451", fmt.Sprintf("Failed to save request list: %v", err))
		return true
	}
	p.reply(ctx, "200", fmt.Sprintf("%s : %s has been deleted.", key, entry.Release))
	return true
}

func (p *Plugin) handleReqWipe(ctx plugin.SiteContext, args []string) bool {
	if !p.canReqWipe(ctx) {
		p.reply(ctx, "550", "Permission denied.")
		return true
	}
	key := strings.TrimSpace(strings.Join(args, " "))
	if key == "" {
		p.reply(ctx, "501", "Usage: SITE REQWIPE <number|request>")
		return true
	}
	entries := p.loadRequests()
	idx := p.findRequest(entries, key)
	if idx < 0 {
		p.reply(ctx, "200", fmt.Sprintf("The request %s does not exist.", key))
		return true
	}
	entry := entries[idx]
	wiped := p.requestDir(entry.Release)
	if p.requestHead != "" && p.dirExists(wiped) {
		_ = p.svc.Bridge.DeleteFile(wiped)
	}
	entries = removeEntry(entries, idx)
	if err := p.saveRequests(entries); err != nil {
		p.reply(ctx, "451", fmt.Sprintf("Failed to save request list: %v", err))
		return true
	}
	p.reply(ctx, "200", fmt.Sprintf("Wiped out %s", wiped))
	return true
}

func (p *Plugin) replyStatus(ctx plugin.SiteContext) bool {
	entries := p.loadRequests()
	if len(entries) == 0 {
		p.reply(ctx, "200", "There are currently no more requests.")
		return true
	}
	lines := []string{fmt.Sprintf("Current Requests on %s:", p.sitename)}
	for _, entry := range entries {
		who := fmt.Sprintf("created by %s at %s", entry.By, entry.Date)
		if entry.For != "" {
			who = fmt.Sprintf("created by %s for %s at %s", entry.By, entry.For, entry.Date)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", p.numberedEntry(entry), entry.Release, who))
	}
	lines = append(lines, "If you fill a request, please do site reqfilled <number or name>")
	p.replyMultiline(ctx, "200", lines)
	return true
}

func (p *Plugin) parseRequestArgs(args []string) (string, string, string, string) {
	var releaseParts []string
	forUser := ""
	byUser := ""
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		lower := strings.ToLower(arg)
		if lower == "-hide" {
			continue
		}
		if strings.HasPrefix(lower, "-for:") {
			forUser = strings.TrimSpace(arg[5:])
			continue
		}
		if strings.HasPrefix(lower, "-by:") {
			byUser = strings.TrimSpace(arg[4:])
			continue
		}
		releaseParts = append(releaseParts, arg)
	}
	release := strings.TrimSpace(strings.Join(releaseParts, " "))
	if release == "" {
		return "", forUser, byUser, ""
	}
	if strings.Contains(release, " ") && !p.allowSpace {
		if p.replaceWith == "" {
			return "", forUser, byUser, "Spaces are not allowed!"
		}
		release = strings.Join(strings.Fields(release), p.replaceWith)
	}
	if strings.ContainsAny(release, p.badChars) || strings.Contains(release, "/") || strings.Contains(release, "\\") {
		return "", forUser, byUser, "Only use alphabetical characters please."
	}
	return release, forUser, byUser, ""
}

func (p *Plugin) parseKeyArgs(args []string) (string, string) {
	var keyParts []string
	byUser := ""
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(arg), "-by:") {
			byUser = strings.TrimSpace(arg[4:])
			continue
		}
		keyParts = append(keyParts, arg)
	}
	return strings.TrimSpace(strings.Join(keyParts, " ")), byUser
}

func (p *Plugin) loadRequests() []requestEntry {
	requests, _ := p.svc.Bridge.GetRequestData(p.dir)
	entries := make([]requestEntry, 0, len(requests))
	for _, request := range requests {
		entries = append(entries, requestEntry{
			Release: strings.TrimSpace(request.Release),
			By:      firstNonEmpty(request.By, "unknown"),
			Mode:    firstNonEmpty(request.Mode, "gl"),
			For:     strings.TrimSpace(request.For),
			Date:    strings.TrimSpace(request.Date),
		})
	}
	entries = p.mergeRequestDirs(entries)
	for i := range entries {
		entries[i].Num = i + 1
	}
	return entries
}

func (p *Plugin) mergeRequestDirs(entries []requestEntry) []requestEntry {
	if p.requestHead == "" {
		return entries
	}
	known := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		known[strings.ToLower(entry.Release)] = struct{}{}
	}
	for _, entry := range p.svc.Bridge.PluginListDir(p.dir) {
		if !entry.IsDir || !strings.HasPrefix(entry.Name, p.requestHead) {
			continue
		}
		release := strings.TrimPrefix(entry.Name, p.requestHead)
		if strings.TrimSpace(release) == "" {
			continue
		}
		if _, ok := known[strings.ToLower(release)]; ok {
			continue
		}
		date := timeutil.Now().Format("2006-01-02 15:04")
		if entry.ModTime > 0 {
			date = time.Unix(entry.ModTime, 0).Format("2006-01-02 15:04")
		}
		entries = append(entries, requestEntry{
			Release: release,
			By:      firstNonEmpty(entry.Owner, "unknown"),
			Mode:    "gl",
			For:     "",
			Date:    date,
		})
		known[strings.ToLower(release)] = struct{}{}
	}
	return entries
}

func (p *Plugin) recordFill(entry requestEntry, filledBy string) error {
	fills := p.loadFillStats()
	fills = append(fills, requestFillEntry{
		Release:     entry.Release,
		RequestedBy: entry.By,
		FilledBy:    filledBy,
		Date:        timeutil.Now().Format("2006-01-02 15:04"),
	})
	return p.saveFillStats(fills)
}

func (p *Plugin) loadFillStats() []requestFillEntry {
	_, fills := p.svc.Bridge.GetRequestData(p.dir)
	out := make([]requestFillEntry, 0, len(fills))
	for _, fill := range fills {
		out = append(out, requestFillEntry{
			Release:     strings.TrimSpace(fill.Release),
			RequestedBy: firstNonEmpty(fill.RequestedBy, "unknown"),
			FilledBy:    firstNonEmpty(fill.FilledBy, "unknown"),
			Date:        strings.TrimSpace(fill.Date),
		})
	}
	return out
}

func (p *Plugin) saveFillStats(entries []requestFillEntry) error {
	requests, _ := p.svc.Bridge.GetRequestData(p.dir)
	fills := make([]plugin.RequestFillRecord, 0, len(entries))
	for _, entry := range entries {
		fills = append(fills, plugin.RequestFillRecord{
			Release:     strings.TrimSpace(entry.Release),
			RequestedBy: firstNonEmpty(entry.RequestedBy, "unknown"),
			FilledBy:    firstNonEmpty(entry.FilledBy, "unknown"),
			Date:        strings.TrimSpace(entry.Date),
		})
	}
	return p.setRequestData(requests, fills)
}

func (p *Plugin) ensureRequestDir(ctx plugin.SiteContext, release, ownerOverride string) error {
	if err := p.ensureBaseDir(ctx); err != nil {
		return err
	}
	dirPath := p.requestDir(release)
	owner := "WeaveFTPd"
	group := "WeaveFTPd"
	if ctx != nil {
		owner = ctx.UserName()
		group = ctx.UserPrimaryGroup()
	}
	if strings.TrimSpace(ownerOverride) != "" {
		owner = strings.TrimSpace(ownerOverride)
	}
	if err := p.makeDir(dirPath, owner, group); err != nil {
		return err
	}
	_ = p.svc.Bridge.Chmod(dirPath, 0777)
	return nil
}

func (p *Plugin) saveRequests(entries []requestEntry) error {
	for i := range entries {
		entries[i].Num = i + 1
		if entries[i].Mode == "" {
			entries[i].Mode = "gl"
		}
	}
	requests := make([]plugin.RequestRecord, 0, len(entries))
	for _, entry := range entries {
		requests = append(requests, plugin.RequestRecord{
			Release: strings.TrimSpace(entry.Release),
			By:      firstNonEmpty(entry.By, "unknown"),
			Mode:    firstNonEmpty(entry.Mode, "gl"),
			For:     strings.TrimSpace(entry.For),
			Date:    strings.TrimSpace(entry.Date),
		})
	}
	_, fills := p.svc.Bridge.GetRequestData(p.dir)
	return p.setRequestData(requests, fills)
}

func (p *Plugin) hydrateRequestState() error {
	if p == nil || p.svc == nil || p.svc.Bridge == nil || strings.TrimSpace(p.stateFile) == "" {
		return nil
	}

	fileRequests, fileFills, found, err := p.loadRequestStateFile()
	if err != nil {
		return err
	}
	vfsRequests, vfsFills := p.svc.Bridge.GetRequestData(p.dir)
	requests := mergeRequestRecords(fileRequests, vfsRequests)
	fills := mergeRequestFillRecords(fileFills, vfsFills)
	if !found && len(requests) == 0 && len(fills) == 0 {
		return nil
	}
	p.svc.Bridge.SetRequestData(p.dir, requests, fills)
	return p.saveRequestStateFile(requests, fills)
}

func (p *Plugin) setRequestData(requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) error {
	p.svc.Bridge.SetRequestData(p.dir, requests, fills)
	return p.saveRequestStateFile(requests, fills)
}

func (p *Plugin) loadRequestStateFile() ([]plugin.RequestRecord, []plugin.RequestFillRecord, bool, error) {
	if strings.TrimSpace(p.stateFile) == "" {
		return nil, nil, false, nil
	}
	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, false, nil
		}
		return nil, nil, false, err
	}
	var state requestStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil, true, err
	}
	if state.Dir != "" && cleanAbs(state.Dir) != cleanAbs(p.dir) {
		return nil, nil, true, fmt.Errorf("state file belongs to %s, plugin dir is %s", state.Dir, p.dir)
	}
	return copyRequestRecords(state.Requests), copyRequestFillRecords(state.Fills), true, nil
}

func (p *Plugin) saveRequestStateFile(requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) error {
	if strings.TrimSpace(p.stateFile) == "" {
		return nil
	}
	state := requestStateFile{
		Version:  1,
		Dir:      cleanAbs(p.dir),
		Requests: copyRequestRecords(requests),
		Fills:    copyRequestFillRecords(fills),
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(p.stateFile)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p.stateFile)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, p.stateFile); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func copyRequestRecords(in []plugin.RequestRecord) []plugin.RequestRecord {
	if len(in) == 0 {
		return nil
	}
	return append([]plugin.RequestRecord(nil), in...)
}

func copyRequestFillRecords(in []plugin.RequestFillRecord) []plugin.RequestFillRecord {
	if len(in) == 0 {
		return nil
	}
	return append([]plugin.RequestFillRecord(nil), in...)
}

func mergeRequestRecords(primary []plugin.RequestRecord, secondary []plugin.RequestRecord) []plugin.RequestRecord {
	out := make([]plugin.RequestRecord, 0, len(primary)+len(secondary))
	seen := make(map[string]int, len(primary)+len(secondary))
	add := func(record plugin.RequestRecord, replace bool) {
		record.Release = strings.TrimSpace(record.Release)
		if record.Release == "" {
			return
		}
		record.By = firstNonEmpty(record.By, "unknown")
		record.Mode = firstNonEmpty(record.Mode, "gl")
		record.For = strings.TrimSpace(record.For)
		record.Date = strings.TrimSpace(record.Date)
		key := strings.ToLower(record.Release)
		if idx, ok := seen[key]; ok {
			if replace {
				out[idx] = record
			}
			return
		}
		seen[key] = len(out)
		out = append(out, record)
	}
	for _, record := range primary {
		add(record, false)
	}
	for _, record := range secondary {
		add(record, true)
	}
	return out
}

func mergeRequestFillRecords(primary []plugin.RequestFillRecord, secondary []plugin.RequestFillRecord) []plugin.RequestFillRecord {
	out := make([]plugin.RequestFillRecord, 0, len(primary)+len(secondary))
	seen := make(map[string]int, len(primary)+len(secondary))
	add := func(record plugin.RequestFillRecord) {
		record.Release = strings.TrimSpace(record.Release)
		record.RequestedBy = firstNonEmpty(record.RequestedBy, "unknown")
		record.FilledBy = firstNonEmpty(record.FilledBy, "unknown")
		record.Date = strings.TrimSpace(record.Date)
		if record.Release == "" || record.FilledBy == "" {
			return
		}
		key := strings.ToLower(record.Release) + "\x00" + strings.ToLower(record.FilledBy)
		if idx, ok := seen[key]; ok {
			existing := out[idx]
			if isWeakRequestUser(existing.RequestedBy) && !isWeakRequestUser(record.RequestedBy) {
				existing.RequestedBy = record.RequestedBy
			}
			if existing.Date == "" && record.Date != "" {
				existing.Date = record.Date
			}
			out[idx] = existing
			return
		}
		seen[key] = len(out)
		out = append(out, record)
	}
	for _, record := range primary {
		add(record)
	}
	for _, record := range secondary {
		add(record)
	}
	return out
}

func isWeakRequestUser(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "unknown", "restore":
		return true
	default:
		return false
	}
}

func (p *Plugin) emitRequestCreated(entry requestEntry, numbered string) {
	if p == nil || p.svc == nil || p.svc.EmitEvent == nil {
		return
	}
	release := strings.TrimSpace(entry.Release)
	if release == "" {
		return
	}
	requester := strings.TrimSpace(entry.By)
	if requester == "" {
		requester = "unknown"
	}
	message := fmt.Sprintf("REQUEST: %s %s created by %s", strings.TrimSpace(numbered), release, requester)
	data := map[string]string{
		"template":      "REQUESTADD",
		"announce_type": "REQUESTADD",
		"request":       release,
		"relname":       release,
		"requester":     requester,
		"requested_by":  requester,
		"number":        strconv.Itoa(entry.Num),
		"numbered":      strings.TrimSpace(numbered),
		"date":          entry.Date,
		"for_user":      entry.For,
		"message":       message,
	}
	p.svc.EmitEvent("CUSTOM", p.requestDir(release), release, "REQUESTS", 0, 0, data)
}

func (p *Plugin) emitRequestFilled(entry requestEntry, filledBy, key string) {
	if p == nil || p.svc == nil || p.svc.EmitEvent == nil {
		return
	}
	release := strings.TrimSpace(entry.Release)
	if release == "" {
		return
	}
	filler := strings.TrimSpace(filledBy)
	if filler == "" {
		filler = "unknown"
	}
	requester := strings.TrimSpace(entry.By)
	if requester == "" {
		requester = "unknown"
	}
	numbered := strings.TrimSpace(p.numberedEntry(entry))
	message := fmt.Sprintf("REQUEST: %s %s filled by %s", numbered, release, filler)
	data := map[string]string{
		"template":      "REQUESTFILL",
		"announce_type": "REQUESTFILL",
		"request":       release,
		"relname":       release,
		"requester":     requester,
		"requested_by":  requester,
		"filled_by":     filler,
		"filler":        filler,
		"number":        strconv.Itoa(entry.Num),
		"numbered":      numbered,
		"date":          entry.Date,
		"for_user":      entry.For,
		"fill_key":      strings.TrimSpace(key),
		"message":       message,
	}
	p.svc.EmitEvent("CUSTOM", p.filledDir(release), release, "REQUESTS", 0, 0, data)
}

func (p *Plugin) ensureBaseDir(ctx plugin.SiteContext) error {
	owner := "WeaveFTPd"
	group := "WeaveFTPd"
	if ctx != nil {
		owner = ctx.UserName()
		group = ctx.UserPrimaryGroup()
	}
	exists := p.dirExists(p.dir)
	if err := p.makeDir(p.dir, owner, group); err != nil && !exists {
		return err
	}
	_ = p.svc.Bridge.Chmod(p.dir, 0777)
	return nil
}

type requestStorageBridge interface {
	MakeDirOnSlave(dirPath, owner, group, slaveName string) error
}

func (p *Plugin) makeDir(dirPath, owner, group string) error {
	if p.storageSlave != "" {
		if bridge, ok := p.svc.Bridge.(requestStorageBridge); ok {
			return bridge.MakeDirOnSlave(dirPath, owner, group, p.storageSlave)
		}
	}
	return p.svc.Bridge.MakeDir(dirPath, owner, group)
}

func (p *Plugin) ensureBaseDirOnStartup() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for attempt := 0; attempt < 60; attempt++ {
		if p.svc != nil && p.svc.Bridge != nil {
			err := p.ensureBaseDir(nil)
			if p.dirExists(p.dir) {
				if p.debug {
					log.Printf("[REQUEST] ensured request dir %s", p.dir)
				}
				return
			}
			if p.debug {
				log.Printf("[REQUEST] request dir %s not ready yet: %v", p.dir, err)
			}
		}
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (p *Plugin) findRequest(entries []requestEntry, key string) int {
	key = strings.TrimSpace(key)
	if num, err := strconv.Atoi(key); err == nil {
		for i, entry := range entries {
			if entry.Num == num {
				return i
			}
		}
		return -1
	}
	for i, entry := range entries {
		if strings.EqualFold(entry.Release, key) {
			return i
		}
	}
	return -1
}

func (p *Plugin) requestDir(release string) string {
	return path.Join(p.dir, p.requestHead+release)
}

func (p *Plugin) filledDir(release string) string {
	return path.Join(p.dir, p.filledHead+release)
}

func (p *Plugin) availableFilledTarget(release string) (string, string) {
	parent := cleanAbs(p.dir)
	base := p.filledHead + release
	if !p.childExists(parent, base) {
		return parent, base
	}
	for i := 1; i < 1000; i++ {
		name := fmt.Sprintf("%s.%d", base, i)
		if !p.childExists(parent, name) {
			return parent, name
		}
	}
	return parent, fmt.Sprintf("%s.%d", base, timeutil.Now().Unix())
}

func (p *Plugin) dirExists(dirPath string) bool {
	dirPath = cleanAbs(dirPath)
	if dirPath == "/" {
		return true
	}
	parent := path.Dir(dirPath)
	name := path.Base(dirPath)
	for _, entry := range p.svc.Bridge.PluginListDir(parent) {
		if strings.EqualFold(entry.Name, name) && entry.IsDir {
			return true
		}
	}
	return false
}

func (p *Plugin) childExists(parent, name string) bool {
	for _, entry := range p.svc.Bridge.PluginListDir(cleanAbs(parent)) {
		if strings.EqualFold(entry.Name, name) {
			return true
		}
	}
	return false
}

type requestTreeEntry struct {
	Path  string
	Slave string
	IsDir bool
}

func (p *Plugin) dirSlave(dirPath string) string {
	dirPath = cleanAbs(dirPath)
	parent := path.Dir(dirPath)
	name := path.Base(dirPath)
	for _, entry := range p.svc.Bridge.PluginListDir(parent) {
		if entry.IsDir && strings.EqualFold(entry.Name, name) {
			return strings.TrimSpace(entry.Slave)
		}
	}
	return ""
}

func (p *Plugin) relocateRequestTreeToSlave(dirPath, targetSlave string) error {
	targetSlave = strings.TrimSpace(targetSlave)
	if p == nil || p.svc == nil || p.svc.Bridge == nil || targetSlave == "" {
		return nil
	}
	entries := p.collectRequestTreeEntries(dirPath)
	sort.Slice(entries, func(i, j int) bool {
		leftDepth := strings.Count(entries[i].Path, "/")
		rightDepth := strings.Count(entries[j].Path, "/")
		if entries[i].IsDir != entries[j].IsDir {
			return !entries[i].IsDir
		}
		return leftDepth > rightDepth
	})
	for _, entry := range entries {
		if entry.Path == cleanAbs(dirPath) || strings.TrimSpace(entry.Slave) == "" || strings.EqualFold(entry.Slave, targetSlave) {
			continue
		}
		if err := p.svc.Bridge.RelocatePathToSlave(entry.Path, path.Dir(entry.Path), path.Base(entry.Path), targetSlave); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) collectRequestTreeEntries(dirPath string) []requestTreeEntry {
	var out []requestTreeEntry
	var walk func(string)
	walk = func(current string) {
		current = cleanAbs(current)
		for _, entry := range p.svc.Bridge.PluginListDir(current) {
			childPath := cleanAbs(path.Join(current, entry.Name))
			out = append(out, requestTreeEntry{
				Path:  childPath,
				Slave: strings.TrimSpace(entry.Slave),
				IsDir: entry.IsDir,
			})
			if entry.IsDir {
				walk(childPath)
			}
		}
	}
	walk(dirPath)
	return out
}

func (p *Plugin) dirEmpty(dirPath string) bool {
	for _, entry := range p.svc.Bridge.PluginListDir(cleanAbs(dirPath)) {
		if entry.Name != "." && entry.Name != ".." {
			return false
		}
	}
	return true
}

func (p *Plugin) numberedEntry(entry requestEntry) string {
	return fmt.Sprintf("[%2d:]", entry.Num)
}

func (p *Plugin) canReqWipe(ctx plugin.SiteContext) bool {
	if strings.TrimSpace(p.reqwipeFlags) == "" {
		return true
	}
	flags := ctx.UserFlags()
	for _, required := range p.reqwipeFlags {
		if required > 32 && strings.ContainsRune(flags, required) {
			return true
		}
	}
	return false
}

func (p *Plugin) canProxyUser(ctx plugin.SiteContext) bool {
	if len(p.proxyUsers) == 0 || ctx == nil {
		return false
	}
	for _, user := range p.proxyUsers {
		if strings.EqualFold(strings.TrimSpace(user), ctx.UserName()) {
			return true
		}
	}
	return false
}

func (p *Plugin) reply(ctx plugin.SiteContext, code, line string) {
	ctx.Reply("%s %s\r\n", code, line)
}

func (p *Plugin) replyMultiline(ctx plugin.SiteContext, code string, lines []string) {
	for i, line := range lines {
		sep := "-"
		if i == len(lines)-1 {
			sep = " "
		}
		ctx.Reply("%s%s %s\r\n", code, sep, line)
	}
}

func removeEntry(entries []requestEntry, idx int) []requestEntry {
	if idx < 0 || idx >= len(entries) {
		return entries
	}
	out := make([]requestEntry, 0, len(entries)-1)
	out = append(out, entries[:idx]...)
	out = append(out, entries[idx+1:]...)
	return out
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

func toStringSlice(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		return compactStrings(v)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return compactStrings(out)
	case string:
		return compactStrings(strings.Split(v, ","))
	default:
		return nil
	}
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
