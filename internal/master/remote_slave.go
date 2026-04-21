package master

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/protocol"
)

const (
	masterSocketTimeout = 10 * time.Second
	masterActualTimeout = 60 * time.Second
)

// RemoteSlave represents a connected slave as seen from the master.
// 
type RemoteSlave struct {
	name   string
	conn   net.Conn
	stream *protocol.ObjectStream
	writeMu sync.Mutex // protects stream writes

	// Async index pool ( / _indexWithCommands)
	indexPool     chan string
	pendingCmds   sync.Map // index (string) -> chan interface{}
	commandNotify chan struct{}

	// State
	online        atomic.Bool
	available     atomic.Bool
	remerging     atomic.Bool
	diskStatus    protocol.DiskStatus
	diskMu        sync.RWMutex

	// Transfers
	transfers    sync.Map // TransferIndex (int32) -> *RemoteTransfer
	activeCount  atomic.Int32 // number of currently-active uploads (for load balancing)

	// Timing
	lastResponseReceived atomic.Int64
	lastCommandSent      atomic.Int64

	// Properties ()
	properties    map[string]string
	propMu        sync.RWMutex

	// Masks
	masks         []string
	maskMu        sync.RWMutex // [Added] Protects masks from concurrent read/write panics
}

// NewRemoteSlave creates a new remote slave from an accepted connection.
func NewRemoteSlave(name string, conn net.Conn, stream *protocol.ObjectStream) *RemoteSlave {
	rs := &RemoteSlave{
		name:          name,
		conn:          conn,
		stream:        stream,
		indexPool:     make(chan string, 256),
		commandNotify: make(chan struct{}, 256),
		properties:    make(map[string]string),
	}

	// Fill index pool ( 00-ff)
	for i := 0; i < 256; i++ {
		key := fmt.Sprintf("%02x", i)
		rs.indexPool <- key
	}

	rs.lastResponseReceived.Store(time.Now().UnixMilli())
	rs.lastCommandSent.Store(time.Now().UnixMilli())
	rs.online.Store(true)

	return rs
}

func (rs *RemoteSlave) Name() string   { return rs.name }
func (rs *RemoteSlave) IsOnline() bool { return rs.online.Load() }
func (rs *RemoteSlave) IsAvailable() bool { return rs.available.Load() }
func (rs *RemoteSlave) IsRemerging() bool { return rs.remerging.Load() }

// ActiveTransfers returns the current number of in-flight uploads on this slave.
// Used by the load balancer in SelectSlaveForUpload.
func (rs *RemoteSlave) ActiveTransfers() int32 { return rs.activeCount.Load() }
func (rs *RemoteSlave) IncActiveTransfers()    { rs.activeCount.Add(1) }
func (rs *RemoteSlave) DecActiveTransfers() {
	if rs.activeCount.Add(-1) < 0 {
		rs.activeCount.Store(0)
	}
}

func (rs *RemoteSlave) GetDiskStatus() protocol.DiskStatus {
	rs.diskMu.RLock()
	defer rs.diskMu.RUnlock()
	return rs.diskStatus
}

func (rs *RemoteSlave) GetPASVIP() string {
	rs.propMu.RLock()
	addr, ok := rs.properties["pasv_addr"]
	rs.propMu.RUnlock()
	if ok && addr != "" {
		return addr
	}
	if rs.conn != nil {
		host, _, _ := net.SplitHostPort(rs.conn.RemoteAddr().String())
		return host
	}
	return ""
}

func (rs *RemoteSlave) SetProperty(key, value string) {
	rs.propMu.Lock()
	rs.properties[key] = value
	rs.propMu.Unlock()
}

func (rs *RemoteSlave) GetProperty(key, def string) string {
	rs.propMu.RLock()
	defer rs.propMu.RUnlock()
	if v, ok := rs.properties[key]; ok {
		return v
	}
	return def
}

// [Added] GetMasks safely retrieves the masks for this slave
func (rs *RemoteSlave) GetMasks() []string {
	rs.maskMu.RLock()
	defer rs.maskMu.RUnlock()
	cp := make([]string, len(rs.masks))
	copy(cp, rs.masks)
	return cp
}

// [Added] SetMasks safely updates the masks for this slave
func (rs *RemoteSlave) SetMasks(masks []string) {
	rs.maskMu.Lock()
	rs.masks = masks
	rs.maskMu.Unlock()
}

// --- Async Command/Response ( / sendCommand / fetchResponse) ---

// FetchIndex gets a free command index from the pool.
func (rs *RemoteSlave) FetchIndex() (string, error) {
	if !rs.IsOnline() {
		return "", fmt.Errorf("slave offline")
	}
	select {
	case idx := <-rs.indexPool:
		return idx, nil
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("index pool exhausted")
	}
}

// SendCommand sends an async command to the slave. ().
func (rs *RemoteSlave) SendCommand(ac *protocol.AsyncCommand) error {
	if !rs.IsOnline() {
		return fmt.Errorf("slave offline")
	}
	rs.writeMu.Lock()
	err := rs.stream.WriteObject(ac)
	rs.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("send failed: %w", err)
	}
	rs.lastCommandSent.Store(time.Now().UnixMilli())
	return nil
}

// FetchResponse waits for a response with the given index.
// ().
func (rs *RemoteSlave) FetchResponse(index string, timeout time.Duration) (interface{}, error) {
	if timeout == 0 {
		timeout = masterActualTimeout
	}

	// Create a channel for this index
	ch := make(chan interface{}, 1)
	rs.pendingCmds.Store(index, ch)
	defer func() {
		rs.pendingCmds.Delete(index)
		// Return index to pool
		select {
		case rs.indexPool <- index:
		default:
		}
	}()

	select {
	case resp, open := <-ch:
		if !open || resp == nil {
			return nil, fmt.Errorf("connection closed during request")
		}
		// Check if it's an error response
		if errResp, ok := resp.(*protocol.AsyncResponseError); ok {
			return nil, fmt.Errorf("slave error: %s", errResp.Message)
		}
		return resp, nil

	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for response %s from slave %s", index, rs.name)
	}
}

// Run is the main read loop for this slave (()).
// It reads all responses from the slave and dispatches them.
func (rs *RemoteSlave) Run(masterSlaveManager *SlaveManager) {
	defer func() {
		rs.SetOffline("connection closed")
	}()

	var pingIndex string

	for rs.IsOnline() {
		rs.conn.SetReadDeadline(time.Now().Add(masterSocketTimeout))

		obj, err := rs.stream.ReadObject()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				now := time.Now().UnixMilli()
				lastResp := rs.lastResponseReceived.Load()

				// Check if we need to ping
				halfTimeout := masterActualTimeout.Milliseconds() / 2
				if now-lastResp > halfTimeout {
					if pingIndex != "" {
						// Previous ping lost
						select {
						case rs.indexPool <- pingIndex:
						default:
						}
					}
					var perr error
					pingIndex, perr = IssuePing(rs)
					if perr != nil {
						rs.SetOffline("failed to ping")
						return
					}
				}

				// Check actual timeout
				if now-lastResp > masterActualTimeout.Milliseconds() {
					rs.SetOffline(fmt.Sprintf("no response in %dms", now-lastResp))
					return
				}
				continue
			}
			log.Printf("[Master] Error reading from slave %s: %v", rs.name, err)
			return
		}

		rs.lastResponseReceived.Store(time.Now().UnixMilli())

		// Dispatch by type ( switch in RemoteSlave.run())
		switch resp := obj.(type) {
		case *protocol.AsyncResponseDiskStatus:
			rs.diskMu.Lock()
			rs.diskStatus = resp.Status
			rs.diskMu.Unlock()
			masterSlaveManager.publishDiskStatus(rs)

		case *protocol.AsyncResponseRemerge:
			// Process remerge data - add files to master's VFS
			masterSlaveManager.ProcessRemerge(rs, resp)

		case *protocol.AsyncResponseTransferStatus:
			// Update transfer status
			if val, ok := rs.transfers.Load(resp.Status.TransferIndex); ok {
				rt := val.(*RemoteTransfer)
				rt.UpdateStatus(resp.Status)
				if resp.Status.Finished {
					rs.transfers.Delete(resp.Status.TransferIndex)
					masterSlaveManager.publishDiskStatus(rs)
				}
			}

		case *protocol.AsyncResponseTransfer:
			// Transfer setup response - store RemoteTransfer
			rt := NewRemoteTransfer(resp.Info, rs)
			rs.transfers.Store(resp.Info.TransferIndex, rt)
			// Also route to pending command
			rs.routeResponse(resp.GetIndex(), obj)

		case *protocol.AsyncResponseSiteBotMessage:
			log.Printf("[Slave %s] %s", rs.name, resp.Message)

		case *protocol.AsyncResponse:
			rs.routeResponse(resp.Index, obj)
			if pingIndex != "" && resp.Index == pingIndex {
				pingIndex = ""
			}

		case *protocol.AsyncResponseError:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseChecksum:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseSSLCheck:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseMaxPath:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseSFVInfo:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseFileContent:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseMediaInfo:
			rs.routeResponse(resp.Index, obj)

		default:
			log.Printf("[Master] Unknown response type from slave %s: %T", rs.name, obj)
		}
	}
}

// routeResponse delivers a response to the goroutine waiting for it via FetchResponse.
func (rs *RemoteSlave) routeResponse(index string, obj interface{}) {
	if val, ok := rs.pendingCmds.Load(index); ok {
		ch := val.(chan interface{})
		select {
		case ch <- obj:
		default:
		}
	}
}

func (rs *RemoteSlave) SetOffline(reason string) {
	// CompareAndSwap guarantees atomic closure and prevents double-disconnect races.
	if !rs.online.CompareAndSwap(true, false) {
		return // already offline
	}
	
	log.Printf("[Master] Slave %s going offline: %s", rs.name, reason)
	rs.available.Store(false)
	rs.remerging.Store(false)
	if rs.conn != nil {
		rs.conn.Close()
	}

	// [Added] Instantly unblocks any FetchResponse calls waiting for data from this dead slave
	rs.pendingCmds.Range(func(key, value interface{}) bool {
		if ch, ok := value.(chan interface{}); ok {
			close(ch)
		}
		return true
	})
}

func (rs *RemoteSlave) SetAvailable(avail bool) {
	rs.available.Store(avail)
}

// AddTransfer stores a RemoteTransfer on this slave
func (rs *RemoteSlave) AddTransfer(idx int32, rt *RemoteTransfer) {
	rs.transfers.Store(idx, rt)
}

// GetTransfer retrieves a RemoteTransfer by index
func (rs *RemoteSlave) GetTransfer(idx int32) (*RemoteTransfer, bool) {
	val, ok := rs.transfers.Load(idx)
	if !ok {
		return nil, false
	}
	return val.(*RemoteTransfer), true
}
