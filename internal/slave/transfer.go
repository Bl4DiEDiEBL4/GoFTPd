package slave

import (
	"crypto/tls"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"goftpd/internal/protocol"
)

const (
	TransferUnknown   = 'U'
	TransferReceiving = 'R' // upload from client to slave
	TransferSending   = 'S' // download from slave to client
)

// Transfer represents a data transfer on the slave side.
type Transfer struct {
	listener      net.Listener // non-nil for passive (LISTEN)
	conn          net.Conn     // non-nil for active (CONNECT) or after passive accept
	transferIndex int32
	slave         *Slave
	encrypted     bool
	sslClientMode bool

	path        string
	direction   byte
	started     time.Time
	finished    time.Time
	transferred int64
	checksum    uint32
	abortReason string
	mu          sync.Mutex
}

func NewTransfer(listener net.Listener, conn net.Conn, idx int32, slave *Slave, encrypted bool, sslClientMode bool) *Transfer {
	return &Transfer{
		listener:      listener,
		conn:          conn,
		transferIndex: idx,
		slave:         slave,
		encrypted:     encrypted,
		sslClientMode: sslClientMode,
		direction:     TransferUnknown,
	}
}

func (t *Transfer) SetPath(path string) {
	t.mu.Lock()
	t.path = path
	t.mu.Unlock()
}

func (t *Transfer) Path() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.path
}

func (t *Transfer) SnapshotLiveStat() protocol.TransferLiveStat {
	t.mu.Lock()
	defer t.mu.Unlock()

	var startedUnixMs int64
	var speedBytes int64
	if !t.started.IsZero() {
		startedUnixMs = t.started.UnixMilli()
		elapsedMs := time.Since(t.started).Milliseconds()
		if elapsedMs > 0 && t.transferred > 0 {
			speedBytes = t.transferred * 1000 / elapsedMs
		}
	}

	return protocol.TransferLiveStat{
		TransferIndex: t.transferIndex,
		Direction:     t.direction,
		Path:          t.path,
		StartedUnixMs: startedUnixMs,
		Transferred:   t.transferred,
		SpeedBytes:    speedBytes,
	}
}

// ReceiveFile receives data from the FTP client and writes to disk.
// ().
func (t *Transfer) ReceiveFile(path string, position int64) protocol.TransferStatus {
	t.mu.Lock()
	t.direction = TransferReceiving
	t.started = time.Now()
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.finished = time.Now()
		t.mu.Unlock()
		t.slave.removeTransfer(t.transferIndex)
	}()

	if err := t.acceptPassiveConn(); err != nil {
		return t.errorStatus(err.Error())
	}

	if t.conn == nil {
		return t.errorStatus("no connection")
	}
	defer t.conn.Close()

	// Determine where to write
	fullPath, err := t.slave.getDirForUpload(path)
	if err != nil {
		return t.errorStatus(fmt.Sprintf("cannot create file: %v", err))
	}

	var file *os.File
	if position > 0 {
		file, err = os.OpenFile(fullPath, os.O_WRONLY, 0644)
		if err != nil {
			return t.errorStatus(fmt.Sprintf("resume open failed: %v", err))
		}
		if _, err := file.Seek(position, io.SeekStart); err != nil {
			file.Close()
			return t.errorStatus(fmt.Sprintf("resume seek failed: %v", err))
		}
	} else {
		file, err = os.Create(fullPath)
	}
	if err != nil {
		return t.errorStatus(fmt.Sprintf("create failed: %v", err))
	}
	defer file.Close()

	// Transfer with CRC32
	h := crc32.NewIEEE()
	w := io.MultiWriter(file, h)
	buf := make([]byte, 65536)

	for {
		if t.abortReason != "" {
			os.Remove(fullPath)
			return t.errorStatus("aborted: " + t.abortReason)
		}

		n, err := t.conn.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			t.mu.Lock()
			t.transferred += int64(n)
			t.mu.Unlock()
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			os.Remove(fullPath)
			return t.errorStatus(fmt.Sprintf("read error: %v", err))
		}
	}

	log.Printf("[Transfer] Received %s (%d bytes, CRC32=%08X, offset=%d)", path, t.transferred, h.Sum32(), position)

	return protocol.TransferStatus{
		TransferIndex: t.transferIndex,
		Elapsed:       time.Since(t.started).Milliseconds(),
		Transferred:   t.transferred,
		Checksum:      h.Sum32(),
		Finished:      true,
	}
}

// SendFile sends a file from disk to the FTP client.
// ().
func (t *Transfer) SendFile(path string, position int64) protocol.TransferStatus {
	t.mu.Lock()
	t.direction = TransferSending
	t.started = time.Now()
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		t.finished = time.Now()
		t.mu.Unlock()
		t.slave.removeTransfer(t.transferIndex)
	}()

	if err := t.acceptPassiveConn(); err != nil {
		return t.errorStatus(err.Error())
	}

	if t.conn == nil {
		return t.errorStatus("no connection")
	}
	defer t.conn.Close()

	// Find the file across roots
	fullPath, err := t.slave.getFileFromRoots(path)
	if err != nil {
		return t.errorStatus(fmt.Sprintf("file not found: %v", err))
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return t.errorStatus(fmt.Sprintf("open failed: %v", err))
	}
	defer file.Close()
	if position > 0 {
		if _, err := file.Seek(position, io.SeekStart); err != nil {
			return t.errorStatus(fmt.Sprintf("resume seek failed: %v", err))
		}
	}

	// Transfer with CRC32
	h := crc32.NewIEEE()
	r := io.TeeReader(file, h)
	buf := make([]byte, 65536)

	for {
		if t.abortReason != "" {
			return t.errorStatus("aborted: " + t.abortReason)
		}

		n, err := r.Read(buf)
		if n > 0 {
			_, werr := t.conn.Write(buf[:n])
			if werr != nil {
				return t.errorStatus(fmt.Sprintf("write error: %v", werr))
			}
			t.mu.Lock()
			t.transferred += int64(n)
			t.mu.Unlock()
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return t.errorStatus(fmt.Sprintf("read error: %v", err))
		}
	}

	log.Printf("[Transfer] Sent %s (%d bytes, CRC32=%08X, offset=%d)", path, t.transferred, h.Sum32(), position)

	return protocol.TransferStatus{
		TransferIndex: t.transferIndex,
		Elapsed:       time.Since(t.started).Milliseconds(),
		Transferred:   t.transferred,
		Checksum:      h.Sum32(),
		Finished:      true,
	}
}

func (t *Transfer) acceptPassiveConn() error {
	if t.conn != nil || t.listener == nil {
		return nil
	}
	if deadlineListener, ok := t.listener.(interface{ SetDeadline(time.Time) error }); ok {
		deadlineListener.SetDeadline(time.Now().Add(30 * time.Second))
	}
	conn, err := t.listener.Accept()
	t.listener.Close()
	if err != nil {
		return fmt.Errorf("accept failed: %v", err)
	}
	if !t.encrypted {
		t.conn = conn
		return nil
	}

	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetDeadline(time.Time{})

	if t.sslClientMode {
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return fmt.Errorf("TLS client handshake failed: %v", err)
		}
		t.conn = tlsConn
		return nil
	}

	cert, err := tls.LoadX509KeyPair(t.slave.tlsCert, t.slave.tlsKey)
	if err != nil {
		conn.Close()
		return fmt.Errorf("load TLS cert: %v", err)
	}
	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return fmt.Errorf("TLS server handshake failed: %v", err)
	}
	t.conn = tlsConn
	return nil
}

func (t *Transfer) Abort(reason string) {
	t.mu.Lock()
	t.abortReason = reason
	t.mu.Unlock()
	if t.conn != nil {
		t.conn.Close()
	}
	if t.listener != nil {
		t.listener.Close()
	}
}

func (t *Transfer) errorStatus(msg string) protocol.TransferStatus {
	return protocol.TransferStatus{
		TransferIndex: t.transferIndex,
		Elapsed:       time.Since(t.started).Milliseconds(),
		Transferred:   t.transferred,
		Finished:      true,
		Error:         msg,
	}
}
