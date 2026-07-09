package irc

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxIRCLineBytes = 510

type Bot struct {
	Host string
	Port int
	SSL  bool
	// TLSVerify controls server-certificate verification when SSL is true:
	// "strict" (default/empty) verifies against the system trust store,
	// "custom" verifies against TLSCACert, "insecure" explicitly disables
	// verification. Anything else falls back to "strict".
	TLSVerify    string
	TLSCACert    string
	Nick         string
	User         string
	RealName     string
	Channel      string
	Password     string
	Keys         map[string]*BlowfishEncryptor
	PMKeys       map[string]*BlowfishEncryptor
	PrivateKey   *BlowfishEncryptor
	Conn         net.Conn
	Connected    bool
	Debug        bool
	AutoExchange bool
	mu           sync.RWMutex
	pendingDH    map[string]*DH1080Ctx
	pendingPM    map[string][]queuedMessage
}

type queuedMessage struct {
	Target string
	Text   string
	Notice bool
}

func NewBot(host string, port int, nick, user, realname string) *Bot {
	return &Bot{
		Host:      host,
		Port:      port,
		Nick:      nick,
		User:      user,
		RealName:  realname,
		Keys:      make(map[string]*BlowfishEncryptor),
		PMKeys:    make(map[string]*BlowfishEncryptor),
		pendingDH: make(map[string]*DH1080Ctx),
		pendingPM: make(map[string][]queuedMessage),
		Debug:     true,
	}
}

func (b *Bot) Connect() error {
	addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))
	var conn net.Conn
	var err error
	if b.SSL {
		var tlsCfg *tls.Config
		tlsCfg, err = b.buildTLSConfig()
		if err != nil {
			return err
		}
		conn, err = tls.Dial("tcp", addr, tlsCfg)
	} else {
		conn, err = net.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	b.Conn = conn
	b.Connected = true
	if b.Password != "" {
		_ = b.SendRaw(fmt.Sprintf("PASS %s", b.Password))
	}
	_ = b.SendRaw(fmt.Sprintf("NICK %s", b.Nick))
	_ = b.SendRaw(fmt.Sprintf("USER %s 0 * :%s", b.User, b.RealName))
	if b.Debug {
		log.Printf("[IRC] Connected to %s:%d", b.Host, b.Port)
	}
	return nil
}

// buildTLSConfig builds the TLS client config for the IRC connection based on
// b.TLSVerify. Verification defaults to "strict" (system trust store) -
// InsecureSkipVerify is now an explicit opt-in ("insecure"), not the default.
func (b *Bot) buildTLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{ServerName: b.Host}
	switch strings.ToLower(strings.TrimSpace(b.TLSVerify)) {
	case "", "strict":
		// default: verify against the system trust store
	case "custom":
		pool, err := loadCACertPool(b.TLSCACert)
		if err != nil {
			return nil, fmt.Errorf("failed to load irc tls_ca_cert: %w", err)
		}
		cfg.RootCAs = pool
	case "insecure":
		cfg.InsecureSkipVerify = true
	default:
		return nil, fmt.Errorf("invalid irc tls_verify %q (expected strict, custom, or insecure)", b.TLSVerify)
	}
	return cfg, nil
}

// loadCACertPool reads a PEM-encoded CA certificate (or bundle) from path.
// Kept local to this package rather than shared with the main weaveftpd
// module: sitebot is a separate Go module with no dependency on it.
func loadCACertPool(path string) (*x509.CertPool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("tls_ca_cert path is required when tls_verify is \"custom\"")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA cert %q: %w", path, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no valid certificates found in %q", path)
	}
	return pool, nil
}

func (b *Bot) SendRaw(cmd string) error {
	if !b.Connected {
		return fmt.Errorf("not connected")
	}
	_, err := b.Conn.Write([]byte(cmd + "\r\n"))
	if b.Debug {
		log.Printf("[IRC] >> %s", cmd)
	}
	return err
}

func (b *Bot) SendMessage(channel, msg string) error {
	return b.sendTarget(channel, msg, false)
}

func (b *Bot) SendNotice(channel, msg string) error {
	return b.sendTarget(channel, msg, true)
}
func (b *Bot) SetTopic(channel, topic string, encrypt bool) error {
	if encrypt {
		if enc := b.channelKey(channel); enc != nil {
			topic = "+OK *" + enc.Encrypt(topic)
		}
	}
	return b.SendRaw(fmt.Sprintf("TOPIC %s :%s", channel, topic))
}
func (b *Bot) Join(channel string) error {
	b.Channel = channel
	return b.SendRaw(fmt.Sprintf("JOIN %s", channel))
}

// Invite sends an IRC INVITE command for nick into channel.
// The bot must have ops (or the channel must allow invites from non-ops).
func (b *Bot) Invite(nick, channel string) error {
	return b.SendRaw(fmt.Sprintf("INVITE %s %s", nick, channel))
}
func (b *Bot) SetChannelKey(channel, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == "" {
		delete(b.Keys, channel)
		return nil
	}
	enc, err := NewBlowfishEncryptor(key)
	if err != nil {
		return err
	}
	b.Keys[channel] = enc
	return nil
}

func (b *Bot) SetPrivateKey(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == "" {
		b.PrivateKey = nil
		return nil
	}
	enc, err := NewBlowfishEncryptor(key)
	if err != nil {
		return err
	}
	b.PrivateKey = enc
	return nil
}

func (b *Bot) SetAutoExchange(enabled bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.AutoExchange = enabled
}

func (b *Bot) SetPrivateUserKey(nick, key string) error {
	nick = normalizeNick(nick)
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == "" {
		delete(b.PMKeys, nick)
		return nil
	}
	enc, err := NewBlowfishEncryptor(key)
	if err != nil {
		return err
	}
	b.PMKeys[nick] = enc
	return nil
}

func (b *Bot) DecryptIncomingMessage(sender, target, msg string) (string, bool) {
	if !strings.HasPrefix(msg, "+OK ") {
		return msg, false
	}
	ciphertext := strings.TrimSpace(strings.TrimPrefix(msg, "+OK "))
	ciphertext = strings.TrimPrefix(ciphertext, "*")
	var enc *BlowfishEncryptor
	if strings.HasPrefix(target, "#") {
		enc = b.channelKey(target)
	} else if strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(b.Nick)) {
		enc = b.privateKeyForNick(sender)
	}
	if enc == nil {
		return msg, false
	}
	plain, err := enc.Decrypt(ciphertext)
	if err != nil {
		return msg, false
	}
	return plain, true
}

func (b *Bot) HandleKeyExchange(sender, target, msg string) (bool, error) {
	if !strings.EqualFold(strings.TrimSpace(target), strings.TrimSpace(b.Nick)) {
		return false, nil
	}
	if !strings.HasPrefix(msg, "DH1080_") {
		return false, nil
	}
	command, _, _, err := parseDH1080Message(msg)
	if err != nil {
		return true, err
	}
	switch command {
	case "DH1080_INIT":
		if !b.autoExchangeEnabled() {
			return true, nil
		}
		ctx, err := NewDH1080Ctx("CBC")
		if err != nil {
			return true, err
		}
		if err := ctx.Handle(msg); err != nil {
			return true, err
		}
		key, err := ctx.NegotiatedKey()
		if err != nil {
			return true, err
		}
		if err := b.SetPrivateUserKey(sender, key); err != nil {
			return true, err
		}
		return true, b.SendRaw(fmt.Sprintf("NOTICE %s :%s", sender, ctx.FinishMessage()))
	case "DH1080_FINISH":
		ctx, pending := b.takePendingDH(sender)
		if !pending {
			return true, nil
		}
		if err := ctx.Handle(msg); err != nil {
			return true, err
		}
		key, err := ctx.NegotiatedKey()
		if err != nil {
			return true, err
		}
		if err := b.SetPrivateUserKey(sender, key); err != nil {
			return true, err
		}
		return true, b.flushQueuedPrivate(sender)
	default:
		return true, nil
	}
}

func (b *Bot) Listen(handler func(string)) error {
	buf := make([]byte, 2048)
	for {
		n, err := b.Conn.Read(buf)
		if err != nil {
			b.Connected = false
			return err
		}
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if line == "" {
				continue
			}
			if b.Debug {
				log.Printf("[IRC] << %s", line)
			}
			if strings.HasPrefix(line, "PING") {
				parts := strings.Split(line, " ")
				_ = b.SendRaw(fmt.Sprintf("PONG %s", parts[1]))
				continue
			}
			handler(line)
		}
	}
}
func (b *Bot) Close() error {
	if b.Conn != nil {
		b.Connected = false
		return b.Conn.Close()
	}
	return nil
}
func (b *Bot) Quit(msg string) error {
	if msg == "" {
		msg = "GoSitebot away"
	}
	return b.SendRaw(fmt.Sprintf("QUIT :%s", msg))
}
func (b *Bot) SetTimeout(d time.Duration) error {
	if b.Conn != nil {
		return b.Conn.SetReadDeadline(time.Now().Add(d))
	}
	return nil
}

func (b *Bot) sendTarget(target, msg string, notice bool) error {
	if strings.HasPrefix(target, "#") {
		if enc := b.channelKey(target); enc != nil {
			return b.sendEncryptedTarget(target, msg, notice, enc)
		}
		return b.sendTargetRaw(target, msg, notice)
	}
	if enc := b.negotiatedPrivateKeyForNick(target); enc != nil {
		return b.sendEncryptedTarget(target, msg, notice, enc)
	}
	if b.autoExchangeEnabled() {
		initMsg, queued := b.queuePrivateForExchange(target, msg, notice)
		if queued && initMsg != "" {
			return b.SendRaw(fmt.Sprintf("NOTICE %s :%s", target, initMsg))
		}
		if queued {
			return nil
		}
	}
	if enc := b.staticPrivateKey(); enc != nil {
		return b.sendEncryptedTarget(target, msg, notice, enc)
	}
	return b.sendTargetRaw(target, msg, notice)
}

func (b *Bot) sendTargetRaw(target, msg string, notice bool) error {
	command := "PRIVMSG"
	if notice {
		command = "NOTICE"
	}
	prefix := fmt.Sprintf("%s %s :", command, target)
	for _, chunk := range splitIRCMessage(prefix, msg) {
		if err := b.SendRaw(prefix + chunk); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) sendEncryptedTarget(target, msg string, notice bool, enc *BlowfishEncryptor) error {
	command := "PRIVMSG"
	if notice {
		command = "NOTICE"
	}
	prefix := fmt.Sprintf("%s %s :", command, target)
	maxPlain := maxEncryptedPlainBytes(prefix)
	for _, chunk := range splitTextByByteLimit(msg, maxPlain) {
		encrypted := "+OK *" + enc.Encrypt(chunk)
		if len(prefix)+len(encrypted) > maxIRCLineBytes {
			encrypted = truncateIRCMessage(prefix, encrypted)
		}
		if err := b.SendRaw(prefix + encrypted); err != nil {
			return err
		}
	}
	return nil
}

func splitIRCMessage(prefix, msg string) []string {
	return splitTextByByteLimit(msg, maxIRCLineBytes-len(prefix))
}

func splitTextByByteLimit(msg string, max int) []string {
	if max <= 0 {
		return []string{""}
	}
	if len(msg) <= max {
		return []string{msg}
	}
	parts := make([]string, 0, (len(msg)/max)+1)
	for len(msg) > 0 {
		msg = strings.TrimLeft(msg, " \t")
		if msg == "" {
			break
		}
		if len(msg) <= max {
			parts = append(parts, msg)
			break
		}
		cut := lastWhitespaceCut(msg, max)
		if cut <= 0 {
			cut = safeUTF8Cut(msg, max)
		}
		chunk := strings.TrimRight(msg[:cut], " \t")
		if chunk == "" {
			cut = safeUTF8Cut(msg, max)
			chunk = msg[:cut]
		}
		parts = append(parts, chunk)
		msg = msg[cut:]
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func lastWhitespaceCut(s string, max int) int {
	cut := -1
	for i, r := range s {
		if i > max {
			break
		}
		if i > 0 && (r == ' ' || r == '\t') {
			cut = i
		}
	}
	return cut
}

func safeUTF8Cut(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	cut := max
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	if cut > 0 {
		return cut
	}
	_, size := utf8.DecodeRuneInString(s)
	if size > 0 {
		return size
	}
	return 1
}

func maxEncryptedPlainBytes(prefix string) int {
	maxCipher := maxIRCLineBytes - len(prefix) - len("+OK *")
	if maxCipher <= 0 {
		return 0
	}
	best := 1
	for n := 1; n <= maxCipher; n++ {
		if cbcBase64Len(n) <= maxCipher {
			best = n
			continue
		}
		if n > best+8 {
			break
		}
	}
	return best
}

func cbcBase64Len(plainLen int) int {
	padded := plainLen
	if rem := padded % 8; rem != 0 {
		padded += 8 - rem
	}
	raw := 8 + padded
	return ((raw + 2) / 3) * 4
}

func truncateIRCMessage(prefix, msg string) string {
	max := maxIRCLineBytes - len(prefix)
	if max <= 0 || len(msg) <= max {
		return msg
	}
	if max <= 3 {
		return truncateUTF8Bytes(msg, max)
	}
	return truncateUTF8Bytes(msg, max-3) + "..."
}

func truncateUTF8Bytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		if max <= 0 {
			return ""
		}
		return s
	}
	out := s[:max]
	for !utf8.ValidString(out) && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out
}

func (b *Bot) channelKey(channel string) *BlowfishEncryptor {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.Keys[channel]
}

func (b *Bot) privateKeyForNick(nick string) *BlowfishEncryptor {
	nick = normalizeNick(nick)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if enc, ok := b.PMKeys[nick]; ok {
		return enc
	}
	return b.PrivateKey
}

func (b *Bot) negotiatedPrivateKeyForNick(nick string) *BlowfishEncryptor {
	nick = normalizeNick(nick)
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.PMKeys[nick]
}

func (b *Bot) staticPrivateKey() *BlowfishEncryptor {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.PrivateKey
}

func (b *Bot) autoExchangeEnabled() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.AutoExchange
}

func (b *Bot) queuePrivateForExchange(target, msg string, notice bool) (string, bool) {
	nick := normalizeNick(target)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingPM[nick] = append(b.pendingPM[nick], queuedMessage{Target: target, Text: msg, Notice: notice})
	if _, ok := b.pendingDH[nick]; ok {
		return "", true
	}
	ctx, err := NewDH1080Ctx("CBC")
	if err != nil {
		if b.Debug {
			log.Printf("[IRC] Failed to create DH1080 context for %s: %v", target, err)
		}
		delete(b.pendingPM, nick)
		return "", false
	}
	b.pendingDH[nick] = ctx
	return ctx.InitMessage(), true
}

func (b *Bot) takePendingDH(nick string) (*DH1080Ctx, bool) {
	nick = normalizeNick(nick)
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx, ok := b.pendingDH[nick]
	if ok {
		delete(b.pendingDH, nick)
	}
	return ctx, ok
}

func (b *Bot) flushQueuedPrivate(nick string) error {
	nick = normalizeNick(nick)
	b.mu.Lock()
	queued := append([]queuedMessage(nil), b.pendingPM[nick]...)
	delete(b.pendingPM, nick)
	enc := b.PMKeys[nick]
	b.mu.Unlock()
	if enc == nil {
		return nil
	}
	for _, item := range queued {
		target := item.Target
		if strings.TrimSpace(target) == "" {
			target = nick
		}
		if err := b.sendEncryptedTarget(target, item.Text, item.Notice, enc); err != nil {
			return err
		}
	}
	return nil
}

func normalizeNick(nick string) string {
	return strings.ToLower(strings.TrimSpace(nick))
}
