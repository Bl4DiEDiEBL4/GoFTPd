package selfip

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
)

type Plugin struct {
	host        string
	port        int
	user        string
	password    string
	useTLS      bool
	insecure    bool
	timeout     time.Duration
	replyTarget string
	channels    []string
	theme       *tmpl.Theme
}

func New() *Plugin {
	return &Plugin{
		host:        "127.0.0.1",
		port:        21212,
		user:        "goftpd",
		password:    "goftpd",
		useTLS:      true,
		insecure:    true,
		timeout:     10 * time.Second,
		replyTarget: "pm",
		channels:    []string{"#goftpd"},
	}
}

func (p *Plugin) Name() string { return "SelfIP" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "selfip")
	if s, ok := stringConfig(cfg, config, "host", "selfip_host"); ok && strings.TrimSpace(s) != "" {
		p.host = strings.TrimSpace(s)
	}
	if n := intConfig(configValue(cfg, config, "port", "selfip_port"), p.port); n > 0 {
		p.port = n
	}
	if s, ok := stringConfig(cfg, config, "user", "selfip_user"); ok && strings.TrimSpace(s) != "" {
		p.user = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(cfg, config, "password", "selfip_password"); ok {
		p.password = s
	}
	if b, ok := boolConfig(configValue(cfg, config, "tls", "selfip_tls")); ok {
		p.useTLS = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "insecure_skip_verify", "selfip_insecure_skip_verify")); ok {
		p.insecure = b
	}
	if n := intConfig(configValue(cfg, config, "timeout_seconds", "selfip_timeout_seconds"), 0); n > 0 {
		p.timeout = time.Duration(n) * time.Second
	}
	if raw, ok := configValueOK(cfg, config, "channels", "selfip_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "selfip_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	cmd := strings.ToLower(strings.TrimSpace(evt.Data["command"]))
	args := strings.Fields(strings.TrimSpace(evt.Data["args"]))
	if !p.allowed(evt) && (cmd == "ip" || cmd == "ips" || cmd == "addip" || cmd == "delip" || cmd == "chgip" || cmd == "changeip") {
		return p.reply(evt, p.render("SELFIPCMD_DENIED", map[string]string{"response": "command not enabled in this channel."}, "SELFIP: command not enabled in this channel.")), nil
	}

	switch cmd {
	case "ip":
		return p.help(evt), nil
	case "ips":
		if len(args) < 2 {
			return p.help(evt), nil
		}
		return p.runAndReply(evt, "LIST", args[0], args[1]), nil
	case "addip":
		if len(args) < 3 {
			return p.help(evt), nil
		}
		siteArgs := append([]string{"ADD", args[0], args[1]}, args[2:]...)
		return p.runAndReply(evt, siteArgs...), nil
	case "delip":
		if len(args) < 3 {
			return p.help(evt), nil
		}
		siteArgs := append([]string{"DEL", args[0], args[1]}, args[2:]...)
		return p.runAndReply(evt, siteArgs...), nil
	case "chgip", "changeip":
		if len(args) < 4 {
			return p.help(evt), nil
		}
		return p.runAndReply(evt, "CHG", args[0], args[1], args[2], args[3]), nil
	default:
		return nil, nil
	}
}

func (p *Plugin) help(evt *event.Event) []plugin.Output {
	lines := []string{
		p.render("SELFIPCMD_HELP", map[string]string{"line": "Use these commands in a PM to the bot so your password stays out of channel."}, "SELFIP: Use these commands in a PM to the bot so your password stays out of channel."),
		p.render("SELFIPCMD_HELP", map[string]string{"line": "All IP changes are logged to staff for abuse tracking."}, "SELFIP: All IP changes are logged to staff for abuse tracking."),
		p.render("SELFIPCMD_HELP", map[string]string{"line": "!ips <user> <pass> - list your current IPs"}, "SELFIP: !ips <user> <pass> - list your current IPs"),
		p.render("SELFIPCMD_HELP", map[string]string{"line": "!addip <user> <pass> <ident@ip> [ident@ip ...] - add IP(s)"}, "SELFIP: !addip <user> <pass> <ident@ip> [ident@ip ...] - add IP(s)"),
		p.render("SELFIPCMD_HELP", map[string]string{"line": "!delip <user> <pass> <ident@ip> [ident@ip ...] - remove IP(s)"}, "SELFIP: !delip <user> <pass> <ident@ip> [ident@ip ...] - remove IP(s)"),
		p.render("SELFIPCMD_HELP", map[string]string{"line": "!chgip <user> <pass> <oldip> <newip> - replace one IP with another"}, "SELFIP: !chgip <user> <pass> <oldip> <newip> - replace one IP with another"),
	}
	return p.reply(evt, lines...)
}

func (p *Plugin) runAndReply(evt *event.Event, siteArgs ...string) []plugin.Output {
	lines, err := p.runSITE(siteArgs...)
	response := responseText(lines)
	if err != nil {
		if response == "" {
			response = err.Error()
		}
		return p.reply(evt, p.render("SELFIPCMD_ERROR", map[string]string{"response": response}, "SELFIP: "+response))
	}
	if len(lines) == 0 {
		return p.reply(evt, p.render("SELFIPCMD_OK", map[string]string{"response": "done"}, "SELFIP: done"))
	}
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		key := "SELFIPCMD_LINE"
		if i == 0 {
			key = "SELFIPCMD_OK"
		}
		out = append(out, p.render(key, map[string]string{"line": line, "response": line}, "SELFIP: "+line))
	}
	return p.reply(evt, out...)
}

func (p *Plugin) runSITE(parts ...string) ([]string, error) {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	rawConn, err := net.DialTimeout("tcp", addr, p.timeout)
	if err != nil {
		return nil, err
	}
	defer rawConn.Close()
	_ = rawConn.SetDeadline(time.Now().Add(p.timeout))

	conn := rawConn
	reader := bufio.NewReader(conn)
	if _, _, err := readFTPResponse(reader); err != nil {
		return nil, err
	}

	if p.useTLS {
		if err := writeFTPCommand(conn, "AUTH TLS"); err != nil {
			return nil, err
		}
		code, lines, err := readFTPResponse(reader)
		if err != nil {
			return nil, err
		}
		if code < 200 || code >= 400 {
			clean := responseLines(lines)
			return clean, fmt.Errorf("AUTH TLS failed: %s", responseText(clean))
		}
		tlsConn := tls.Client(conn, &tls.Config{ServerName: p.host, InsecureSkipVerify: p.insecure})
		if err := tlsConn.Handshake(); err != nil {
			return nil, err
		}
		conn = tlsConn
		reader = bufio.NewReader(conn)
	}

	if err := writeFTPCommand(conn, "USER "+p.user); err != nil {
		return nil, err
	}
	code, lines, err := readFTPResponse(reader)
	if err != nil {
		return nil, err
	}
	if code == 331 {
		if err := writeFTPCommand(conn, "PASS "+p.password); err != nil {
			return nil, err
		}
		code, lines, err = readFTPResponse(reader)
		if err != nil {
			return nil, err
		}
	}
	if code < 200 || code >= 300 {
		clean := responseLines(lines)
		return clean, fmt.Errorf("login failed: %s", responseText(clean))
	}

	if err := writeFTPCommand(conn, "SITE SELFIP "+strings.Join(parts, " ")); err != nil {
		return nil, err
	}
	code, lines, err = readFTPResponse(reader)
	if err != nil {
		return nil, err
	}
	_ = writeFTPCommand(conn, "QUIT")
	clean := responseLines(lines)
	if code >= 400 {
		return clean, errors.New(responseText(clean))
	}
	return clean, nil
}

func (p *Plugin) allowed(evt *event.Event) bool {
	if strings.EqualFold(strings.TrimSpace(evt.Data["private"]), "true") {
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

func (p *Plugin) reply(evt *event.Event, lines ...string) []plugin.Output {
	target := evt.User
	notice := false
	if strings.HasPrefix(p.replyTarget, "#") {
		target = p.replyTarget
	} else if strings.EqualFold(p.replyTarget, "notice") {
		target = evt.User
		notice = true
	}
	out := make([]plugin.Output, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, plugin.Output{Type: "COMMAND", Target: target, Notice: notice, Text: line})
	}
	return out
}

func (p *Plugin) render(key string, vars map[string]string, fallback string) string {
	if p.theme != nil {
		if raw, ok := p.theme.Announces[key]; ok && raw != "" {
			return tmpl.Render(raw, vars)
		}
	}
	return fallback
}

func readFTPResponse(r *bufio.Reader) (int, []string, error) {
	line, err := readFTPLine(r)
	if err != nil {
		return 0, nil, err
	}
	if len(line) < 3 {
		return 0, []string{line}, fmt.Errorf("short FTP response: %s", line)
	}
	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, []string{line}, err
	}
	lines := []string{line}
	if len(line) > 3 && line[3] == '-' {
		endPrefix := line[:3] + " "
		for {
			next, err := readFTPLine(r)
			if err != nil {
				return code, lines, err
			}
			lines = append(lines, next)
			if strings.HasPrefix(next, endPrefix) {
				break
			}
		}
	}
	return code, lines, nil
}

func readFTPLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func writeFTPCommand(conn net.Conn, command string) error {
	_, err := fmt.Fprintf(conn, "%s\r\n", command)
	return err
}

func responseText(lines []string) string {
	return strings.Join(lines, " | ")
}

func responseLines(lines []string) []string {
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) >= 4 && isFTPCodePrefix(line) {
			line = strings.TrimSpace(line[4:])
		}
		if line != "" {
			parts = append(parts, line)
		}
	}
	return parts
}

func isFTPCodePrefix(line string) bool {
	if len(line) < 4 {
		return false
	}
	for i := 0; i < 3; i++ {
		if line[i] < '0' || line[i] > '9' {
			return false
		}
	}
	return line[3] == ' ' || line[3] == '-'
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
