package core

import (
	"bytes"
	"strings"
	"testing"
)

func TestTemporaryUploadBusyResponseIsRetryable(t *testing.T) {
	var buf bytes.Buffer

	writeTemporaryUploadBusyResponse(&buf, "file.r00")

	got := buf.String()
	if !strings.HasPrefix(got, "450 ") {
		t.Fatalf("busy upload response must be temporary 450, got %q", got)
	}
	if strings.Contains(got, "553") || strings.Contains(strings.ToUpper(got), "X-DUPE") {
		t.Fatalf("busy upload response must not look like a permanent dupe: %q", got)
	}
}

func TestUploadAlreadyInProgressResponseLooksLikeDuplicate(t *testing.T) {
	conn := &bufferConn{}
	s := &Session{
		Conn: conn,
		Config: &Config{
			XdupeEnabled: true,
		},
		XDupeMode: 3,
	}

	writeUploadAlreadyInProgressResponse(s, "file.r00", []string{"file.r01"})

	got := conn.String()
	if !strings.Contains(got, "553-X-DUPE: file.r00") {
		t.Fatalf("expected in-progress file in X-DUPE response, got %q", got)
	}
	if !strings.Contains(got, "553 file.r00: file already exists (X-DUPE)") {
		t.Fatalf("expected permanent duplicate response, got %q", got)
	}
	if strings.Contains(got, "450 ") || strings.Contains(strings.ToLower(got), "retry later") {
		t.Fatalf("in-progress upload should not ask racing clients to retry the same file, got %q", got)
	}
}

func TestValidationDeleteResponsesAreTransferFailures(t *testing.T) {
	var zip bytes.Buffer
	writeZipIntegrityFailureDeleteResponse(&zip)
	if !strings.HasPrefix(zip.String(), "426 ") {
		t.Fatalf("zip delete response must fail the transfer, got %q", zip.String())
	}

	var crc bytes.Buffer
	writeChecksumMismatchDeleteResponse(&crc, 0x12345678, 0x90ABCDEF)
	got := crc.String()
	if !strings.Contains(got, "426- checksum mismatch") || !strings.Contains(got, "\r\n426 Checksum mismatch") {
		t.Fatalf("CRC delete response must be a 426 multiline failure, got %q", got)
	}
	if strings.Contains(got, "226") {
		t.Fatalf("CRC delete response must not contain success code 226, got %q", got)
	}
}

type pendingReplyTestBridge struct {
	pending bool
}

func (b pendingReplyTestBridge) PendingUploadExists(string) bool {
	return b.pending
}

func TestPendingUploadForReplyUsesOptionalBridgeSignal(t *testing.T) {
	if !pendingUploadForReply(pendingReplyTestBridge{pending: true}, "/race/file.r00") {
		t.Fatalf("expected pending upload to be detected")
	}
	if pendingUploadForReply(pendingReplyTestBridge{}, "/race/file.r00") {
		t.Fatalf("did not expect pending upload for false bridge signal")
	}
	if pendingUploadForReply(struct{}{}, "/race/file.r00") {
		t.Fatalf("bridge without pending signal should not report pending")
	}
}
