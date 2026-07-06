package core

import (
	"net"
	"testing"

	"weaveftpd/internal/user"
)

func TestDisconnectActiveSessionClearsTransferState(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	s := &Session{
		Conn:       server,
		IsLogged:   true,
		CurrentDir: "/",
	}
	s.beginTransferOnSlave("download", "/MP3/Test.Release-GRP/file.mp3", "SLAVE1", 42)
	s.ID = registerSession(s)
	defer unregisterSession(s.ID)

	if !DisconnectActiveSession(s.ID) {
		t.Fatalf("expected disconnect to succeed")
	}

	snaps := listActiveSessions()
	if len(snaps) != 1 {
		t.Fatalf("expected one active session snapshot, got %d", len(snaps))
	}
	snap := snaps[0]
	if snap.TransferDirection != "" {
		t.Fatalf("expected transfer direction to be cleared, got %q", snap.TransferDirection)
	}
	if snap.TransferPath != "" {
		t.Fatalf("expected transfer path to be cleared, got %q", snap.TransferPath)
	}
	if snap.TransferSlaveName != "" || snap.TransferSlaveIdx != 0 {
		t.Fatalf("expected slave transfer identity to be cleared, got %q/%d", snap.TransferSlaveName, snap.TransferSlaveIdx)
	}
}

func TestCountTransfersForUserCountsByDirection(t *testing.T) {
	uploadServer, uploadClient := net.Pipe()
	defer uploadClient.Close()
	downloadServer, downloadClient := net.Pipe()
	defer downloadClient.Close()

	uploadSession := &Session{
		Conn:     uploadServer,
		IsLogged: true,
		User:     &user.User{Name: "tester"},
	}
	uploadSession.beginTransfer("upload", "/UPLOAD/release/file.r00")
	uploadSession.ID = registerSession(uploadSession)
	defer unregisterSession(uploadSession.ID)

	downloadSession := &Session{
		Conn:     downloadServer,
		IsLogged: true,
		User:     &user.User{Name: "tester"},
	}
	downloadSession.beginTransfer("download", "/UPLOAD/release/file.r01")
	downloadSession.ID = registerSession(downloadSession)
	defer unregisterSession(downloadSession.ID)

	if got := countTransfersForUser("tester", "upload"); got != 1 {
		t.Fatalf("countTransfersForUser(upload) = %d, want 1", got)
	}
	if got := countTransfersForUser("tester", "download"); got != 1 {
		t.Fatalf("countTransfersForUser(download) = %d, want 1", got)
	}
	if got := countTransfersForUser("tester", "other"); got != 0 {
		t.Fatalf("countTransfersForUser(other) = %d, want 0", got)
	}
}

func TestReserveUploadPathBlocksDuplicateUntilRelease(t *testing.T) {
	filePath := "/UPLOAD/release/file.r00"
	releaseUploadPath(filePath)

	if !reserveUploadPath(filePath) {
		t.Fatalf("first reservation should succeed")
	}
	if !activeUploadForPath(filePath) {
		t.Fatalf("reserved upload path should be treated as active")
	}
	if reserveUploadPath(filePath) {
		t.Fatalf("second reservation should be blocked")
	}

	releaseUploadPath(filePath)
	if activeUploadForPath(filePath) {
		t.Fatalf("released upload path should not be treated as active")
	}
	if !reserveUploadPath(filePath) {
		t.Fatalf("reservation should succeed again after release")
	}
	releaseUploadPath(filePath)
}
