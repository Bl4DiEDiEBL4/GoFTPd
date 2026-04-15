package master

import (
	"sync"

	"goftpd/internal/protocol"
)

// RemoteTransfer represents a transfer happening on a slave, tracked by the master.
// 
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
	IssueAbort(rt.slave, rt.connectInfo.TransferIndex, reason)
}
