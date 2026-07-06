package master

import (
	"fmt"
	"sync"
	"time"

	"weaveftpd/internal/protocol"
)

// RemoteTransfer represents a transfer happening on a slave, tracked by the master.
type RemoteTransfer struct {
	connectInfo protocol.ConnectInfo
	slave       *RemoteSlave
	status      protocol.TransferStatus
	statusMu    sync.RWMutex
	path        string
	direction   byte // 'R' = receiving upload, 'S' = sending download
}

func NewRemoteTransfer(info protocol.ConnectInfo, slave *RemoteSlave) *RemoteTransfer {
	return &RemoteTransfer{
		connectInfo: info,
		slave:       slave,
		status:      info.Status,
	}
}

func (rt *RemoteTransfer) GetConnectInfo() protocol.ConnectInfo {
	return rt.connectInfo
}

func (rt *RemoteTransfer) GetSlave() *RemoteSlave {
	return rt.slave
}

func (rt *RemoteTransfer) SetPath(path string) {
	rt.path = path
}

func (rt *RemoteTransfer) GetPath() string {
	return rt.path
}

func (rt *RemoteTransfer) SetDirection(dir byte) {
	rt.direction = dir
}

func (rt *RemoteTransfer) GetDirection() byte {
	return rt.direction
}

func (rt *RemoteTransfer) UpdateStatus(status protocol.TransferStatus) {
	rt.statusMu.Lock()
	rt.status = status
	rt.statusMu.Unlock()
}

func (rt *RemoteTransfer) GetStatus() protocol.TransferStatus {
	rt.statusMu.RLock()
	defer rt.statusMu.RUnlock()
	return rt.status
}

func (rt *RemoteTransfer) IsFinished() bool {
	rt.statusMu.RLock()
	defer rt.statusMu.RUnlock()
	return rt.status.Finished
}

func (rt *RemoteTransfer) GetTransferred() int64 {
	rt.statusMu.RLock()
	defer rt.statusMu.RUnlock()
	return rt.status.Transferred
}

func (rt *RemoteTransfer) GetTransferSpeed() int64 {
	rt.statusMu.RLock()
	defer rt.statusMu.RUnlock()
	if rt.status.Elapsed <= 0 {
		return 0
	}
	return rt.status.Transferred * 1000 / rt.status.Elapsed // bytes/sec
}

func (rt *RemoteTransfer) Abort(reason string) {
	if rt.IsFinished() {
		return
	}
	IssueAbort(rt.slave, rt.connectInfo.TransferIndex, reason)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rt.IsFinished() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	rt.statusMu.Lock()
	if !rt.status.Finished && rt.status.Error == "" {
		rt.status.Error = fmt.Sprintf("aborted: %s", reason)
		rt.status.Finished = true
	}
	rt.statusMu.Unlock()
}
