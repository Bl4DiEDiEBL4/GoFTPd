package core

import (
	"fmt"
	"io"
	"net"
	"strings"
)

func writeTransferFailure(conn net.Conn, operation string, err error) {
	if conn == nil {
		return
	}
	if err == nil {
		fmt.Fprintf(conn, "426 %s failed.\r\n", operation)
		return
	}
	fmt.Fprintf(conn, "426 %s failed: %v\r\n", operation, err)
}

func writeTemporaryUploadBusyResponse(conn io.Writer, fileName string) {
	if conn == nil {
		return
	}
	fmt.Fprintf(conn, "450 %s: upload already in progress, retry later\r\n", fileName)
}

func writeUploadAlreadyInProgressResponse(s *Session, fileName string, existingNames []string) {
	if s == nil || s.Conn == nil {
		return
	}
	if s.Config != nil && s.Config.XdupeEnabled && s.XDupeMode > 0 {
		for _, line := range xdupeResponseLines(s.XDupeMode, duplicateResponseFileNames(existingNames, fileName)) {
			fmt.Fprintf(s.Conn, "553-%s\r\n", line)
		}
	}
	fmt.Fprintf(s.Conn, "553 %s: file is being uploaded by another user\r\n", fileName)
}

func writeZipIntegrityFailureDeleteResponse(conn io.Writer) {
	if conn == nil {
		return
	}
	fmt.Fprintf(conn, "426 Zip integrity check failed, deleting file\r\n")
}

func writeChecksumMismatchDeleteResponse(conn io.Writer, checksum, expectedCRC uint32) {
	if conn == nil {
		return
	}
	fmt.Fprintf(conn, "426- checksum mismatch: SLAVE: %08X SFV: %08X\r\n", checksum, expectedCRC)
	fmt.Fprintf(conn, "426 Checksum mismatch, deleting file\r\n")
}

type pendingUploadReplyChecker interface {
	PendingUploadExists(filePath string) bool
}

func pendingUploadForReply(bridge interface{}, filePath string) bool {
	checker, ok := bridge.(pendingUploadReplyChecker)
	return ok && checker.PendingUploadExists(filePath)
}

func describeTransferFailure(err error) string {
	if err == nil {
		return "unknown transfer failure"
	}

	raw := err.Error()
	lower := strings.ToLower(raw)

	switch {
	case strings.Contains(lower, "tls server handshake failed") && strings.Contains(lower, "i/o timeout"):
		return "remote peer accepted the FXP data connection but did not finish the TLS handshake in time"
	case strings.Contains(lower, "tls client handshake failed") && strings.Contains(lower, "i/o timeout"):
		return "remote peer did not complete the client-side FXP TLS handshake in time"
	case strings.Contains(lower, "tls server handshake failed") && strings.Contains(lower, "connection reset by peer"):
		return "remote peer reset the FXP connection during the TLS handshake"
	case strings.Contains(lower, "tls client handshake failed") && strings.Contains(lower, "connection reset by peer"):
		return "remote peer reset the client-side FXP connection during the TLS handshake"
	case strings.Contains(lower, "connect failed") && strings.Contains(lower, "connection refused"):
		return "remote peer advertised an FXP data port but nothing was listening on it"
	case strings.Contains(lower, "connect failed") && strings.Contains(lower, "i/o timeout"):
		return "remote peer advertised an FXP data port but never accepted the connection"
	case strings.Contains(lower, "read error") && strings.Contains(lower, "connection reset by peer"):
		return "remote peer reset the FXP data connection during transfer"
	case strings.Contains(lower, "write error") && strings.Contains(lower, "connection reset by peer"):
		return "remote peer closed the FXP data connection while we were sending data"
	case strings.Contains(lower, "write error") && strings.Contains(lower, "broken pipe"):
		return "remote peer closed the FXP data connection while we were sending data"
	case strings.Contains(raw, "The IP that connected to the socket was not the one that was expected."):
		return "a different host than the announced data peer connected to the prepared socket"
	case strings.Contains(lower, "unexpected response from slave"):
		return "slave returned an unexpected async response"
	case strings.Contains(lower, "file not found on any available slave"):
		return "file was requested before it was available on any online slave"
	case strings.Contains(lower, "receive ack:") && strings.Contains(lower, "file not found:"):
		return "slave acknowledged setup but reported the source file missing before transfer start"
	case strings.Contains(lower, "send ack:") && strings.Contains(lower, "file not found:"):
		return "slave acknowledged setup but reported the download source missing before transfer start"
	case strings.Contains(lower, "file not found: file not found:"):
		return "slave reported the requested source file missing during transfer setup"
	case strings.Contains(lower, "file not found:"):
		return "requested source file was not available on the selected slave"
	default:
		return "transfer failed for an unclassified reason"
	}
}

func formatTransferFailureLog(err error) string {
	if err == nil {
		return "unknown transfer failure"
	}
	return fmt.Sprintf("%s (raw: %v)", describeTransferFailure(err), err)
}
