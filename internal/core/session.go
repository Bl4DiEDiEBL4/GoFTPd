package core

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"goftpd/internal/acl"
	"goftpd/internal/user"
)

// Session represents an active FTP client connection and its state.
type Session struct {
	ID            uint64
	Conn          net.Conn
	User          *user.User
	Config        *Config
	ACLEngine     *acl.Engine    // Engine for handling permissions/flags
	DupeChecker   interface{}    // dupe.DupeChecker for duplicate checking
	MasterManager interface{}    // *master.Manager for master/slave operations
	IsLogged      bool           // Login state (synchronized with commands.go)
	CurrentDir    string         // Virtual path
	RenameFrom    string         // Source for RNTO
	SSCN          bool           // Secure FXP mode
	DataListen    net.Listener   // For PASV mode
	ActiveAddr    string         // For PORT mode (Fixes the undefined error in commands.go)
	IsTLS         bool           // Control channel encryption state
	DataTLS       bool           // Data channel encryption state (PROT P)
	GroupMap      map[string]int // groupname -> GID mapping
	StartedAt     time.Time

	// Passthrough transfer state (drftpd-style direct client→slave)
	PretCmd         string      // "STOR", "RETR", or "" — set by PRET
	PretArg         string      // filename from PRET
	PassthruSlave   interface{} // slave selected for passthrough (avoids import cycle)
	PassthruXferIdx int32       // slave transfer index for passthrough
	RestOffset      int64       // REST offset applied to the next STOR/RETR
}

// readLinePure reads exactly one line byte-by-byte.
// It accepts a 'prefix' buffer to catch any bytes we might have "peeked" at
// during our legacy client check.
func readLinePure(conn net.Conn, prefix []byte) (string, error) {
	var buf []byte
	buf = append(buf, prefix...)
	b := make([]byte, 1)

	for {
		_, err := conn.Read(b)
		if err != nil {
			return "", err
		}
		buf = append(buf, b[0])
		if b[0] == '\n' {
			break
		}
		if len(buf) > 4096 {
			return "", fmt.Errorf("command line too long")
		}
	}
	return string(buf), nil
}

// HandleSession initializes the session and manages the command read loop.
func HandleSession(conn net.Conn, tlsConfig *tls.Config, cfg *Config, aclEngine *acl.Engine, dupeChecker interface{}) {
	session := &Session{
		Conn:          conn,
		Config:        cfg,
		ACLEngine:     aclEngine,
		DupeChecker:   dupeChecker,
		MasterManager: cfg.MasterManager,
		CurrentDir:    "/",
		GroupMap:      LoadGroupFile("etc/group"),
		StartedAt:     time.Now(),
	}
	session.ID = registerSession(session)
	defer unregisterSession(session.ID)
	defer session.Conn.Close()

	// Initial Banner
	fmt.Fprintf(session.Conn, "220-%s GoFTPd v%s\r\n220 Ready.\r\n",
		session.Config.SiteNameShort, session.Config.Version)

	var leftover []byte // Used to hold the 1 byte if we peeked it

	// Main Command Loop
	for {
		line, err := readLinePure(session.Conn, leftover)
		leftover = nil // Clear it immediately after using it

		if err != nil {
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if cfg.Debug {
			logLine := line
			if strings.HasPrefix(strings.ToUpper(line), "PASS ") {
				logLine = "PASS ********"
			}
			if session.Config.Debug {
				log.Printf("[%s] -> %s", session.Conn.RemoteAddr(), logLine)
			}
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		cmd := strings.ToUpper(parts[0])
		args := parts[1:]

		if session.Config.Debug {
			log.Printf("[CMD] raw=%q cmd=%q args=%q", line, cmd, args)
		}

		// Handle AUTH TLS to upgrade the control channel
		if cmd == "AUTH" && len(args) > 0 && strings.ToUpper(args[0]) == "TLS" {
			fmt.Fprintf(session.Conn, "234 AUTH TLS successful\r\n")

			tlsConn := tls.Server(session.Conn, tlsConfig)

			// Set a strict deadline so old/broken clients don't hang the server
			session.Conn.SetDeadline(time.Now().Add(10 * time.Second))
			if err := tlsConn.Handshake(); err != nil {
				if cfg.Debug {
					log.Printf("Handshake Error: %v", err)
				}
				return
			}

			// Handshake complete, clear the deadline
			session.Conn.SetDeadline(time.Time{})

			session.Conn = tlsConn
			session.IsTLS = true

			if cfg.Debug {
				log.Printf("[%s] TLS Handshake Successful", session.Conn.RemoteAddr())
			}

			// --- THE SMART PEEK FIX (Native net.Conn version) ---
			// RushFTP waits for 220 in dead silence. cbftp pipelines USER instantly.
			// We give the client 250ms to say something.
			session.Conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			peekBuf := make([]byte, 1)
			n, peekErr := session.Conn.Read(peekBuf)
			session.Conn.SetReadDeadline(time.Time{}) // Clear deadline immediately!

			if peekErr != nil {
				// If we hit a timeout, the client is silent. It's RushFTP.
				if netErr, ok := peekErr.(net.Error); ok && netErr.Timeout() {
					fmt.Fprintf(session.Conn, "220 TLS connection established\r\n")
					if cfg.Debug {
						log.Printf("[%s] Client silent after TLS, sent implicit 220 greeting", session.Conn.RemoteAddr())
					}
				}
			} else if n == 1 {
				// The client (cbftp) sent data immediately! (Probably the 'U' in USER)
				// We must save this byte so we don't lose it in the next read loop.
				leftover = peekBuf
			}
			// ----------------------------------------------------

			continue
		}

		// Execute the command via the shared processCommand logic
		if quit := session.processCommand(cmd, args, tlsConfig); quit {
			break
		}
	}
}

// getRawDataConn establishes the physical TCP connection for transfers (PORT or PASV).
func (s *Session) getRawDataConn() (net.Conn, error) {
	// Passive Mode (PASV)
	if s.DataListen != nil {
		if s.Config.Debug {
			log.Printf("Waiting for PASV connection on listener...")
		}
		s.DataListen.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
		conn, err := s.DataListen.Accept()
		s.DataListen.Close()
		s.DataListen = nil

		if err != nil {
			if s.Config.Debug {
				log.Printf("PASV Accept error: %v", err)
			}
			return nil, err
		}
		return conn, nil
	}

	// Active Mode (PORT)
	if s.ActiveAddr != "" {
		if s.Config.Debug {
			log.Printf("Dialing PORT connection to %s", s.ActiveAddr)
		}
		conn, err := net.DialTimeout("tcp", s.ActiveAddr, 10*time.Second)
		s.ActiveAddr = ""
		return conn, err
	}

	return nil, fmt.Errorf("no data connection method specified")
}

// upgradeDataTLS applies encryption to the data connection if PROT P is enabled.
func (s *Session) upgradeDataTLS(conn net.Conn, tlsConfig *tls.Config) (net.Conn, error) {
	if !s.DataTLS {
		if s.Config.Debug {
			log.Printf("Data connection in clear")
		}
		return conn, nil
	}

	if s.Config.Debug {
		log.Printf("Starting TLS handshake on data connection from %s", conn.RemoteAddr())
	}

	conn.SetDeadline(time.Now().Add(10 * time.Second))

	tlsConn := tls.Server(conn, tlsConfig.Clone())
	if err := tlsConn.Handshake(); err != nil {
		if s.Config.Debug {
			log.Printf("Data TLS Handshake error: %v", err)
		}
		conn.Close()
		return nil, err
	}

	conn.SetDeadline(time.Time{})

	if s.Config.Debug {
		log.Printf("Data TLS handshake successful")
	}
	return tlsConn, nil
}
