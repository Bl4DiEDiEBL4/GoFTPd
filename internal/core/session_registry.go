package core

import (
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/acl"
)

type sessionSnapshot struct {
	ID                uint64
	User              string
	Flags             string
	Remote            string
	CurrentDir        string
	StartedAt         time.Time
	LastCommandAt     time.Time
	LoggedIn          bool
	TransferDirection string
	TransferPath      string
	TransferBytes     int64
	TransferStartedAt time.Time
	TransferSlaveName string
	TransferSlaveIdx  int32
}

var (
	nextSessionID  atomic.Uint64
	activeSessions sync.Map
)

func registerSession(s *Session) uint64 {
	id := nextSessionID.Add(1)
	activeSessions.Store(id, s)
	return id
}

func unregisterSession(id uint64) {
	activeSessions.Delete(id)
}

func listActiveSessions() []sessionSnapshot {
	var out []sessionSnapshot
	activeSessions.Range(func(key, value interface{}) bool {
		s, ok := value.(*Session)
		if !ok || s == nil {
			return true
		}
		snap := sessionSnapshot{
			ID:         s.ID,
			Remote:     remoteAddrString(s.Conn),
			CurrentDir: s.CurrentDir,
			StartedAt:  s.StartedAt,
			LoggedIn:   s.IsLogged,
		}
		s.stateMu.RLock()
		snap.LastCommandAt = s.LastCommandAt
		snap.TransferDirection = s.TransferDirection
		snap.TransferPath = s.TransferPath
		snap.TransferBytes = s.TransferBytes
		snap.TransferStartedAt = s.TransferStartedAt
		snap.TransferSlaveName = s.TransferSlaveName
		snap.TransferSlaveIdx = s.TransferSlaveIdx
		s.stateMu.RUnlock()
		if s.IsLogged && s.User != nil {
			snap.User = s.User.Name
			snap.Flags = s.User.Flags
		}
		out = append(out, snap)
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func kickActiveUser(username string) int {
	username = strings.TrimSpace(username)
	if username == "" {
		return 0
	}
	kicked := 0
	activeSessions.Range(func(key, value interface{}) bool {
		s, ok := value.(*Session)
		if !ok || s == nil || !s.IsLogged || s.User == nil {
			return true
		}
		if strings.EqualFold(s.User.Name, username) {
			_ = s.Conn.Close()
			kicked++
		}
		return true
	})
	return kicked
}

func UpdateActiveSessionACL(engine *acl.Engine) {
	if engine == nil {
		return
	}
	activeSessions.Range(func(key, value interface{}) bool {
		s, ok := value.(*Session)
		if !ok || s == nil {
			return true
		}
		s.ACLEngine = engine
		return true
	})
}

func remoteAddrString(conn net.Conn) string {
	if conn == nil || conn.RemoteAddr() == nil {
		return ""
	}
	return conn.RemoteAddr().String()
}
