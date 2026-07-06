package core

import (
	"io"
	"net"
	"time"

	"weaveftpd/internal/metrics"
)

func (s *Session) touchActivity() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.LastCommandAt = time.Now()
	s.stateMu.Unlock()
}

func (s *Session) beginTransfer(direction, targetPath string) {
	s.beginTransferOnSlave(direction, targetPath, "", 0)
}

func (s *Session) beginTransferOnSlave(direction, targetPath, slaveName string, slaveIdx int32) {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.TransferDirection = direction
	s.TransferPath = targetPath
	s.TransferBytes.Store(0)
	s.TransferStartedAt = time.Now()
	s.TransferSlaveName = slaveName
	s.TransferSlaveIdx = slaveIdx
	s.stateMu.Unlock()
	metrics.TransferBegin(direction)
}

func (s *Session) attachTransferToSlave(slaveName string, slaveIdx int32) {
	if s == nil || slaveName == "" || slaveIdx == 0 {
		return
	}
	s.stateMu.Lock()
	s.TransferSlaveName = slaveName
	s.TransferSlaveIdx = slaveIdx
	s.stateMu.Unlock()
}

func (s *Session) addTransferBytes(n int64) {
	if s == nil || n <= 0 {
		return
	}
	s.TransferBytes.Add(n)
}

func (s *Session) endTransfer() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	dir := s.TransferDirection
	startedAt := s.TransferStartedAt
	bytes := s.TransferBytes.Load()
	s.TransferDirection = ""
	s.TransferPath = ""
	s.TransferBytes.Store(0)
	s.TransferStartedAt = time.Time{}
	s.TransferSlaveName = ""
	s.TransferSlaveIdx = 0
	s.stateMu.Unlock()
	// Several upload paths call endTransfer explicitly and again via defer.
	// Record metrics only on the first call (when a transfer was actually
	// active) so the gauge and latency aren't double-counted.
	if dir != "" && !startedAt.IsZero() {
		metrics.TransferEnd(dir, time.Since(startedAt), bytes)
	}
}

func (s *Session) currentTransferSpeedBytes() float64 {
	if s == nil {
		return 0
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	bytes := s.TransferBytes.Load()
	if s.TransferDirection == "" || s.TransferStartedAt.IsZero() || bytes <= 0 {
		return 0
	}
	seconds := time.Since(s.TransferStartedAt).Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(bytes) / seconds
}

type bandwidthTrackingConn struct {
	net.Conn
	session   *Session
	direction string
}

func (c *bandwidthTrackingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.direction == "upload" {
		c.session.addTransferBytes(int64(n))
	}
	return n, err
}

func (c *bandwidthTrackingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 && c.direction == "download" {
		c.session.addTransferBytes(int64(n))
	}
	return n, err
}

func (c *bandwidthTrackingConn) ReadFrom(r io.Reader) (int64, error) {
	if rf, ok := c.Conn.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(r)
		if n > 0 && c.direction == "download" {
			c.session.addTransferBytes(n)
		}
		return n, err
	}
	return copyTransferDataLoop(c, r)
}

func (c *bandwidthTrackingConn) WriteTo(w io.Writer) (int64, error) {
	if wt, ok := c.Conn.(io.WriterTo); ok {
		n, err := wt.WriteTo(w)
		if n > 0 && c.direction == "upload" {
			c.session.addTransferBytes(n)
		}
		return n, err
	}
	return copyTransferDataLoop(w, c)
}

func trackTransferConn(s *Session, conn net.Conn, direction string) net.Conn {
	if s == nil || conn == nil || direction == "" {
		return conn
	}
	return &bandwidthTrackingConn{Conn: conn, session: s, direction: direction}
}
