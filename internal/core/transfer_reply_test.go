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
