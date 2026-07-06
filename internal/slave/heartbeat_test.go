package slave

import (
	"net"
	"testing"

	"weaveftpd/internal/protocol"
)

func TestWriteObjectNoActivityRefreshesHeartbeatActivity(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	s := &Slave{stream: protocol.NewObjectStream(left)}
	done := make(chan error, 1)
	go func() {
		_, err := protocol.NewObjectStream(right).ReadObject()
		done <- err
	}()

	if err := s.writeObjectNoActivity(&protocol.AsyncResponseDiskStatus{}); err != nil {
		t.Fatalf("writeObjectNoActivity() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ReadObject() error = %v", err)
	}
	if got := s.lastWriteTime.Load(); got <= 0 {
		t.Fatalf("lastWriteTime = %d, want updated timestamp", got)
	}
}
