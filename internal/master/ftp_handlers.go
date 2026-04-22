package master

import (
	"fmt"
	"log"
	"time"

	"goftpd/internal/protocol"
)

// --------------------------------------------------------------------------
// FTP Command Integration
// These functions are called from the FTP session (internal/core) when
// running in master mode. They coordinate with slaves for data transfers,
// directory listings, and file operations.
//
// The flow for STOR (upload) mirrors  DataConnectionHandler:
//   1. FTP client sends PRET STOR / PASV
//   2. Master picks a slave, tells slave to LISTEN (open passive port)
//   3. Slave returns port number
//   4. Master tells FTP client to connect to slave_ip:port
//   5. FTP client sends STOR, master tells slave to RECEIVE
//   6. FTP client connects to slave's data port and uploads directly
//
// The flow for RETR (download) is similar but uses SEND instead of RECEIVE.
// --------------------------------------------------------------------------

// MasterTransferState holds the state of a pending transfer setup.
// Stored on the FTP session between PASV and STOR/RETR.
type MasterTransferState struct {
	Slave         *RemoteSlave
	Transfer      *RemoteTransfer
	TransferIndex int32
	IsPASV        bool
	IsUpload      bool  // true = STOR, false = RETR
}

// SetupPASVForUpload selects a slave and tells it to listen.
// Returns the slave's PASV address and port for the FTP client.
// () for upload path.
func (sm *SlaveManager) SetupPASVForUpload(filePath string) (*MasterTransferState, string, int, error) {
	slave := sm.SelectSlaveForUpload(filePath)
	if slave == nil {
		return nil, "", 0, fmt.Errorf("no available slave for upload")
	}

	// Tell slave to listen (open passive port)
	index, err := IssueListen(slave, false, false)
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to issue listen to %s: %w", slave.Name(), err)
	}

	// Wait for slave to respond with port
	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return nil, "", 0, fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return nil, "", 0, fmt.Errorf("unexpected response type from slave")
	}

	rt := NewRemoteTransfer(transferResp.Info, slave)
	rt.SetPath(filePath)
	rt.SetDirection('R')
	slave.AddTransfer(transferResp.Info.TransferIndex, rt)

	state := &MasterTransferState{
		Slave:         slave,
		Transfer:      rt,
		TransferIndex: transferResp.Info.TransferIndex,
		IsPASV:        true,
		IsUpload:      true,
	}

	ip := slave.GetPASVIP()
	port := transferResp.Info.Port

	log.Printf("[Master] Upload PASV: slave=%s ip=%s port=%d path=%s", slave.Name(), ip, port, filePath)

	return state, ip, port, nil
}

// SetupPASVForDownload selects the slave that has the file and tells it to listen.
// () for download path.
func (sm *SlaveManager) SetupPASVForDownload(filePath string) (*MasterTransferState, string, int, error) {
	slave := sm.SelectSlaveForDownload(filePath)
	if slave == nil {
		return nil, "", 0, fmt.Errorf("file not found on any available slave: %s", filePath)
	}

	index, err := IssueListen(slave, false, false)
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to issue listen to %s: %w", slave.Name(), err)
	}

	resp, err := slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return nil, "", 0, fmt.Errorf("slave %s listen failed: %w", slave.Name(), err)
	}

	transferResp, ok := resp.(*protocol.AsyncResponseTransfer)
	if !ok {
		return nil, "", 0, fmt.Errorf("unexpected response type from slave")
	}

	rt := NewRemoteTransfer(transferResp.Info, slave)
	rt.SetPath(filePath)
	rt.SetDirection('S')
	slave.AddTransfer(transferResp.Info.TransferIndex, rt)

	state := &MasterTransferState{
		Slave:         slave,
		Transfer:      rt,
		TransferIndex: transferResp.Info.TransferIndex,
		IsPASV:        true,
		IsUpload:      false,
	}

	ip := slave.GetPASVIP()
	port := transferResp.Info.Port

	log.Printf("[Master] Download PASV: slave=%s ip=%s port=%d path=%s", slave.Name(), ip, port, filePath)

	return state, ip, port, nil
}

// ExecuteUpload tells the slave to start receiving the file.
// Called after the FTP client has received the PASV response and sends STOR.
// () calling issueReceiveToSlave.
func (sm *SlaveManager) ExecuteUpload(state *MasterTransferState, clientAddr string) error {
	index, err := IssueReceive(
		state.Slave,
		state.Transfer.GetPath(),
		'I', // binary
		0,   // position
		clientAddr,
		state.TransferIndex,
		0, 0, // min/max speed
	)
	if err != nil {
		return fmt.Errorf("issue receive failed: %w", err)
	}

	// Wait for acknowledgement (slave sends AsyncResponse before starting transfer)
	_, err = state.Slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return fmt.Errorf("receive ack failed: %w", err)
	}

	log.Printf("[Master] Upload started: %s on slave %s", state.Transfer.GetPath(), state.Slave.Name())

	// The actual transfer happens directly between FTP client and slave.
	// The TransferStatus will come asynchronously via the RemoteSlave.Run() loop.
	return nil
}

// ExecuteDownload tells the slave to start sending the file.
// () calling issueSendToSlave.
func (sm *SlaveManager) ExecuteDownload(state *MasterTransferState, clientAddr string) error {
	index, err := IssueSend(
		state.Slave,
		state.Transfer.GetPath(),
		'I', // binary
		0,   // position
		clientAddr,
		state.TransferIndex,
		0, 0, // min/max speed
	)
	if err != nil {
		return fmt.Errorf("issue send failed: %w", err)
	}

	_, err = state.Slave.FetchResponse(index, 30*time.Second)
	if err != nil {
		return fmt.Errorf("send ack failed: %w", err)
	}

	log.Printf("[Master] Download started: %s from slave %s", state.Transfer.GetPath(), state.Slave.Name())
	return nil
}

// WaitForTransferComplete blocks until the transfer finishes or times out.
func (sm *SlaveManager) WaitForTransferComplete(state *MasterTransferState, timeout time.Duration) (*protocol.TransferStatus, error) {
	deadline := time.After(timeout)

	for {
		select {
		case <-deadline:
			state.Transfer.Abort("timeout")
			return nil, fmt.Errorf("transfer timeout")
		case <-time.After(500 * time.Millisecond):
			if state.Transfer.IsFinished() {
				status := state.Transfer.GetStatus()
				if status.Error != "" {
					return &status, fmt.Errorf("transfer error: %s", status.Error)
				}
				return &status, nil
			}
		}
	}
}

// ListDirectory returns VFS listing for a directory.
func (sm *SlaveManager) ListDirectory(dirPath string) []*VFSFile {
	return sm.vfs.ListDirectory(dirPath)
}

// DeleteFile deletes a file from all slaves and from VFS.
func (sm *SlaveManager) DeleteFile(path string) error {
	file := sm.vfs.GetFile(path)
	if file == nil {
		return fmt.Errorf("file not found: %s", path)
	}
	if sm.IsSlaveReadOnly(file.SlaveName) {
		return fmt.Errorf("path is on read-only slave: %s", path)
	}
	sm.DeleteOnAllSlaves(path)
	return nil
}

// RenameFile renames a file on all slaves and in VFS.
func (sm *SlaveManager) RenameFile(from, toDir, toName string) {
	file := sm.vfs.GetFile(from)
	if file != nil && sm.IsSlaveReadOnly(file.SlaveName) {
		log.Printf("[SlaveManager] Refusing rename of read-only path %s on slave %s", from, file.SlaveName)
		return
	}
	sm.RenameOnAllSlaves(from, toDir, toName)
	to := toDir
	if to == "/" {
		to = "/" + toName
	} else {
		to = toDir + "/" + toName
	}
	sm.vfs.RenameFile(from, to)
}

// MakeDirectory creates a directory in the VFS (virtual only, slaves create on upload).
func (sm *SlaveManager) MakeDirectory(path, owner, group string) {
	sm.vfs.AddFile(path, VFSFile{
		Path:         path,
		IsDir:        true,
		LastModified: time.Now().Unix(),
		Owner:        owner,
		Group:        group,
	})
}
