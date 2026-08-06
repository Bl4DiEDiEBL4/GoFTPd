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

func TestActiveDownloadForPathDetectsInFlightDownload(t *testing.T) {
	filePath := "/UPLOAD/release/file.r00"

	if activeDownloadForPath(filePath) {
		t.Fatalf("no session active yet, expected no in-flight download")
	}

	server, client := net.Pipe()
	defer client.Close()

	s := &Session{
		Conn:     server,
		IsLogged: true,
	}
	s.beginTransfer("download", filePath)
	s.ID = registerSession(s)
	defer unregisterSession(s.ID)

	if !activeDownloadForPath(filePath) {
		t.Fatalf("expected in-flight download to be detected")
	}
	if activeDownloadForPath("/UPLOAD/release/other-file.r00") {
		t.Fatalf("expected unrelated path to report no in-flight download")
	}

	s.endTransfer()
	if activeDownloadForPath(filePath) {
		t.Fatalf("expected download path to clear once transfer ends")
	}
}

func TestDownloadPathReservationCountsConcurrentReaders(t *testing.T) {
	filePath := "/UPLOAD/release/shared-file.r00"
	releaseDownloadPath(filePath)

	if !reserveDownloadPath(filePath) || !reserveDownloadPath(filePath) {
		t.Fatalf("expected concurrent download reservations to succeed")
	}
	if !activeDownloadForPath(filePath) {
		t.Fatalf("reserved download path should be treated as active")
	}

	releaseDownloadPath(filePath)
	if !activeDownloadForPath(filePath) {
		t.Fatalf("one remaining reader should keep the path active")
	}
	releaseDownloadPath(filePath)
	if activeDownloadForPath(filePath) {
		t.Fatalf("path should clear after the final reader exits")
	}
}

func TestUploadReservationBlocksDownloadReservation(t *testing.T) {
	filePath := "/UPLOAD/release/uploading-file.r00"
	releaseUploadPath(filePath)
	releaseDownloadPath(filePath)

	if !reserveUploadPath(filePath) {
		t.Fatalf("expected upload reservation to succeed")
	}
	defer releaseUploadPath(filePath)

	if reserveDownloadPath(filePath) {
		releaseDownloadPath(filePath)
		t.Fatalf("download reservation must not pass an active upload reservation")
	}
}

func TestDownloadReservationBlocksUploadReservation(t *testing.T) {
	filePath := "/UPLOAD/release/downloading-file.r00"
	releaseUploadPath(filePath)
	releaseDownloadPath(filePath)

	if !reserveDownloadPath(filePath) {
		t.Fatalf("expected download reservation to succeed")
	}
	defer releaseDownloadPath(filePath)

	if reserveUploadPath(filePath) {
		releaseUploadPath(filePath)
		t.Fatalf("upload reservation must not pass an active download reservation")
	}
}
