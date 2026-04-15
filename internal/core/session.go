package core

import (
	"bufio"
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
	Conn       net.Conn
	User       *user.User
	Config     *Config
	ACLEngine  *acl.Engine // Engine for handling permissions/flags
	DupeChecker interface{} // dupe.DupeChecker for duplicate checking
	MasterManager interface{} // *master.Manager for master/slave operations
	IsLogged   bool        // Login state (synchronized with commands.go)
	CurrentDir string      // Virtual path
	RenameFrom string      // Source for RNTO
	SSCN       bool        // Secure FXP mode
	DataListen net.Listener // For PASV mode
	ActiveAddr string      // For PORT mode (Fixes the undefined error in commands.go)
	IsTLS      bool        // Control channel encryption state
	DataTLS    bool        // Data channel encryption state (PROT P)
	GroupMap   map[string]int // groupname -> GID mapping
}

// HandleSession initializes the session and manages the command read loop.
func HandleSession(conn net.Conn, tlsConfig *tls.Config, cfg *Config, aclEngine *acl.Engine, dupeChecker interface{}) {
	session := &Session{
		Conn:       conn,
		Config:     cfg,
		ACLEngine:  aclEngine,
		DupeChecker: dupeChecker,
		MasterManager: cfg.MasterManager,
		CurrentDir: "/",
		GroupMap:   LoadGroupFile("etc/group"),
	}
	defer session.Conn.Close()

	// Initial Banner
	fmt.Fprintf(session.Conn, "220-%s GoFTPd v%s\r\n220 Ready.\r\n", 
		session.Config.SiteNameShort, session.Config.Version)

	reader := bufio.NewReader(session.Conn)
	for {
		line, err := reader.ReadString('\n')
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

		// Handle AUTH TLS to upgrade the control channel
		if cmd == "AUTH" && len(args) > 0 && strings.ToUpper(args[0]) == "TLS" {
			fmt.Fprintf(session.Conn, "234 AUTH TLS successful\r\n")
			
			// Small delay for client-side state toggle
			time.Sleep(60 * time.Millisecond)

			tlsConn := tls.Server(session.Conn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				if cfg.Debug {
					log.Printf("Handshake Error: %v", err)
				}
				return
			}
			
			session.Conn = tlsConn
			session.IsTLS = true
			
			// Re-wrap the reader with the encrypted stream
			reader = bufio.NewReader(session.Conn)
			
			if cfg.Debug {
				log.Printf("[%s] TLS Handshake Successful", session.Conn.RemoteAddr())
			}
			
			fmt.Fprintf(session.Conn, "220 TLS connection established\r\n")
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
		// Set a 30s deadline for the client to connect
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
		s.ActiveAddr = "" // Clear after use
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
	
	// Ensure the handshake doesn't hang the master process
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	
	// Clone to ensure unique session states
	tlsConn := tls.Server(conn, tlsConfig.Clone())
	if err := tlsConn.Handshake(); err != nil {
		if s.Config.Debug {
			log.Printf("Data TLS Handshake error: %v", err)
		}
		conn.Close()
		return nil, err
	}
	
	conn.SetDeadline(time.Time{}) // Restore timeout for actual data transfer
	
	if s.Config.Debug {
		log.Printf("Data TLS handshake successful")
	}
	return tlsConn, nil
}