package slave

import (
	"crypto/tls"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/protocol"
)

const (
	socketTimeout = 10 * time.Second
	actualTimeout = 60 * time.Second
)

// Slave is the slave daemon, 
// It connects to a master, sends its name, then enters a command/response loop.
type Slave struct {
	name          string
	masterHost    string
	masterPort    int
	roots         []string // local filesystem roots (1, slave.root.2)
	pasvPortMin   int
	pasvPortMax   int
	tlsEnabled    bool
	tlsCert       string
	tlsKey        string
	bindIP        string

	conn          net.Conn
	stream        *protocol.ObjectStream
	writeMu       sync.Mutex // protects stream writes (gob is not thread-safe)
	transfers     sync.Map   // transferIndex (int32) -> *Transfer
	nextTransferIdx int32

	online        atomic.Bool
	lastWriteTime atomic.Int64 // UnixMilli of last successful write
	timeout       time.Duration
}


// writeObject sends an object to the master with mutex protection.
// gob encoding is not thread-safe, so all writes must be serialized.
func (s *Slave) writeObject(obj interface{}) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err := s.stream.WriteObject(obj)
	if err == nil {
		s.lastWriteTime.Store(time.Now().UnixMilli())
	}
	return err
}

// SlaveConfig holds slave configuration loaded from YAML
type SlaveConfig struct {
	Name        string   `yaml:"name"`
	MasterHost  string   `yaml:"master_host"`
	MasterPort  int      `yaml:"master_port"`
	Roots       []string `yaml:"roots"` // e.g. ["/data/site", "/data2/site"]
	PasvPortMin int      `yaml:"pasv_port_min"`
	PasvPortMax int      `yaml:"pasv_port_max"`
	TLSEnabled  bool     `yaml:"tls_enabled"`
	TLSCert     string   `yaml:"tls_cert"`
	TLSKey      string   `yaml:"tls_key"`
	BindIP      string   `yaml:"bind_ip"`
	Timeout     int      `yaml:"timeout"` // seconds, default 60
}

func NewSlave(cfg SlaveConfig) *Slave {
	timeout := actualTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}
	if len(cfg.Roots) == 0 {
		cfg.Roots = []string{"./site"}
	}
	return &Slave{
		name:        cfg.Name,
		masterHost:  cfg.MasterHost,
		masterPort:  cfg.MasterPort,
		roots:       cfg.Roots,
		pasvPortMin: cfg.PasvPortMin,
		pasvPortMax: cfg.PasvPortMax,
		tlsEnabled:  cfg.TLSEnabled,
		tlsCert:     cfg.TLSCert,
		tlsKey:      cfg.TLSKey,
		bindIP:      cfg.BindIP,
		timeout:     timeout,
	}
}

// Boot connects to master, performs handshake, sends disk status, then enters command loop.
// Boot connects to master with auto-reconnect.
func (s *Slave) Boot() error {
	for {
		err := s.connectAndRun()
		if err != nil {
			log.Printf("[Slave] Disconnected: %v", err)
		}
		s.online.Store(false)
		log.Printf("[Slave] Reconnecting to master in 10 seconds...")
		time.Sleep(10 * time.Second)
	}
}

// connectAndRun connects to master, registers, and enters command loop.
func (s *Slave) connectAndRun() error {
	log.Printf("[Slave] %s connecting to master at %s:%d", s.name, s.masterHost, s.masterPort)

	addr := fmt.Sprintf("%s:%d", s.masterHost, s.masterPort)

	var conn net.Conn
	var err error

	if s.tlsEnabled {
		tlsCfg := &tls.Config{InsecureSkipVerify: true}
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: socketTimeout}, "tcp", addr, tlsCfg)
	} else {
		conn, err = net.DialTimeout("tcp", addr, socketTimeout)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to master: %w", err)
	}

	s.conn = conn
	s.stream = protocol.NewObjectStream(conn)
	log.Printf("[Slave] Connected to master")

	// Send slave name
	if err := s.writeObject(s.name); err != nil {
		s.conn.Close()
		return fmt.Errorf("failed to send slave name: %w", err)
	}

	// Send initial disk status
	ds := s.getDiskStatus()
	if err := s.writeObject(&protocol.AsyncResponseDiskStatus{Status: ds}); err != nil {
		s.conn.Close()
		return fmt.Errorf("failed to send disk status: %w", err)
	}

	s.online.Store(true)
	log.Printf("[Slave] Registered as '%s', entering command loop", s.name)

	// Block on command loop
	return s.listenForCommands()
}

// listenForCommands reads AsyncCommand objects from master and dispatches them.
// ().
func (s *Slave) listenForCommands() error {
	lastActivity := time.Now()

	for s.online.Load() {
		// Set read deadline for socket timeout detection
		s.conn.SetReadDeadline(time.Now().Add(socketTimeout))

		obj, err := s.stream.ReadObject()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Check if we've exceeded actual timeout
				// But NOT during remerge — the master won't send commands
				// while we're flooding it with remerge data
				if time.Since(lastActivity) > s.timeout {
					// Check if any goroutine has been writing recently
					// (remerge writes keep the connection alive)
					if s.lastWriteTime.Load() > 0 {
						lastWrite := time.UnixMilli(s.lastWriteTime.Load())
						if time.Since(lastWrite) < s.timeout {
							// Writes are happening, connection is alive
							continue
						}
					}
					log.Printf("[Slave] No communication from master in %v, going offline", time.Since(lastActivity))
					s.shutdown()
					return fmt.Errorf("master timeout")
				}
				continue
			}
			log.Printf("[Slave] Error reading from master: %v", err)
			s.shutdown()
			return fmt.Errorf("lost connection to master: %w", err)
		}

		ac, ok := obj.(*protocol.AsyncCommand)
		if ac == nil || !ok {
			log.Printf("[Slave] Received unexpected object type: %T", obj)
			continue
		}

		lastActivity = time.Now()
		log.Printf("[Slave] Received command: %s", ac)

		// Handle command in its own goroutine ( thread)
		go func(cmd *protocol.AsyncCommand) {
			resp := s.handleCommand(cmd)
			if resp != nil {
				if err := s.writeObject(resp); err != nil {
					log.Printf("[Slave] Error sending response for %s: %v", cmd.Name, err)
				}
			}
		}(ac)
	}

	return nil
}

// handleCommand dispatches a command to the appropriate handler.
//  -> BasicHandler.handleXxx
func (s *Slave) handleCommand(ac *protocol.AsyncCommand) interface{} {
	switch ac.Name {
	case "ping":
		return &protocol.AsyncResponse{Index: ac.Index}

	case "delete":
		return s.handleDelete(ac)

	case "rename":
		return s.handleRename(ac)

	case "chmod":
		return s.handleChmod(ac)

	case "symlink":
		return s.handleSymlink(ac)

	case "checksum":
		return s.handleChecksum(ac)

	case "listen":
		return s.handleListen(ac)

	case "connect":
		return s.handleConnect(ac)

	case "receive":
		return s.handleReceive(ac)

	case "send":
		return s.handleSend(ac)

	case "abort":
		return s.handleAbort(ac)

	case "remerge":
		return s.handleRemerge(ac)

	case "sfvFile":
		return s.handleSFVFile(ac)

	case "readFile":
		return s.handleReadFile(ac)

	case "writeFile":
		return s.handleWriteFile(ac)
		
	case "makedir":
		return s.handleMakeDir(ac)

	case "remergePause":
		return &protocol.AsyncResponse{Index: ac.Index}

	case "remergeResume":
		return &protocol.AsyncResponse{Index: ac.Index}

	case "checkSSL":
		return &protocol.AsyncResponseSSLCheck{Index: ac.Index, SSLReady: s.tlsEnabled}

	case "maxpath":
		return &protocol.AsyncResponseMaxPath{Index: ac.Index, MaxPath: 4096}

	case "shutdown":
		log.Printf("[Slave] Master requested shutdown")
		s.shutdown()
		return nil

	default:
		log.Printf("[Slave] Unknown command: %s", ac.Name)
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "unknown command: " + ac.Name}
	}
}

// --- Command Handlers ---

func (s *Slave) handleDelete(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "delete: missing path"}
	}
	path := ac.Args[0]

	for _, root := range s.roots {
		fullPath := filepath.Join(root, path)
		if info, err := os.Stat(fullPath); err == nil {
			if info.IsDir() {
				os.RemoveAll(fullPath)
			} else {
				os.Remove(fullPath)
			}
			// Clean up empty parent dirs ()
			s.cleanEmptyParents(fullPath, root)
		}
	}

	// Report updated disk status
	ds := s.getDiskStatus()
	s.writeObject(&protocol.AsyncResponseDiskStatus{Status: ds})

	return &protocol.AsyncResponse{Index: ac.Index}
}

func (s *Slave) handleRename(ac *protocol.AsyncCommand) interface{} {
	// Args: [from, toDir, toName] 
	if len(ac.Args) < 3 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "rename: need from, toDir, toName"}
	}
	from := ac.Args[0]
	toDir := ac.Args[1]
	toName := ac.Args[2]

	for _, root := range s.roots {
		fromPath := filepath.Join(root, from)
		if _, err := os.Stat(fromPath); err != nil {
			continue
		}
		toDirPath := filepath.Join(root, toDir)
		toPath := filepath.Join(toDirPath, toName)

		os.MkdirAll(toDirPath, 0755)

		if err := os.Rename(fromPath, toPath); err != nil {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("rename failed: %v", err)}
		}
	}

	return &protocol.AsyncResponse{Index: ac.Index}
}

func (s *Slave) handleChmod(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 2 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "chmod: need path and mode"}
	}
	mode64, err := strconv.ParseUint(ac.Args[1], 8, 32)
	if err != nil {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "chmod: invalid mode"}
	}
	changed := false
	for _, root := range s.roots {
		fullPath := filepath.Join(root, ac.Args[0])
		if _, err := os.Lstat(fullPath); err != nil {
			continue
		}
		if err := os.Chmod(fullPath, os.FileMode(mode64)); err != nil {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("chmod failed: %v", err)}
		}
		changed = true
	}
	if !changed {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "chmod: path not found"}
	}
	return &protocol.AsyncResponse{Index: ac.Index}
}

func (s *Slave) handleSymlink(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 2 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "symlink: need link and target"}
	}
	linkPath := ac.Args[0]
	targetPath := ac.Args[1]
	for _, root := range s.roots {
		fullLink := filepath.Join(root, linkPath)
		fullTarget := filepath.Join(root, targetPath)
		if _, err := os.Stat(fullTarget); err != nil {
			continue
		}
		_ = os.Remove(fullLink)
		if err := os.Symlink(targetPath, fullLink); err != nil {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("symlink failed: %v", err)}
		}
		return &protocol.AsyncResponse{Index: ac.Index}
	}
	return &protocol.AsyncResponseError{Index: ac.Index, Message: "symlink: target not found"}
}

// handleMakeDir physically creates empty directories on the slave disk.
func (s *Slave) handleMakeDir(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "makedir: missing path"}
	}
	dirPath := ac.Args[0]

	if len(s.roots) == 0 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "no roots available"}
	}

	// Always create empty folders (like banners) on the first root
	fullPath := filepath.Join(s.roots[0], dirPath)
	if err := os.MkdirAll(fullPath, 0755); err != nil {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("makedir failed: %v", err)}
	}

	log.Printf("[Slave] Created directory %s", dirPath)
	return &protocol.AsyncResponse{Index: ac.Index}
}

func (s *Slave) handleChecksum(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "checksum: missing path"}
	}
	path := ac.Args[0]

	for _, root := range s.roots {
		fullPath := filepath.Join(root, path)
		f, err := os.Open(fullPath)
		if err != nil {
			continue
		}
		defer f.Close()

		h := crc32.NewIEEE()
		if _, err := io.Copy(h, f); err != nil {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("checksum error: %v", err)}
		}
		return &protocol.AsyncResponseChecksum{Index: ac.Index, Checksum: h.Sum32()}
	}

	return &protocol.AsyncResponseError{Index: ac.Index, Message: "file not found: " + path}
}

// handleListen - slave opens a passive port and waits for a connection.
// 
func (s *Slave) handleListen(ac *protocol.AsyncCommand) interface{} {
	listener, err := s.listenOnPortRange()
	if err != nil {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("listen failed: %v", err)}
	}

	idx := atomic.AddInt32(&s.nextTransferIdx, 1)
	t := NewTransfer(listener, nil, idx, s)
	s.transfers.Store(idx, t)

	port := listener.Addr().(*net.TCPAddr).Port

	return &protocol.AsyncResponseTransfer{
		Index: ac.Index,
		Info: protocol.ConnectInfo{
			Port:          port,
			TransferIndex: idx,
		},
	}
}

// handleConnect - slave connects out to a given address (active mode).
// 
func (s *Slave) handleConnect(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "connect: missing address"}
	}
	// Args[0] = "ip:port", Args[1] = "encrypted", Args[2] = "sslClientMode"
	address := ac.Args[0]

	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("connect failed: %v", err)}
	}

	// TLS wrap if encrypted flag is set (for FXP)
	// Args[2] = sslClientHandshake: "true" = slave acts as TLS CLIENT, "false" = slave acts as TLS SERVER
	var finalConn net.Conn = conn
	if len(ac.Args) > 1 && ac.Args[1] == "true" {
		sslClientMode := len(ac.Args) > 2 && ac.Args[2] == "true"
		if sslClientMode {
			// Slave acts as TLS CLIENT (normal outbound)
			tlsConfig := &tls.Config{
				InsecureSkipVerify: true,
			}
			tlsConn := tls.Client(conn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("TLS client handshake failed: %v", err)}
			}
			finalConn = tlsConn
			log.Printf("[Slave] TLS client connect to %s successful", address)
		} else {
			// Slave acts as TLS SERVER (CPSV FXP — other site is TLS client)
			cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
			if err != nil {
				conn.Close()
				return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("load TLS cert: %v", err)}
			}
			tlsConfig := &tls.Config{
				Certificates: []tls.Certificate{cert},
			}
			tlsConn := tls.Server(conn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("TLS server handshake failed: %v", err)}
			}
			finalConn = tlsConn
			log.Printf("[Slave] TLS server connect to %s successful", address)
		}
	}

	idx := atomic.AddInt32(&s.nextTransferIdx, 1)
	port := conn.RemoteAddr().(*net.TCPAddr).Port
	t := NewTransfer(nil, finalConn, idx, s)
	s.transfers.Store(idx, t)

	return &protocol.AsyncResponseTransfer{
		Index: ac.Index,
		Info: protocol.ConnectInfo{
			Port:          port,
			TransferIndex: idx,
		},
	}
}

// handleReceive - slave receives (uploads) a file from the FTP client via the data connection.
// 
// Args: [type, position, transferIndex, inetAddress, path, minSpeed, maxSpeed]
func (s *Slave) handleReceive(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 5 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "receive: not enough args"}
	}

	var transferIdx int32
	fmt.Sscanf(ac.Args[2], "%d", &transferIdx)
	path := ac.Args[4]

	val, ok := s.transfers.Load(transferIdx)
	if !ok {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "transfer not found"}
	}
	t := val.(*Transfer)

	// Acknowledge to master that we're starting (: sendResponse(new AsyncResponse(ac.getIndex())))
	s.writeObject(&protocol.AsyncResponse{Index: ac.Index})

	// Actually receive the file - this blocks until done
	status := t.ReceiveFile(path)
	return &protocol.AsyncResponseTransferStatus{Status: status}
}

// handleSend - slave sends (downloads) a file to the FTP client via the data connection.
// 
// Args: [type, position, transferIndex, inetAddress, path, minSpeed, maxSpeed]
func (s *Slave) handleSend(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 5 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "send: not enough args"}
	}

	var transferIdx int32
	fmt.Sscanf(ac.Args[2], "%d", &transferIdx)
	path := ac.Args[4]

	val, ok := s.transfers.Load(transferIdx)
	if !ok {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "transfer not found"}
	}
	t := val.(*Transfer)

	// Acknowledge to master
	s.writeObject(&protocol.AsyncResponse{Index: ac.Index})

	// Actually send the file
	status := t.SendFile(path)
	return &protocol.AsyncResponseTransferStatus{Status: status}
}

func (s *Slave) handleAbort(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return nil
	}
	var transferIdx int32
	fmt.Sscanf(ac.Args[0], "%d", &transferIdx)

	reason := "aborted by master"
	if len(ac.Args) > 1 {
		reason = ac.Args[1]
	}

	if val, ok := s.transfers.Load(transferIdx); ok {
		t := val.(*Transfer)
		t.Abort(reason)
	}

	return &protocol.AsyncResponse{Index: ac.Index}
}

// handleRemerge - slave scans its roots and sends all files to master.
// 
func (s *Slave) handleRemerge(ac *protocol.AsyncCommand) interface{} {
	basePath := "/"
	if len(ac.Args) > 0 {
		basePath = ac.Args[0]
	}

	log.Printf("[Slave] Starting remerge from %s across %d roots", basePath, len(s.roots))

	totalFiles := 0
	totalDirs := 0
	sentDirs := 0

	for _, root := range s.roots {
		scanRoot := filepath.Join(root, basePath)

		// Check if scanRoot exists
		if _, err := os.Stat(scanRoot); err != nil {
			log.Printf("[Slave] Remerge: root %s does not exist, skipping", scanRoot)
			continue
		}

		log.Printf("[Slave] Remerge: scanning root %s", scanRoot)

		// Stream directory-by-directory: collect files per dir, send when dir changes
		currentDir := ""
		var currentFiles []protocol.LightRemoteInode

		sendCurrentDir := func() {
			if currentDir == "" || len(currentFiles) == 0 {
				return
			}
			resp := &protocol.AsyncResponseRemerge{
				Path:         currentDir,
				Files:        currentFiles,
				LastModified: time.Now().Unix(),
			}
			if err := s.writeObject(resp); err != nil {
				log.Printf("[Slave] Error sending remerge for %s: %v", currentDir, err)
			}
			sentDirs++
			currentFiles = nil
		}

		// Walk the root - list each directory's direct children
		filepath.Walk(scanRoot, func(fullPath string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}

			// Skip the root itself
			if fullPath == scanRoot {
				return nil
			}

			// Get VFS-relative path
			relPath, _ := filepath.Rel(root, fullPath)
			relPath = "/" + filepath.ToSlash(relPath)

			// Parent dir in VFS
			parentDir := filepath.ToSlash(filepath.Dir(relPath))
			if parentDir == "." {
				parentDir = "/"
			}

			// If we moved to a new directory, send the previous one
			if parentDir != currentDir {
				sendCurrentDir()
				currentDir = parentDir
			}

			currentFiles = append(currentFiles, protocol.LightRemoteInode{
				Name:         info.Name(),
				IsDir:        info.IsDir(),
				IsSymlink:    info.Mode()&os.ModeSymlink != 0,
				Size:         info.Size(),
				LastModified: info.ModTime().Unix(),
				Owner:        getFileOwner(info),
				Group:        getFileGroup(info),
			})
			if info.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Readlink(fullPath); err == nil {
					currentFiles[len(currentFiles)-1].LinkTarget = filepath.ToSlash(target)
				}
			}

			if info.IsDir() {
				totalDirs++
			} else {
				totalFiles++
			}
			return nil
		})

		// Send last directory
		sendCurrentDir()

		log.Printf("[Slave] Remerge root %s done: sent %d directories", root, sentDirs)
	}

	log.Printf("[Slave] Remerge complete: %d files, %d dirs across %d sent directories", totalFiles, totalDirs, sentDirs)
	return &protocol.AsyncResponse{Index: ac.Index}
}

// handleSFVFile - slave parses an SFV file and sends the entries to master.
// 
func (s *Slave) handleSFVFile(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "sfvFile: missing path"}
	}
	sfvPath := ac.Args[0]

	for _, root := range s.roots {
		fullPath := filepath.Join(root, sfvPath)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		// Parse SFV: each line is "filename HEXCRC32"
		var entries []protocol.SFVEntry
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ";") {
				continue
			}
			// Find last space separator
			sep := strings.LastIndex(line, " ")
			if sep < 0 {
				continue
			}
			fileName := strings.TrimSpace(line[:sep])
			crcStr := strings.TrimSpace(line[sep+1:])
			crc, err := strconv.ParseUint(crcStr, 16, 32)
			if err != nil {
				continue
			}
			entries = append(entries, protocol.SFVEntry{
				FileName: fileName,
				CRC32:    uint32(crc),
			})
		}

		// Calculate CRC32 of the SFV file itself
		h := crc32.NewIEEE()
		h.Write(data)

		log.Printf("[Slave] Parsed SFV %s: %d entries", sfvPath, len(entries))
		return &protocol.AsyncResponseSFVInfo{
			Index:    ac.Index,
			SFVName:  filepath.Base(sfvPath),
			Entries:  entries,
			Checksum: h.Sum32(),
		}
	}

	return &protocol.AsyncResponseError{Index: ac.Index, Message: "SFV not found: " + sfvPath}
}

// handleReadFile - slave reads a small file and sends content to master.
// Used for .message, .imdb, .nfo display without a full transfer setup.
func (s *Slave) handleReadFile(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "readFile: missing path"}
	}
	filePath := ac.Args[0]

	for _, root := range s.roots {
		fullPath := filepath.Join(root, filePath)
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		// Safety: don't read files larger than 64KB
		if info.Size() > 65536 {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: "file too large for readFile"}
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("read error: %v", err)}
		}
		return &protocol.AsyncResponseFileContent{
			Index:   ac.Index,
			Content: data,
		}
	}

	return &protocol.AsyncResponseError{Index: ac.Index, Message: "file not found: " + filePath}
}

// handleWriteFile - master writes a small file to slave (e.g. .message).
func (s *Slave) handleWriteFile(ac *protocol.AsyncCommand) interface{} {
	if len(ac.Args) < 1 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "writeFile: missing path"}
	}
	filePath := ac.Args[0]

	// Content is in Args[1] (base64 would be safer but for small text files this works)
	var content []byte
	if len(ac.Args) > 1 {
		content = []byte(ac.Args[1])
	}

	// Write to first root
	if len(s.roots) == 0 {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: "no roots"}
	}

	fullPath := filepath.Join(s.roots[0], filePath)
	os.MkdirAll(filepath.Dir(fullPath), 0755)

	if err := os.WriteFile(fullPath, content, 0644); err != nil {
		return &protocol.AsyncResponseError{Index: ac.Index, Message: fmt.Sprintf("write error: %v", err)}
	}

	log.Printf("[Slave] Wrote file %s (%d bytes)", filePath, len(content))
	return &protocol.AsyncResponse{Index: ac.Index}
}

// --- Utility methods ---

func (s *Slave) getDiskStatus() protocol.DiskStatus {
	var totalAvail, totalCap int64
	for _, root := range s.roots {
		avail, cap := getDiskSpace(root)
		totalAvail += avail
		totalCap += cap
	}
	return protocol.DiskStatus{
		SpaceAvailable: totalAvail,
		SpaceCapacity:  totalCap,
	}
}

// getFileFromRoots finds a file across all roots, returns the full filesystem path.
func (s *Slave) getFileFromRoots(relPath string) (string, error) {
	for _, root := range s.roots {
		fullPath := filepath.Join(root, relPath)
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath, nil
		}
	}
	return "", fmt.Errorf("file not found: %s", relPath)
}

// getDirForUpload selects a root for a new upload (picks root with most free space).
func (s *Slave) getDirForUpload(relPath string) (string, error) {
	var bestRoot string
	var bestAvail int64

	for _, root := range s.roots {
		avail, _ := getDiskSpace(root)
		if avail > bestAvail {
			bestAvail = avail
			bestRoot = root
		}
	}

	if bestRoot == "" {
		return "", fmt.Errorf("no root available")
	}

	dirPart := filepath.Dir(relPath)
	fullDir := filepath.Join(bestRoot, dirPart)
	os.MkdirAll(fullDir, 0755)

	return filepath.Join(bestRoot, relPath), nil
}

func (s *Slave) listenOnPortRange() (net.Listener, error) {
	if s.pasvPortMin > 0 && s.pasvPortMax > s.pasvPortMin {
		for port := s.pasvPortMin; port <= s.pasvPortMax; port++ {
			bindAddr := fmt.Sprintf("%s:%d", s.bindIP, port)
			l, err := net.Listen("tcp", bindAddr)
			if err == nil {
				return l, nil
			}
		}
		return nil, fmt.Errorf("no available port in range %d-%d", s.pasvPortMin, s.pasvPortMax)
	}
	// Random port
	return net.Listen("tcp", s.bindIP+":0")
}

func (s *Slave) removeTransfer(idx int32) {
	s.transfers.Delete(idx)
}

func (s *Slave) cleanEmptyParents(path string, root string) {
	dir := filepath.Dir(path)
	for dir != root && len(dir) > len(root) {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		os.Remove(dir)
		dir = filepath.Dir(dir)
	}
}

func (s *Slave) shutdown() {
	log.Printf("[Slave] Shutting down")
	s.online.Store(false)
	if s.conn != nil {
		s.conn.Close()
	}
}

func (s *Slave) GetRoots() []string {
	return s.roots
}

func (s *Slave) GetStream() *protocol.ObjectStream {
	return s.stream
}
