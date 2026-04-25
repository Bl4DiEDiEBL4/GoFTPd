package master

import (
	"testing"
	"time"

	"goftpd/internal/protocol"
)

func TestFetchResponseReturnsBufferedEarlyResponse(t *testing.T) {
	rs := &RemoteSlave{
		indexPool:        make(chan string, 1),
		commandNotify:    make(chan struct{}, 1),
		heartbeatTimeout: time.Second,
	}

	rs.routeResponse("05", &protocol.AsyncResponse{Index: "05"})

	resp, err := rs.FetchResponse("05", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("FetchResponse returned error: %v", err)
	}

	got, ok := resp.(*protocol.AsyncResponse)
	if !ok {
		t.Fatalf("expected *protocol.AsyncResponse, got %T", resp)
	}
	if got.Index != "05" {
		t.Fatalf("expected response index 05, got %q", got.Index)
	}
}
