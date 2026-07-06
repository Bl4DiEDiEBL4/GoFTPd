package bw

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"weaveftpd/sitebot/internal/event"
	"weaveftpd/sitebot/internal/plugin"
	tmpl "weaveftpd/sitebot/internal/template"
)

type Plugin struct {
	host        string
	port        int
	user        string
	password    string
	siteName    string
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
		port:        2121,
		user:        "weaveftpd",
		siteName:    "WeaveFTPd",
		useTLS:      true,
		insecure:    true,
		timeout:     10 * time.Second,
		replyTarget: "channel",
		channels:    []string{},
	}
}

func (p *Plugin) Name() string { return "BW" }

func (p *Plugin) Initialize(config map[string]interface{}) error {
	if themeFile, ok := config["theme_file"].(string); ok && strings.TrimSpace(themeFile) != "" {
		th, err := tmpl.LoadTheme(themeFile)
		if err == nil {
			p.theme = th
		}
	}

	cfg := plugin.ConfigSection(config, "bandwidth")
	if s, ok := stringConfig(cfg, config, "host", "bandwidth_host"); ok && strings.TrimSpace(s) != "" {
		p.host = strings.TrimSpace(s)
	}
	if n := intConfig(configValue(cfg, config, "port", "bandwidth_port"), 0); n > 0 {
		p.port = n
	}
	if s, ok := stringConfig(cfg, config, "user", "bandwidth_user"); ok && strings.TrimSpace(s) != "" {
		p.user = strings.TrimSpace(s)
	}
	if s, ok := firstStringConfig(cfg, config,
		[2]string{"sitename", "sitename"},
		[2]string{"sitename_short", "sitename_short"},
		[2]string{"sitename_long", "sitename_long"},
	); ok && strings.TrimSpace(s) != "" {
		p.siteName = strings.TrimSpace(s)
	}
	if s, ok := stringConfig(cfg, config, "password", "bandwidth_password"); ok {
		p.password = s
	}
	if b, ok := boolConfig(configValue(cfg, config, "tls", "bandwidth_tls")); ok {
		p.useTLS = b
	}
	if b, ok := boolConfig(configValue(cfg, config, "insecure_skip_verify", "bandwidth_insecure_skip_verify")); ok {
		p.insecure = b
	}
	if n := intConfig(configValue(cfg, config, "timeout_seconds", "bandwidth_timeout_seconds"), 0); n > 0 {
		p.timeout = time.Duration(n) * time.Second
	}
	if raw, ok := configValueOK(cfg, config, "channels", "bandwidth_channels"); ok {
		p.channels = plugin.ToStringSlice(raw, p.channels)
	}
	if s, ok := stringConfig(cfg, config, "reply_target", "bandwidth_reply_target"); ok && strings.TrimSpace(s) != "" {
		p.replyTarget = strings.ToLower(strings.TrimSpace(s))
	}
	return nil
}

func (p *Plugin) Close() error { return nil }

func (p *Plugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
	if evt.Type != event.EventCommand {
		return nil, nil
	}
	if strings.ToLower(strings.TrimSpace(evt.Data["command"])) != "bw" {
		return nil, nil
	}
	if !p.channelAllowed(evt) {
		return p.reply(evt, p.render("BW_ERROR", map[string]string{"response": "command not enabled in this channel."}, "BANDWiDTH: command not enabled in this channel.")), nil
	}

	args := strings.TrimSpace(evt.Data["args"])
	lines, err := p.query(args)
	if err != nil {
		return p.reply(evt, p.render("BW_ERROR", map[string]string{"response": err.Error()}, "BANDWiDTH: "+err.Error())), nil
	}
	if len(lines) == 0 {
		return p.reply(evt, p.render("BW_ERROR", map[string]string{"response": "no bandwidth output returned."}, "BANDWiDTH: no bandwidth output returned.")), nil
	}

	waitText := fmt.Sprintf("Checking %s bandwidth, please wait ...", p.siteName)
	outLines := []string{p.render("BW", map[string]string{"response": waitText, "sitename": p.siteName}, "BANDWiDTH: "+waitText)}
	for _, line := range lines {
		colored := stylizeBandwidthLine(line)
		outLines = append(outLines, p.render("BW_LINE", map[string]string{"response": colored, "sitename": p.siteName}, colored))
	}
	return p.replies(evt, outLines...), nil
}

var bwSummaryPattern = regexp.MustCompile(`^BANDWiDTH: \((\d+) uploading at ([^)]+)\) - \((\d+) downloading at ([^)]+)\) - \((\d+) browsing\) - \((\d+) idling\) - \[(\d+) users / (\d+) connections total at ([^\]]+)\]$`)
var bwSlavePattern = regexp.MustCompile(`^BANDWiDTH: ([^-]+?) - \((\d+) uploading at ([^)]+)\) - \((\d+) downloading at ([^)]+)\) - \[(\d+) transfers at ([^\]]+)\]$`)
var bwUserTransferPattern = regexp.MustCompile(`^BANDWiDTH: (.+?) is (uploading|downloading) (.+?) at (.+?)(?: via (.+))?$`)

func stylizeBandwidthLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return line
	}
	if m := bwSummaryPattern.FindStringSubmatch(line); m != nil {
		return fmt.Sprintf(
			"\x0301BANDWiDTH:\x0f (\x0303%s uploading at %s\x0f) - (\x0304%s downloading at %s\x0f) - (\x0312%s browsing\x0f) - (\x0314%s idling\x0f) - [\x0306%s users\x0f / \x0307%s connections\x0f total at %s]",
			m[1], colorSpeed(m[2], "03"),
			m[3], colorSpeed(m[4], "04"),
			m[5], m[6],
			m[7], m[8], colorSpeed(m[9], "07"),
		)
	}
	if m := bwSlavePattern.FindStringSubmatch(line); m != nil {
		return fmt.Sprintf(
			"\x0301BANDWiDTH:\x0f \x0306%s\x0f - (\x0303%s uploading at %s\x0f) - (\x0304%s downloading at %s\x0f) - [\x0307%s transfers at %s\x0f]",
			strings.TrimSpace(m[1]),
			m[2], colorSpeed(m[3], "03"),
			m[4], colorSpeed(m[5], "04"),
			m[6], colorSpeed(m[7], "07"),
		)
	}
	if m := bwUserTransferPattern.FindStringSubmatch(line); m != nil {
		actionColor := "03"
		if strings.EqualFold(m[2], "downloading") {
			actionColor = "04"
		}
		via := ""
		if len(m) > 5 && strings.TrimSpace(m[5]) != "" {
			via = fmt.Sprintf(" via \x0306%s\x0f", strings.TrimSpace(m[5]))
		}
		return fmt.Sprintf(
			"\x0301BANDWiDTH:\x0f \x0306%s\x0f is \x03%s%s\x0f \x0312%s\x0f at %s%s",
			m[1], actionColor, m[2], m[3], colorSpeed(m[4], actionColor), via,
		)
	}
	return "\x0301" + line + "\x0f"
}

func colorSpeed(speed string, color string) string {
	return fmt.Sprintf("\x03%s%s\x0f", color, strings.TrimSpace(speed))
}

func (p *Plugin) query(args string) ([]string, error) {
	conn, reader, err := p.connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	command := "SITE BW"
	if strings.TrimSpace(args) != "" {
		command += " " + strings.TrimSpace(args)
	}
	if err := writeFTPCommand(conn, command); err != nil {
		return nil, err
	}

	return readAllFTPReplies(conn, reader, p.timeout)
}

func (p *Plugin) connect() (net.Conn, *bufio.Reader, error) {
	addr := net.JoinHostPort(p.host, strconv.Itoa(p.port))
	rawConn, err := net.DialTimeout("tcp", addr, p.timeout)
	if err != nil {
		return nil, nil, err
	}
	_ = rawConn.SetDeadline(time.Now().Add(p.timeout))

	conn := net.Conn(rawConn)
	reader := bufio.NewReader(conn)
	if _, _, err := readFTPResponse(reader); err != nil {
		rawConn.Close()
		return nil, nil, err
	}

	if p.useTLS {
		if err := writeFTPCommand(conn, "AUTH TLS"); err != nil {
			rawConn.Close()
			return nil, nil, err
		}
		code, lines, err := readFTPResponse(reader)
		if err != nil {
			rawConn.Close()
			return nil, nil, err
		}
		if code < 200 || code >= 400 {
			rawConn.Close()
			return nil, nil, errors.New(responseText(responseLines(lines)))
		}
		tlsConn := tls.Client(conn, &tls.Config{ServerName: p.host, InsecureSkipVerify: p.insecure})
		if err := tlsConn.Handshake(); err != nil {
			rawConn.Close()
			return nil, nil, err
		}
		conn = tlsConn
		reader = bufio.NewReader(conn)
	}

	if err := writeFTPCommand(conn, "USER "+p.user); err != nil {
		conn.Close()
		return nil, nil, err
	}
	code, lines, err := readFTPResponse(reader)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if code == 331 {
		if err := writeFTPCommand(conn, "PASS "+p.password); err != nil {
			conn.Close()
			return nil, nil, err
		}
		code, lines, err = readFTPResponse(reader)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
	}
	if code < 200 || code >= 300 {
		conn.Close()
		return nil, nil, errors.New(responseText(responseLines(lines)))
	}
	return conn, reader, nil
}

func readAllFTPReplies(conn net.Conn, reader *bufio.Reader, timeout time.Duration) ([]string, error) {
	var lines []string
	for {
		code, respLines, err := readFTPResponse(reader)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && len(lines) > 0 {
				break
			}
			return nil, err
		}
		for _, line := range responseLines(respLines) {
			if line != "" {
				lines = append(lines, line)
			}
		}
		if code >= 400 {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := reader.Peek(1); err != nil {
			_ = conn.SetReadDeadline(time.Time{})
			break
		}
		_ = conn.SetReadDeadline(time.Time{})
	}
	_ = conn.SetReadDeadline(time.Time{})
	return lines, nil
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

func firstStringConfig(section, flat map[string]interface{}, keys ...[2]string) (string, bool) {
	for _, key := range keys {
		if s, ok := stringConfig(section, flat, key[0], key[1]); ok {
			if strings.TrimSpace(s) != "" {
				return s, true
			}
		}
	}
	return "", false
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
