package master

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"weaveftpd/internal/protocol"
)

const (
	masterSocketTimeout = 10 * time.Second
)

// RemoteSlave represents a connected slave as seen from the master.
type RemoteSlave struct {
	name      string
	conn      net.Conn
	stream    *protocol.ObjectStream
	writeMu   sync.Mutex // protects stream writes
	onOffline func(name string)

	// Async index pool ( / _indexWithCommands)
	nextCmdIndex   atomic.Uint64
	pendingCmds    sync.Map // index (string) -> chan interface{}
	earlyResponses sync.Map // index (string) -> interface{}
	commandNotify  chan struct{}
	remergeQueue   chan *protocol.AsyncResponseRemerge
	remergeDrained chan struct{}
	remergeStop    chan struct{} // closed on SetOffline to stop runRemergeQueue without racing enqueueRemerge's send

	// State
	online            atomic.Bool
	available         atomic.Bool
	remerging         atomic.Bool
	remergeQueueDepth atomic.Int64
	remergePaused     atomic.Bool
	remergeStateMu    sync.Mutex
	remergeIndex      string
	remergeTimedOut   bool
	diskStatus        protocol.DiskStatus
	diskMu            sync.RWMutex

	// Transfers
	transfers          sync.Map     // TransferIndex (int32) -> *RemoteTransfer
	completedTransfers sync.Map     // TransferIndex (int32) -> protocol.TransferStatus
	transferDone       sync.Map     // TransferIndex (int32) -> chan struct{}, closed on completion
	activeCount        atomic.Int32 // number of currently-active uploads/downloads

	// Timing
	lastResponseReceived atomic.Int64
	lastCommandSent      atomic.Int64
	heartbeatTimeout     time.Duration

	// Properties ()
	properties map[string]string
	propMu     sync.RWMutex

	// Masks
	masks  []string
	maskMu sync.RWMutex // [Added] Protects masks from concurrent read/write panics
}

// NewRemoteSlave creates a new remote slave from an accepted connection.
func NewRemoteSlave(name string, conn net.Conn, stream *protocol.ObjectStream, heartbeatTimeout time.Duration, onOffline func(name string)) *RemoteSlave {
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = 60 * time.Second
	}
	rs := &RemoteSlave{
		name:             name,
		conn:             conn,
		stream:           stream,
		onOffline:        onOffline,
		commandNotify:    make(chan struct{}, 256),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 512),
		remergeDrained:   make(chan struct{}, 1),
		remergeStop:      make(chan struct{}),
		properties:       make(map[string]string),
		heartbeatTimeout: heartbeatTimeout,
	}

	rs.lastResponseReceived.Store(time.Now().UnixMilli())
	rs.lastCommandSent.Store(time.Now().UnixMilli())
	rs.online.Store(true)

	return rs
}

func (rs *RemoteSlave) Name() string      { return rs.name }
func (rs *RemoteSlave) IsOnline() bool    { return rs.online.Load() }
func (rs *RemoteSlave) IsAvailable() bool { return rs.available.Load() }
func (rs *RemoteSlave) IsRemerging() bool { return rs.remerging.Load() }

func (rs *RemoteSlave) setActiveRemerge(index string) {
	rs.remergeStateMu.Lock()
	rs.remergeIndex = index
	rs.remergeTimedOut = false
	rs.remergeStateMu.Unlock()
	rs.remerging.Store(index != "")
}

func (rs *RemoteSlave) markActiveRemergeTimedOut(index string) bool {
	rs.remergeStateMu.Lock()
	defer rs.remergeStateMu.Unlock()
	if rs.remergeIndex != index {
		return false
	}
	rs.remergeTimedOut = true
	rs.remerging.Store(true)
	return true
}

func (rs *RemoteSlave) clearActiveRemerge(index string) bool {
	rs.remergeStateMu.Lock()
	defer rs.remergeStateMu.Unlock()
	if index != "" && rs.remergeIndex != index {
		return false
	}
	rs.remergeIndex = ""
	rs.remergeTimedOut = false
	rs.remerging.Store(false)
	rs.remergePaused.Store(false)
	return true
}

func (rs *RemoteSlave) isTimedOutRemergeResponse(index string) bool {
	rs.remergeStateMu.Lock()
	defer rs.remergeStateMu.Unlock()
	return index != "" && rs.remergeIndex == index && rs.remergeTimedOut
}

// ActiveTransfers returns the current number of in-flight data transfers on this slave.
// Used by upload routing and background remerge skip-busy decisions.
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
	return fmt.Sprintf("%016x", rs.nextCmdIndex.Add(1)), nil
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
		timeout = rs.heartbeatTimeout
	}

	// Create a channel for this index
	ch := make(chan interface{}, 1)
	rs.pendingCmds.Store(index, ch)
	if resp, ok := rs.earlyResponses.LoadAndDelete(index); ok {
		select {
		case ch <- resp:
		default:
		}
	}
	defer func() {
		rs.pendingCmds.Delete(index)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

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

	case <-timer.C:
		return nil, fmt.Errorf("timeout waiting for response %s from slave %s", index, rs.name)
	}
}

// Run is the main read loop for this slave (()).
// It reads all responses from the slave and dispatches them.
func (rs *RemoteSlave) Run(masterSlaveManager *SlaveManager) {
	defer func() {
		rs.SetOffline("connection closed")
	}()

	go rs.runRemergeQueue(masterSlaveManager)

	var pingIndex string

	for rs.IsOnline() {
		rs.conn.SetReadDeadline(time.Now().Add(masterSocketTimeout))

		obj, err := rs.stream.ReadObject()
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				now := time.Now().UnixMilli()
				lastResp := rs.lastResponseReceived.Load()

				// Check if we need to ping
				halfTimeout := rs.heartbeatTimeout.Milliseconds() / 2
				if now-lastResp > halfTimeout {
					if pingIndex != "" {
						// Previous ping lost
						rs.pendingCmds.Delete(pingIndex)
					}
					var perr error
					pingIndex, perr = IssuePing(rs)
					if perr != nil {
						rs.SetOffline("failed to ping")
						return
					}
				}

				// Check actual timeout
				if now-lastResp > rs.heartbeatTimeout.Milliseconds() {
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
			rs.enqueueRemerge(masterSlaveManager, resp)

		case *protocol.AsyncResponseTransferStatus:
			// Update transfer status
			if val, ok := rs.transfers.Load(resp.Status.TransferIndex); ok {
				rt := val.(*RemoteTransfer)
				rt.UpdateStatus(resp.Status)
				if resp.Status.Finished {
					rs.completedTransfers.Store(resp.Status.TransferIndex, resp.Status)
					rs.transfers.Delete(resp.Status.TransferIndex)
					rs.signalTransferDone(resp.Status.TransferIndex)
					masterSlaveManager.publishDiskStatus(rs)
				}
			} else if resp.Status.Finished {
				rs.completedTransfers.Store(resp.Status.TransferIndex, resp.Status)
				rs.signalTransferDone(resp.Status.TransferIndex)
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

		case *protocol.AsyncResponseZipEntryContent:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseZipIntegrity:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseMediaInfo:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseTransferStats:
			rs.routeResponse(resp.Index, obj)

		case *protocol.AsyncResponseCommandResult:
			rs.routeResponse(resp.Index, obj)

		default:
			log.Printf("[Master] Unknown response type from slave %s: %T", rs.name, obj)
		}
	}
}

func (rs *RemoteSlave) runRemergeQueue(masterSlaveManager *SlaveManager) {
	// Selects on remergeStop (closed by SetOffline) instead of ranging over
	// remergeQueue, since enqueueRemerge may still be sending concurrently on
	// disconnect and closing remergeQueue itself would risk a send-on-closed-channel panic.
	for {
		select {
		case resp := <-rs.remergeQueue:
			if resp == nil {
				continue
			}
			masterSlaveManager.ProcessRemerge(rs, resp)
			depth := rs.remergeQueueDepth.Add(-1)
			if depth < 0 {
				rs.remergeQueueDepth.Store(0)
				depth = 0
			}
			rs.applyRemergeFlowControl(masterSlaveManager, depth)
			if depth == 0 {
				select {
				case rs.remergeDrained <- struct{}{}:
				default:
				}
			}
		case <-rs.remergeStop:
			return
		}
	}
}

func (rs *RemoteSlave) enqueueRemerge(masterSlaveManager *SlaveManager, resp *protocol.AsyncResponseRemerge) {
	depth := rs.remergeQueueDepth.Add(1)
	rs.remergeQueue <- resp
	rs.applyRemergeFlowControl(masterSlaveManager, depth)
}

func (rs *RemoteSlave) applyRemergeFlowControl(masterSlaveManager *SlaveManager, depth int64) {
	if masterSlaveManager == nil || !rs.IsOnline() || !rs.IsRemerging() {
		return
	}
	pauseThreshold, resumeThreshold := masterSlaveManager.GetRemergeFlowControl()
	if rs.remergePaused.Load() {
		if depth <= int64(resumeThreshold) {
			if err := IssueRemergeResume(rs); err != nil {
				log.Printf("[Master] Failed to resume slave %s remerge: %v", rs.name, err)
				return
			}
			rs.remergePaused.Store(false)
			masterSlaveManager.publishRemergeStatus(RemergeStatus{
				Action:  "resumed",
				Status:  "running",
				Slave:   rs.name,
				Message: fmt.Sprintf("remerge resumed for %s: queue depth %d <= resume threshold %d", rs.name, depth, resumeThreshold),
			})
		}
		return
	}
	if depth > int64(pauseThreshold) {
		if err := IssueRemergePause(rs); err != nil {
			log.Printf("[Master] Failed to pause slave %s remerge: %v", rs.name, err)
			return
		}
		rs.remergePaused.Store(true)
		masterSlaveManager.publishRemergeStatus(RemergeStatus{
			Action:  "paused",
			Status:  "paused",
			Slave:   rs.name,
			Message: fmt.Sprintf("remerge paused for %s: queue depth %d > pause threshold %d", rs.name, depth, pauseThreshold),
		})
	}
}

func (rs *RemoteSlave) WaitForRemergeDrain(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = rs.heartbeatTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		if rs.remergeQueueDepth.Load() == 0 {
			return nil
		}
		if !rs.IsOnline() {
			return fmt.Errorf("slave %s went offline while draining remerge queue", rs.name)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("timeout waiting for remerge queue to drain on slave %s", rs.name)
		}
		wait := remaining
		if wait > 250*time.Millisecond {
			wait = 250 * time.Millisecond
		}
		select {
		case <-rs.remergeDrained:
		case <-time.After(wait):
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
		return
	}
	if rs.isTimedOutRemergeResponse(index) {
		rs.clearActiveRemerge(index)
		log.Printf("[Master] Late remerge response from slave %s cleared timed-out remerge state", rs.name)
		return
	}
	rs.earlyResponses.Store(index, obj)
}

func (rs *RemoteSlave) SetOffline(reason string) {
	// CompareAndSwap guarantees atomic closure and prevents double-disconnect races.
	if !rs.online.CompareAndSwap(true, false) {
		return // already offline
	}

	log.Printf("[Master] Slave %s going offline: %s", rs.name, reason)
	rs.available.Store(false)
	rs.clearActiveRemerge("")
	rs.remergeQueueDepth.Store(0)
	if rs.remergeStop != nil {
		close(rs.remergeStop)
	}
	if rs.conn != nil {
		rs.conn.Close()
	}
	if rs.onOffline != nil {
		rs.onOffline(rs.name)
	}

	// [Added] Instantly unblocks any FetchResponse calls waiting for data from this dead slave
	rs.pendingCmds.Range(func(key, value interface{}) bool {
		if ch, ok := value.(chan interface{}); ok {
			close(ch)
		}
		return true
	})
	select {
	case rs.remergeDrained <- struct{}{}:
	default:
	}
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

// transferDoneChan returns the per-transfer completion channel, creating it if
// absent. Closed by signalTransferDone when the slave reports the transfer
// finished, so WaitTransferStatus wakes immediately instead of polling.
func (rs *RemoteSlave) transferDoneChan(idx int32) chan struct{} {
	ch, _ := rs.transferDone.LoadOrStore(idx, make(chan struct{}))
	return ch.(chan struct{})
}

// signalTransferDone wakes any WaitTransferStatus waiting on idx. LoadAndDelete
// guarantees exactly one closer, so the channel is never double-closed.
func (rs *RemoteSlave) signalTransferDone(idx int32) {
	if ch, ok := rs.transferDone.LoadAndDelete(idx); ok {
		close(ch.(chan struct{}))
	}
}

func (rs *RemoteSlave) clearTransferDone(idx int32) {
	rs.transferDone.Delete(idx)
}

func (rs *RemoteSlave) WaitTransferStatus(idx int32, timeout time.Duration) (protocol.TransferStatus, error) {
	doneCh := rs.transferDoneChan(idx)
	defer rs.clearTransferDone(idx)

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	// Safety ticker only for offline detection / missed-signal races; completion
	// itself is delivered instantly via doneCh, so this can be coarse.
	safety := time.NewTicker(time.Second)
	defer safety.Stop()

	for {
		if val, ok := rs.completedTransfers.LoadAndDelete(idx); ok {
			return val.(protocol.TransferStatus), nil
		}
		if val, ok := rs.transfers.Load(idx); ok {
			rt := val.(*RemoteTransfer)
			if rt.IsFinished() {
				status := rt.GetStatus()
				rs.transfers.Delete(idx)
				return status, nil
			}
		}
		if !rs.IsOnline() {
			return protocol.TransferStatus{}, fmt.Errorf("slave %s went offline during transfer %d", rs.name, idx)
		}

		select {
		case <-doneCh:
			// completion recorded; loop re-reads it from completedTransfers
		case <-safety.C:
			// periodic re-check (offline / missed signal)
		case <-deadline.C:
			return protocol.TransferStatus{}, fmt.Errorf("timeout waiting for transfer %d from slave %s", idx, rs.name)
		}
	}
}
