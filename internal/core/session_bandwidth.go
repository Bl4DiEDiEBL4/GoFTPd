package core

import (
	"net"
	"time"
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
	s.TransferBytes = 0
	s.TransferStartedAt = time.Now()
	s.TransferSlaveName = slaveName
	s.TransferSlaveIdx = slaveIdx
	s.stateMu.Unlock()
}

func (s *Session) addTransferBytes(n int64) {
	if s == nil || n <= 0 {
		return
	}
	s.stateMu.Lock()
	s.TransferBytes += n
	s.stateMu.Unlock()
}

func (s *Session) endTransfer() {
	if s == nil {
		return
	}
	s.stateMu.Lock()
	s.TransferDirection = ""
	s.TransferPath = ""
	s.TransferBytes = 0
	s.TransferStartedAt = time.Time{}
	s.TransferSlaveName = ""
	s.TransferSlaveIdx = 0
	s.stateMu.Unlock()
}

func (s *Session) currentTransferSpeedBytes() float64 {
	if s == nil {
		return 0
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if s.TransferDirection == "" || s.TransferStartedAt.IsZero() || s.TransferBytes <= 0 {
		return 0
	}
	seconds := time.Since(s.TransferStartedAt).Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(s.TransferBytes) / seconds
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

func trackTransferConn(s *Session, conn net.Conn, direction string) net.Conn {
	if s == nil || conn == nil || direction == "" {
		return conn
	}
	return &bandwidthTrackingConn{Conn: conn, session: s, direction: direction}
}
