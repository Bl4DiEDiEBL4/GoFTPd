package master

import (
	"fmt"
	"log"
	"strings"
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
	IsUpload      bool // true = STOR, false = RETR
	TransferType  byte
	ResumeOffset  int64
	Encrypted     bool
	SSLClientMode bool
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

	rt, ok := slave.GetTransfer(transferResp.Info.TransferIndex)
	if !ok || rt == nil {
		rt = NewRemoteTransfer(transferResp.Info, slave)
		slave.AddTransfer(transferResp.Info.TransferIndex, rt)
	}
	rt.SetPath(filePath)
	rt.SetDirection('R')

	state := &MasterTransferState{
		Slave:         slave,
		Transfer:      rt,
		TransferIndex: transferResp.Info.TransferIndex,
		IsPASV:        true,
		IsUpload:      true,
		TransferType:  'I',
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

	rt, ok := slave.GetTransfer(transferResp.Info.TransferIndex)
	if !ok || rt == nil {
		rt = NewRemoteTransfer(transferResp.Info, slave)
		slave.AddTransfer(transferResp.Info.TransferIndex, rt)
	}
	rt.SetPath(filePath)
	rt.SetDirection('S')

	state := &MasterTransferState{
		Slave:         slave,
		Transfer:      rt,
		TransferIndex: transferResp.Info.TransferIndex,
		IsPASV:        true,
		IsUpload:      false,
		TransferType:  'I',
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
		normalizeLegacyTransferType(state.TransferType),
		state.ResumeOffset,
		clientAddr,
		state.TransferIndex,
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
		normalizeLegacyTransferType(state.TransferType),
		state.ResumeOffset,
		clientAddr,
		state.TransferIndex,
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
	status, err := state.Slave.WaitTransferStatus(state.TransferIndex, timeout)
	if err != nil {
		state.Transfer.Abort("timeout")
		return nil, err
	}
	if status.Error != "" {
		return &status, fmt.Errorf("transfer error: %s", status.Error)
	}
	return &status, nil
}

func normalizeLegacyTransferType(transferType byte) byte {
	switch transferType {
	case 'A', 'I':
		return transferType
	default:
		return 'I'
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
	if file.IsSymlink || strings.TrimSpace(file.SlaveName) == "" {
		sm.vfs.DeleteFile(path)
		return nil
	}
	if sm.IsSlaveReadOnly(file.SlaveName) {
		return fmt.Errorf("path is on read-only slave: %s", path)
	}
	rs := sm.GetSlave(file.SlaveName)
	if rs == nil || !rs.IsAvailable() {
		return fmt.Errorf("owning slave unavailable: %s", file.SlaveName)
	}
	index, err := IssueDelete(rs, path)
	if err != nil {
		return err
	}
	if _, err := rs.FetchResponse(index, 5*time.Minute); err != nil && !isIgnorableDeleteError(err) {
		return err
	}
	sm.vfs.DeleteFile(path)
	return nil
}

// RenameFile renames a file on its owning slave and in VFS.
func (sm *SlaveManager) RenameFile(from, toDir, toName string) error {
	file := sm.vfs.GetFile(from)
	if file == nil {
		return fmt.Errorf("path not found: %s", from)
	}
	to := toDir
	if to == "/" {
		to = "/" + toName
	} else {
		to = toDir + "/" + toName
	}
	if file.IsSymlink && strings.TrimSpace(file.SlaveName) == "" {
		sm.vfs.RenameFile(from, to)
		return nil
	}
	if sm.IsSlaveReadOnly(file.SlaveName) {
		log.Printf("[SlaveManager] Refusing rename of read-only path %s on slave %s", from, file.SlaveName)
		return fmt.Errorf("path is on read-only slave: %s", from)
	}
	rs := sm.GetSlave(file.SlaveName)
	if rs == nil || !rs.IsAvailable() {
		return fmt.Errorf("owning slave unavailable: %s", file.SlaveName)
	}
	index, err := IssueRename(rs, from, toDir, toName)
	if err != nil {
		return err
	}
	if _, err := rs.FetchResponse(index, 30*time.Second); err != nil {
		return err
	}
	sm.vfs.RenameFile(from, to)
	return nil
}

// MakeDirectoryOnSlave records a directory in VFS after it was created on a slave.
func (sm *SlaveManager) MakeDirectoryOnSlave(path, owner, group, slaveName string) {
	sm.vfs.AddFile(path, VFSFile{
		Path:         path,
		IsDir:        true,
		SlaveName:    slaveName,
		LastModified: time.Now().Unix(),
		Owner:        owner,
		Group:        group,
	})
}
