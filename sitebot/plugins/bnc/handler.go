package bnc

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"goftpd/sitebot/internal/event"
	"goftpd/sitebot/internal/plugin"
	tmpl "goftpd/sitebot/internal/template"
)

type Target struct {
	Name string
	Host string
	Port int
}

type Plugin struct {
	user        string
	password    string
	useTLS      bool
	insecure    bool
	timeout     time.Duration
	replyTarget string
	channels    []string
	targets     []Target
	theme       *tmpl.Theme
}

func New() *Plugin {
	return &Plugin{
		user:        "goftpd",
		useTLS:      true,
		insecure:    true,
		timeout:     10 * time.Second,
		replyTarget: "channel",
		channels:    []string{},
		targets:     []Target{},
	}
}

func (p *Plugin) Name() string { return "BNC" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "bnc")
	if s, ok := stringConfig(cfg, config, "user", "bnc_user"); ok && strings.TrimSpace(s) != "" {
		p.user = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(cfg, config, "password", "bnc_password"); ok {
		p.password = s
	}
	if b, ok := boolConfig(configValue(cfg, config, "tls", "bnc_tls")); ok {
		p.useTLS = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "insecure_skip_verify", "bnc_insecure_skip_verify")); ok {
		p.insecure = b
	}
	if n := intConfig(configValue(cfg, config, "timeout_seconds", "bnc_timeout_seconds"), 0); n > 0 {
		p.timeout = time.Duration(n) * time.Second
	}
	if raw, ok := configValueOK(cfg, config, "channels", "bnc_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "bnc_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	if raw, ok := configValueOK(cfg, config, "targets", "bnc_targets"); ok {
		p.targets = parseTargets(raw)
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(evt.Data["command"])) != "bnc" {
		return nil, nil
	}
	if !p.channelAllowed(evt) {
		vars := map[string]string{"user": evt.User, "response": "command not enabled in this channel."}
		return p.reply(evt, p.render("BNC_ERROR", vars, "BNC: command not enabled in this channel.")), nil
	}
	if len(p.targets) == 0 {
		vars := map[string]string{"response": "no BNC targets configured."}
		return p.reply(evt, p.render("BNC_ERROR", vars, "BNC: no BNC targets configured.")), nil
	}

	filter := strings.ToLower(strings.TrimSpace(evt.Data["args"]))
	targets := p.targets
	if filter != "" {
		matched := make([]Target, 0, len(p.targets))
		for _, t := range p.targets {
			if strings.Contains(strings.ToLower(t.Name), filter) || strings.Contains(strings.ToLower(t.Host), filter) {
				matched = append(matched, t)
			}
		}
		targets = matched
		if len(targets) == 0 {
			vars := map[string]string{"response": "no matching BNC target."}
			return p.reply(evt, p.render("BNC_ERROR", vars, "BNC: no matching BNC target.")), nil
		}
	}

	lines := []string{p.render("BNC", map[string]string{
		"user":     evt.User,
		"channel":  evt.Data["channel"],
		"response": "Checking GoFTPd status, please wait ...",
	}, "BNC: Checking GoFTPd status, please wait ...")}

	for i, target := range targets {
		ok, latency, message := p.checkTarget(target)
		vars := map[string]string{
			"index":   fmt.Sprintf("%d", i+1),
			"name":    target.Name,
			"host":    target.Host,
			"port":    fmt.Sprintf("%d", target.Port),
			"latency": fmt.Sprintf("%d", latency.Milliseconds()),
			"error":   message,
		}
		if ok {
			lines = append(lines, p.render("BNC_UP", vars, fmt.Sprintf("%d.- %s at %s:%d is UP (login: %dms)", i+1, target.Name, target.Host, target.Port, latency.Milliseconds())))
		} else {
			lines = append(lines, p.render("BNC_DOWN", vars, fmt.Sprintf("%d.- %s at %s:%d is DOWN (%s)", i+1, target.Name, target.Host, target.Port, message)))
		}
	}
	return p.replies(evt, lines...), nil
}

func (p *Plugin) checkTarget(target Target) (bool, time.Duration, string) {
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	start := time.Now()
	rawConn, err := net.DialTimeout("tcp", addr, p.timeout)
	if err != nil {
		return false, 0, err.Error()
	}
	defer rawConn.Close()
	_ = rawConn.SetDeadline(time.Now().Add(p.timeout))

	conn := rawConn
	reader := bufio.NewReader(conn)
	if _, _, err := readFTPResponse(reader); err != nil {
		return false, 0, err.Error()
	}

	if p.useTLS {
		if err := writeFTPCommand(conn, "AUTH TLS"); err != nil {
			return false, 0, err.Error()
		}
		code, lines, err := readFTPResponse(reader)
		if err != nil {
			return false, 0, err.Error()
		}
		if code < 200 || code >= 400 {
			clean := responseLines(lines)
			return false, 0, responseText(clean)
		}
		tlsConn := tls.Client(conn, &tls.Config{ServerName: target.Host, InsecureSkipVerify: p.insecure})
		if err := tlsConn.Handshake(); err != nil {
			return false, 0, err.Error()
		}
		conn = tlsConn
		reader = bufio.NewReader(conn)
	}

	if err := writeFTPCommand(conn, "USER "+p.user); err != nil {
		return false, 0, err.Error()
	}
	code, lines, err := readFTPResponse(reader)
	if err != nil {
		return false, 0, err.Error()
	}
	if code == 331 {
		if err := writeFTPCommand(conn, "PASS "+p.password); err != nil {
			return false, 0, err.Error()
		}
		code, lines, err = readFTPResponse(reader)
		if err != nil {
			return false, 0, err.Error()
		}
	}
	_ = writeFTPCommand(conn, "QUIT")
	if code < 200 || code >= 300 {
		return false, 0, responseText(responseLines(lines))
	}
	return true, time.Since(start), ""
}

func parseTargets(raw interface{}) []Target {
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]Target, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			if mm, ok2 := item.(map[interface{}]interface{}); ok2 {
				m = map[string]interface{}{}
				for k, v := range mm {
					if ks, ok3 := k.(string); ok3 {
						m[ks] = v
					}
				}
			} else {
				continue
			}
		}
		host, _ := m["host"].(string)
		name, _ := m["name"].(string)
		if strings.TrimSpace(name) == "" {
			name = strings.TrimSpace(host)
		}
		port := intConfig(m["port"], 0)
		if strings.TrimSpace(host) == "" || port <= 0 {
			continue
		}
		out = append(out, Target{
			Name: strings.TrimSpace(name),
			Host: strings.TrimSpace(host),
			Port: port,
		})
	}
	return out
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
