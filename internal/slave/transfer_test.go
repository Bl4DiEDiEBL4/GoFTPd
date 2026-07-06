package slave

import (
	"os"
	"path/filepath"
	"testing"
)

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
