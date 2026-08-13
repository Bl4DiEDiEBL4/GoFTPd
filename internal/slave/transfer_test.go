package slave

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConnectActiveDoesNotBindAdvertisedPASVAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	s := &Slave{bindIP: "203.0.113.10", timeout: time.Second}
	transfer := NewTransfer(nil, nil, 1, s, false, false)
	transfer.SetActiveAddress(listener.Addr().String())
	if err := transfer.connectActive(); err != nil {
		t.Fatalf("connectActive bound the advertised NAT address: %v", err)
	}
	defer transfer.currentConn().Close()

	select {
	case conn := <-accepted:
		conn.Close()
	case <-time.After(time.Second):
		t.Fatal("listener did not receive active connection")
	}
}

func TestCleanupFailedReceiveRemovesFreshUpload(t *testing.T) {
	dir := t.TempDir()
	fullPath := filepath.Join(dir, "fresh.bin")
	if err := os.WriteFile(fullPath, []byte("broken"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cleanupFailedReceive(nil, fullPath, 0)

	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		t.Fatalf("expected fresh failed upload to be removed, stat err=%v", err)
	}
}

func TestCleanupFailedReceiveTruncatesBackToResumeOffset(t *testing.T) {
	dir := t.TempDir()
	fullPath := filepath.Join(dir, "resume.bin")
	if err := os.WriteFile(fullPath, []byte("abcdefghij"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	file, err := os.OpenFile(fullPath, os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	defer file.Close()

	if _, err := file.Seek(6, 0); err != nil {
		t.Fatalf("seek file: %v", err)
	}
	if _, err := file.Write([]byte("WXYZ")); err != nil {
		t.Fatalf("append bytes: %v", err)
	}

	cleanupFailedReceive(file, fullPath, 6)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) != "abcdef" {
		t.Fatalf("expected file truncated back to resume offset, got %q", string(data))
	}
}

func TestReceiveFileStartsTimerOnFirstDataByte(t *testing.T) {
	dir := t.TempDir()
	slaveSide, clientSide := net.Pipe()
	defer clientSide.Close()

	s := &Slave{roots: []MountedRoot{{Path: dir, MountPath: "/"}}}
	transfer := NewTransfer(nil, slaveSide, 1, s, false, false)
	transfer.SetPath("/delayed.bin")

	done := make(chan int64, 1)
	errs := make(chan string, 1)
	go func() {
		status := transfer.ReceiveFile("/delayed.bin", 0, "")
		if status.Error != "" {
			errs <- status.Error
			return
		}
		done <- status.Elapsed
	}()

	deadline := time.Now().Add(time.Second)
	for {
		stat := transfer.SnapshotLiveStat()
		if stat.Direction == TransferReceiving {
			if stat.StartedUnixMs != 0 {
				t.Fatalf("receive timer started before data arrived: %d", stat.StartedUnixMs)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("transfer did not enter receiving state")
		}
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(100 * time.Millisecond)
	if stat := transfer.SnapshotLiveStat(); stat.StartedUnixMs != 0 {
		t.Fatalf("receive timer started during pre-data wait: %d", stat.StartedUnixMs)
	}

	if _, err := clientSide.Write([]byte("a")); err != nil {
		t.Fatalf("write first byte: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := clientSide.Write([]byte("b")); err != nil {
		t.Fatalf("write second byte: %v", err)
	}
	clientSide.Close()

	select {
	case err := <-errs:
		t.Fatalf("receive failed: %s", err)
	case elapsed := <-done:
		if elapsed <= 0 {
			t.Fatalf("expected positive elapsed after data, got %d", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("receive did not finish")
	}

	data, err := os.ReadFile(filepath.Join(dir, "delayed.bin"))
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if string(data) != "ab" {
		t.Fatalf("upload content = %q, want ab", string(data))
	}
}

func TestFindAssociatedUploadUsesPathIndex(t *testing.T) {
	s := &Slave{}
	upload := NewTransfer(nil, nil, 1, s, false, false)
	upload.SetPath("/X265/Release/File.R00")
	upload.mu.Lock()
	upload.direction = TransferReceiving
	upload.mu.Unlock()
	s.registerUpload(upload)
	defer s.unregisterUpload(upload)

	download := NewTransfer(nil, nil, 2, s, false, false)
	if got := download.findAssociatedUpload("/x265/release/file.r00"); got != upload {
		t.Fatalf("expected associated upload from path index, got %+v", got)
	}

	upload.mu.Lock()
	upload.finished = upload.started.Add(1)
	upload.mu.Unlock()
	if got := download.findAssociatedUpload("/x265/release/file.r00"); got != nil {
		t.Fatalf("expected finished upload to be ignored, got %+v", got)
	}
}

func TestUnregisterUploadDoesNotDeleteSamePathSuccessor(t *testing.T) {
	s := &Slave{}
	first := NewTransfer(nil, nil, 1, s, false, false)
	first.SetPath("/X265/Release/File.R00")
	second := NewTransfer(nil, nil, 2, s, false, false)
	second.SetPath("/X265/Release/File.R00")

	s.registerUpload(first)
	s.registerUpload(second)
	s.unregisterUpload(first)

	if got := s.findUploadByPath("/x265/release/file.r00", nil); got != second {
		t.Fatalf("expected successor upload to remain indexed, got %+v", got)
	}
}
