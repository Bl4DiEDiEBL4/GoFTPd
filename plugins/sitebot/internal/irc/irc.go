package irc

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// Bot represents an IRC bot connection
type Bot struct {
	Host      string
	Port      int
	Nick      string
	User      string
	RealName  string
	Channel   string
	Password  string
	Keys      map[string]*BlowfishEncryptor // Channel -> encryptor
	Conn      net.Conn
	Connected bool
	Debug     bool
}

// NewBot creates a new IRC bot
func NewBot(host string, port int, nick, user, realname string) *Bot {
	return &Bot{
		Host:     host,
		Port:     port,
		Nick:     nick,
		User:     user,
		RealName: realname,
		Keys:     make(map[string]*BlowfishEncryptor),
		Debug:    true,
	}
}

// Connect connects to IRC server
func (b *Bot) Connect() error {
	addr := fmt.Sprintf("%s:%d", b.Host, b.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	
	b.Conn = conn
	b.Connected = true
	
	// Send PASS if password is set
	if b.Password != "" {
		b.SendRaw(fmt.Sprintf("PASS %s", b.Password))
	}
	
	// Send NICK and USER
	b.SendRaw(fmt.Sprintf("NICK %s", b.Nick))
	b.SendRaw(fmt.Sprintf("USER %s 0 * :%s", b.User, b.RealName))
	
	if b.Debug {
		log.Printf("[IRC] Connected to %s:%d", b.Host, b.Port)
	}
	
	return nil
}

// SendRaw sends raw IRC command
func (b *Bot) SendRaw(cmd string) error {
	if !b.Connected {
		return fmt.Errorf("not connected")
	}
	
	line := cmd + "\r\n"
	_, err := b.Conn.Write([]byte(line))
	
	if b.Debug {
		log.Printf("[IRC] >> %s", cmd)
	}
	
	return err
}

// SendMessage sends a message to channel
func (b *Bot) SendMessage(channel, msg string) error {
	// Check if channel has encryption key
	if enc, ok := b.Keys[channel]; ok {
		encrypted := enc.Encrypt(msg)
		msg = "+OK " + encrypted
	}
	
	return b.SendRaw(fmt.Sprintf("PRIVMSG %s :%s", channel, msg))
}

// SendNotice sends a notice to channel
func (b *Bot) SendNotice(channel, msg string) error {
	return b.SendRaw(fmt.Sprintf("NOTICE %s :%s", channel, msg))
}

// Join joins a channel
func (b *Bot) Join(channel string) error {
	b.Channel = channel
	return b.SendRaw(fmt.Sprintf("JOIN %s", channel))
}

// SetChannelKey sets encryption key for a channel
func (b *Bot) SetChannelKey(channel, key string) error {
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

// Listen listens for incoming IRC messages
func (b *Bot) Listen(handler func(string)) error {
	buf := make([]byte, 512)
	
	for {
		n, err := b.Conn.Read(buf)
		if err != nil {
			b.Connected = false
			return err
		}
		
		lines := strings.Split(string(buf[:n]), "\r\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			
			if b.Debug {
				log.Printf("[IRC] << %s", line)
			}
			
			// Handle PING
			if strings.HasPrefix(line, "PING") {
				parts := strings.Split(line, " ")
				b.SendRaw(fmt.Sprintf("PONG %s", parts[1]))
				continue
			}
			
			handler(line)
		}
	}
}

// Close closes the connection
func (b *Bot) Close() error {
	if b.Conn != nil {
		b.Connected = false
		return b.Conn.Close()
	}
	return nil
}

// Quit gracefully quits IRC
func (b *Bot) Quit(msg string) error {
	if msg == "" {
		msg = "GoSitebot away"
	}
	return b.SendRaw(fmt.Sprintf("QUIT :%s", msg))
}

// Timeout sets read timeout
func (b *Bot) SetTimeout(d time.Duration) error {
	if b.Conn != nil {
		return b.Conn.SetReadDeadline(time.Now().Add(d))
	}
	return nil
}
