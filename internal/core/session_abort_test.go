package core

import (
	"net"
	"testing"
	"time"
)

type fakeTransferAborter struct {
	slaveName string
	idx       int32
	reason    string
	called    bool
}

func (f *fakeTransferAborter) AbortTransfer(slaveName string, transferIndex int32, reason string) bool {
	f.called = true
	f.slaveName = slaveName
	f.idx = transferIndex
	f.reason = reason
	return true
}

func TestAbortCurrentTransferUsesBridgeAbort(t *testing.T) {
	bridge := &fakeTransferAborter{}
	s := &Session{MasterManager: bridge}
	s.beginTransferOnSlave("download", "/MP3/release/file.r00", "SLAVE1", 42)
	s.PretCmd = "RETR"
	s.PretArg = "file.r00"
	s.PassthruSlave = "SLAVE1"
	s.PassthruXferIdx = 42

	if !s.abortCurrentTransfer("manual abort") {
		t.Fatalf("expected abortCurrentTransfer to report an aborted transfer")
	}
	if !bridge.called {
		t.Fatalf("expected bridge AbortTransfer to be called")
	}
	if bridge.slaveName != "SLAVE1" || bridge.idx != 42 || bridge.reason != "manual abort" {
		t.Fatalf("unexpected abort args: %#v", bridge)
	}
	if s.TransferDirection != "" || s.TransferPath != "" || s.TransferSlaveName != "" || s.TransferSlaveIdx != 0 {
		t.Fatalf("expected transfer state to be cleared after abort")
	}
	if s.PretCmd != "" || s.PretArg != "" || s.PassthruSlave != nil || s.PassthruXferIdx != 0 {
		t.Fatalf("expected pending passthrough state to be cleared after abort")
	}
}

func TestAbortCurrentTransferClosesTrackedDataConn(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	s := &Session{}
	s.beginTransfer("upload", "/MP3/release/01-track.mp3")
	tracked := trackTransferConn(s, server, "upload")
	if tracked == nil || s.transferConn == nil {
		t.Fatalf("expected transfer data connection to be tracked")
	}

	if !s.abortCurrentTransfer("slowkick") {
		t.Fatalf("expected tracked data connection to be aborted")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatalf("expected peer read to fail after slowkick closes data connection")
	}
	if s.transferConn != nil {
		t.Fatalf("expected tracked data connection to be cleared")
	}
}
