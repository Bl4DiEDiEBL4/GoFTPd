package irc

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

type Bot struct {
	Host      string
	Port      int
	SSL       bool
	Nick      string
	User      string
	RealName  string
	Channel   string
	Password  string
	Keys      map[string]*BlowfishEncryptor
	Conn      net.Conn
	Connected bool
	Debug     bool
}

func NewBot(host string, port int, nick, user, realname string) *Bot {
	return &Bot{Host: host, Port: port, Nick: nick, User: user, RealName: realname, Keys: make(map[string]*BlowfishEncryptor), Debug: true}
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
	if b.Debug { log.Printf("[IRC] Connected to %s:%d", b.Host, b.Port) }
	return nil
}

func (b *Bot) SendRaw(cmd string) error {
	if !b.Connected { return fmt.Errorf("not connected") }
	_, err := b.Conn.Write([]byte(cmd + "\r\n"))
	if b.Debug { log.Printf("[IRC] >> %s", cmd) }
	return err
}

func (b *Bot) SendMessage(channel, msg string) error {
	if enc, ok := b.Keys[channel]; ok {
		msg = "+OK *" + enc.Encrypt(msg)
	}
	return b.SendRaw(fmt.Sprintf("PRIVMSG %s :%s", channel, msg))
}

func (b *Bot) SendNotice(channel, msg string) error { return b.SendRaw(fmt.Sprintf("NOTICE %s :%s", channel, msg)) }
func (b *Bot) Join(channel string) error { b.Channel = channel; return b.SendRaw(fmt.Sprintf("JOIN %s", channel)) }

// Invite sends an IRC INVITE command for nick into channel.
// The bot must have ops (or the channel must allow invites from non-ops).
func (b *Bot) Invite(nick, channel string) error {
	return b.SendRaw(fmt.Sprintf("INVITE %s %s", nick, channel))
}
func (b *Bot) SetChannelKey(channel, key string) error { if key=="" { delete(b.Keys, channel); return nil }; enc, err := NewBlowfishEncryptor(key); if err!=nil { return err}; b.Keys[channel]=enc; return nil }
func (b *Bot) Listen(handler func(string)) error {
	buf := make([]byte, 2048)
	for {
		n, err := b.Conn.Read(buf)
		if err != nil { b.Connected = false; return err }
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if line == "" { continue }
			if b.Debug { log.Printf("[IRC] << %s", line) }
			if strings.HasPrefix(line, "PING") {
				parts := strings.Split(line, " ")
				_ = b.SendRaw(fmt.Sprintf("PONG %s", parts[1]))
				continue
			}
			handler(line)
		}
	}
}
func (b *Bot) Close() error { if b.Conn != nil { b.Connected=false; return b.Conn.Close() }; return nil }
func (b *Bot) Quit(msg string) error { if msg=="" { msg="GoSitebot away" }; return b.SendRaw(fmt.Sprintf("QUIT :%s", msg)) }
func (b *Bot) SetTimeout(d time.Duration) error { if b.Conn!=nil { return b.Conn.SetReadDeadline(time.Now().Add(d))}; return nil }
