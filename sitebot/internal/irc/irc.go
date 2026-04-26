package irc

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

type Bot struct {
	Host         string
	Port         int
	SSL          bool
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
	addr := fmt.Sprintf("%s:%d", b.Host, b.Port)
	var conn net.Conn
	var err error
	if b.SSL {
		conn, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
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
			msg = "+OK *" + enc.Encrypt(msg)
		}
		return b.sendTargetRaw(target, msg, notice)
	}
	if enc := b.negotiatedPrivateKeyForNick(target); enc != nil {
		msg = "+OK *" + enc.Encrypt(msg)
		return b.sendTargetRaw(target, msg, notice)
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
		msg = "+OK *" + enc.Encrypt(msg)
		return b.sendTargetRaw(target, msg, notice)
	}
	return b.sendTargetRaw(target, msg, notice)
}

func (b *Bot) sendTargetRaw(target, msg string, notice bool) error {
	command := "PRIVMSG"
	if notice {
		command = "NOTICE"
	}
	return b.SendRaw(fmt.Sprintf("%s %s :%s", command, target, msg))
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
		msg := "+OK *" + enc.Encrypt(item.Text)
		if err := b.sendTargetRaw(target, msg, item.Notice); err != nil {
			return err
		}
	}
	return nil
}

func normalizeNick(nick string) string {
	return strings.ToLower(strings.TrimSpace(nick))
}
