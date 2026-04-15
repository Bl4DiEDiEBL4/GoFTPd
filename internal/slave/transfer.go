package slave

import (
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
// 
type Transfer struct {
	listener      net.Listener // non-nil for passive (LISTEN)
	conn          net.Conn     // non-nil for active (CONNECT) or after passive accept
	transferIndex int32
	slave         *Slave

	direction     byte
	started       time.Time
	finished      time.Time
	transferred   int64
	checksum      uint32
	abortReason   string
	mu            sync.Mutex
}

func NewTransfer(listener net.Listener, conn net.Conn, idx int32, slave *Slave) *Transfer {
	return &Transfer{
		listener:      listener,
		conn:          conn,
		transferIndex: idx,
		slave:         slave,
		direction:     TransferUnknown,
	}
}

// ReceiveFile receives data from the FTP client and writes to disk.
// ().
func (t *Transfer) ReceiveFile(path string) protocol.TransferStatus {
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

	// Accept connection if passive
	if t.conn == nil && t.listener != nil {
		t.listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
		conn, err := t.listener.Accept()
		t.listener.Close()
		if err != nil {
			return t.errorStatus(fmt.Sprintf("accept failed: %v", err))
		}
		t.conn = conn
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

	file, err := os.Create(fullPath)
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

	log.Printf("[Transfer] Received %s (%d bytes, CRC32=%08X)", path, t.transferred, h.Sum32())

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
func (t *Transfer) SendFile(path string) protocol.TransferStatus {
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

	// Accept connection if passive
	if t.conn == nil && t.listener != nil {
		t.listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
		conn, err := t.listener.Accept()
		t.listener.Close()
		if err != nil {
			return t.errorStatus(fmt.Sprintf("accept failed: %v", err))
		}
		t.conn = conn
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

	log.Printf("[Transfer] Sent %s (%d bytes, CRC32=%08X)", path, t.transferred, h.Sum32())

	return protocol.TransferStatus{
		TransferIndex: t.transferIndex,
		Elapsed:       time.Since(t.started).Milliseconds(),
		Transferred:   t.transferred,
		Checksum:      h.Sum32(),
		Finished:      true,
	}
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
