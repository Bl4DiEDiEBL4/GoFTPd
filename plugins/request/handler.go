package request

import (
	"bufio"
	"fmt"
	"log"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goftpd/internal/plugin"
	"goftpd/internal/timeutil"
)

type Plugin struct {
	svc                       *plugin.Services
	dir                       string
	reqFile                   string
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
	proxyUsers                []string
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

var reqLineRE = regexp.MustCompile(`^\[\s*(\d+):\]\s+(.+?)\s+~\s+by\s+(.+?)\s+\((.*?)\)(?:\s+for\s+(.+?))?\s+at\s+(.+)$`)

func New() *Plugin {
	return &Plugin{
		dir:          "/REQUESTS",
		reqFile:      "/REQUESTS/.requests",
		requestHead:  "REQ-",
		filledHead:   "FILLED-",
		replaceWith:  "_",
		badChars:     `*{^~/,+&\`,
		maxRequests:  30,
		reqwipeFlags: "1",
		proxyUsers:   []string{"goftpd"},
		sitename:     "GoFTPd",
		stopCh:       make(chan struct{}),
	}
}

func (p *Plugin) Name() string { return "request" }

func (p *Plugin) Init(svc *plugin.Services, cfg map[string]interface{}) error {
	p.svc = svc
	if s := stringConfig(cfg, "dir", ""); strings.TrimSpace(s) != "" {
		p.dir = cleanAbs(s)
	}
	if s := stringConfig(cfg, "reqfile", ""); strings.TrimSpace(s) != "" {
		p.reqFile = cleanAbs(s)
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
	if s := stringConfig(cfg, "sitename", ""); strings.TrimSpace(s) != "" {
		p.sitename = strings.TrimSpace(s)
	}
	if n := intConfig(cfg["max_requests"], 0); n >= 0 {
		p.maxRequests = n
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
	if _, configured := cfg["reqfile"]; !configured && p.dir != "/REQUESTS" {
		p.reqFile = path.Join(p.dir, ".requests")
	} else if strings.TrimSpace(p.reqFile) == "" {
		p.reqFile = path.Join(p.dir, ".requests")
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
	return []string{"REQUEST", "REQUESTS", "REQFILL", "REQFILLED", "REQDEL", "REQWIPE"}
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

	entries := p.loadRequests()
	if p.maxRequests > 0 && len(entries) >= p.maxRequests {
		p.reply(ctx, "200", "No more requests allowed. Fill some up.")
		return true
	}
	if p.findRequest(entries, release) >= 0 || p.dirExists(p.requestDir(release)) || p.dirExists(p.filledDir(release)) {
		p.reply(ctx, "200", fmt.Sprintf("%s has already been requested.", release))
		return true
	}

	p.ensureBaseDir(ctx)
	if p.requestHead != "" && !p.doNotCreateDirUntilFilled {
		p.svc.Bridge.MakeDir(p.requestDir(release), ctx.UserName(), ctx.UserPrimaryGroup())
		_ = p.svc.Bridge.Chmod(p.requestDir(release), 0777)
	}

	entries = append(entries, requestEntry{
		Release: release,
		By:      firstNonEmpty(byUser, ctx.UserName()),
		Mode:    "gl",
		For:     forUser,
		Date:    timeutil.Now().Format("2006-01-02 15:04"),
	})
	p.saveRequests(entries)
	added := p.numberedEntry(entries[len(entries)-1])
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
	key := strings.TrimSpace(strings.Join(args, " "))
	if key == "" {
		p.reply(ctx, "501", "Usage: SITE REQFILL <number|request>")
		return true
	}
	entries := p.loadRequests()
	idx := p.findRequest(entries, key)
	if idx < 0 {
		p.reply(ctx, "200", fmt.Sprintf("The request %s does not exist.", key))
		return true
	}
	entry := entries[idx]
	reqDir := p.requestDir(entry.Release)
	if p.requestHead != "" && p.dirExists(reqDir) {
		if p.dirEmpty(reqDir) {
			p.reply(ctx, "200", "That request is empty. Can not reqfill it.")
			return true
		}
		toDir, toName := p.availableFilledTarget(entry.Release)
		p.svc.Bridge.RenameFile(reqDir, toDir, toName)
	} else if p.doNotCreateDirUntilFilled {
		p.svc.Bridge.MakeDir(p.filledDir(entry.Release), ctx.UserName(), ctx.UserPrimaryGroup())
		_ = p.svc.Bridge.Chmod(p.filledDir(entry.Release), 0777)
	}
	entries = removeEntry(entries, idx)
	p.saveRequests(entries)
	if p.showOnFill {
		return p.replyStatus(ctx)
	}
	p.reply(ctx, "200", fmt.Sprintf("%s : %s has been filled. Thank you.", key, entry.Release))
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
	p.saveRequests(entries)
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
	p.saveRequests(entries)
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
	data, err := p.svc.Bridge.ReadFile(p.reqFile)
	if err != nil || len(data) == 0 {
		return nil
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var entries []requestEntry
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if entry, ok := parseRequestLine(line); ok {
			entries = append(entries, entry)
		}
	}
	for i := range entries {
		entries[i].Num = i + 1
	}
	return entries
}

func parseRequestLine(line string) (requestEntry, bool) {
	m := reqLineRE.FindStringSubmatch(line)
	if len(m) == 0 {
		return requestEntry{}, false
	}
	num, _ := strconv.Atoi(m[1])
	return requestEntry{
		Num:     num,
		Release: strings.TrimSpace(m[2]),
		By:      strings.TrimSpace(m[3]),
		Mode:    strings.TrimSpace(m[4]),
		For:     strings.TrimSpace(m[5]),
		Date:    strings.TrimSpace(m[6]),
	}, true
}

func (p *Plugin) saveRequests(entries []requestEntry) {
	for i := range entries {
		entries[i].Num = i + 1
		if entries[i].Mode == "" {
			entries[i].Mode = "gl"
		}
	}
	var b strings.Builder
	for _, entry := range entries {
		line := fmt.Sprintf("%s %s ~ by %s (%s)", p.numberedEntry(entry), entry.Release, entry.By, entry.Mode)
		if entry.For != "" {
			line += " for " + entry.For
		}
		line += " at " + entry.Date
		b.WriteString(line)
		b.WriteByte('\n')
	}
	p.ensureBaseDir(nil)
	_ = p.svc.Bridge.WriteFile(p.reqFile, []byte(b.String()))
}

func (p *Plugin) ensureBaseDir(ctx plugin.SiteContext) {
	if p.dirExists(p.dir) {
		return
	}
	owner := "GoFTPd"
	group := "GoFTPd"
	if ctx != nil {
		owner = ctx.UserName()
		group = ctx.UserPrimaryGroup()
	}
	p.svc.Bridge.MakeDir(p.dir, owner, group)
	_ = p.svc.Bridge.Chmod(p.dir, 0777)
}

func (p *Plugin) ensureBaseDirOnStartup() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for attempt := 0; attempt < 60; attempt++ {
		if p.svc != nil && p.svc.Bridge != nil {
			p.ensureBaseDir(nil)
			if p.dirExists(p.dir) {
				if p.debug {
					log.Printf("[REQUEST] ensured request dir %s", p.dir)
				}
				return
			}
			if p.debug {
				log.Printf("[REQUEST] request dir %s not ready yet", p.dir)
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
