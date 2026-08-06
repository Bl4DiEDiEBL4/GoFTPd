package master

import (
	"sync"
	"testing"
	"time"

	"weaveftpd/internal/protocol"
)

func TestFetchResponseReturnsBufferedEarlyResponse(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
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

func TestUpdateDiskStatusPublishesSlavePASVAddress(t *testing.T) {
	rs := &RemoteSlave{properties: make(map[string]string)}
	rs.updateDiskStatus(protocol.DiskStatus{
		SpaceAvailable: 100,
		PASVAddress:    "203.0.113.10",
	})

	if got := rs.GetPASVIP(); got != "203.0.113.10" {
		t.Fatalf("expected advertised PASV address, got %q", got)
	}
	if got := rs.GetDiskStatus().SpaceAvailable; got != 100 {
		t.Fatalf("expected disk status to be retained, got %d", got)
	}
}

func TestTimedOutRemergeLateResponseClearsState(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
		heartbeatTimeout: time.Second,
	}
	rs.setActiveRemerge("abc")
	if !rs.markActiveRemergeTimedOut("abc") {
		t.Fatalf("expected active remerge timeout marker")
	}
	if !rs.IsRemerging() {
		t.Fatalf("expected slave to remain marked remerging after timeout")
	}

	rs.routeResponse("abc", &protocol.AsyncResponse{Index: "abc"})

	if rs.IsRemerging() {
		t.Fatalf("late response should clear timed-out remerge state")
	}
	if _, ok := rs.earlyResponses.Load("abc"); ok {
		t.Fatalf("late timed-out remerge response should not stay buffered")
	}
}

func TestEarlyActiveRemergeResponseStillBuffersBeforeWaiter(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
		heartbeatTimeout: time.Second,
	}
	rs.setActiveRemerge("abc")

	rs.routeResponse("abc", &protocol.AsyncResponse{Index: "abc"})

	if _, ok := rs.earlyResponses.Load("abc"); !ok {
		t.Fatalf("early response should be buffered until FetchResponse starts")
	}
	if !rs.IsRemerging() {
		t.Fatalf("early response should not clear active remerge before waiter consumes it")
	}
}

func TestWaitForRemergeDrainReturnsAfterQueueClears(t *testing.T) {
	rs := &RemoteSlave{
		remergeDrained: make(chan struct{}, 1),
	}
	rs.online.Store(true)
	rs.remergeQueueDepth.Store(1)

	go func() {
		time.Sleep(20 * time.Millisecond)
		rs.remergeQueueDepth.Store(0)
		rs.remergeDrained <- struct{}{}
	}()

	if err := rs.WaitForRemergeDrain(200 * time.Millisecond); err != nil {
		t.Fatalf("WaitForRemergeDrain returned error: %v", err)
	}
}

// TestSetOfflineDoesNotPanicOnConcurrentRouteResponse guards against a shutdown
// racing with Run()'s read loop: SetOffline must never close a pendingCmds
// channel while routeResponse is still sending on it, since that panics
// regardless of the select/default guard on the send side.
func TestSetOfflineDoesNotPanicOnConcurrentRouteResponse(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
		remergeStop:      make(chan struct{}),
		heartbeatTimeout: time.Second,
	}
	rs.online.Store(true)
	rs.pendingCmds.Store("01", make(chan interface{}, 1))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			rs.routeResponse("01", &protocol.AsyncResponse{Index: "01"})
		}
	}()
	go func() {
		defer wg.Done()
		rs.SetOffline("concurrent shutdown")
	}()
	wg.Wait()
}

// TestRunRemergeQueueExitsOnSetOffline verifies runRemergeQueue's goroutine
// actually terminates once SetOffline closes remergeStop, instead of
// leaking forever waiting on an unclosed remergeQueue.
func TestRunRemergeQueueExitsOnSetOffline(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
		remergeStop:      make(chan struct{}),
		heartbeatTimeout: time.Second,
	}
	rs.online.Store(true)

	done := make(chan struct{})
	go func() {
		rs.runRemergeQueue(nil)
		close(done)
	}()

	rs.SetOffline("shutdown")

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runRemergeQueue goroutine leaked: did not exit after SetOffline")
	}
}

func TestEnqueueRemergeAfterOfflineDoesNotBlock(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge),
		remergeDrained:   make(chan struct{}, 1),
		remergeStop:      make(chan struct{}),
		heartbeatTimeout: time.Second,
	}
	rs.online.Store(true)
	rs.SetOffline("shutdown")

	done := make(chan struct{})
	go func() {
		rs.enqueueRemerge(nil, &protocol.AsyncResponseRemerge{})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueueRemerge blocked after SetOffline")
	}
	if got := rs.remergeQueueDepth.Load(); got != 0 {
		t.Fatalf("expected queue depth to stay 0 after dropped enqueue, got %d", got)
	}
}

// TestSetOfflineIsIdempotent verifies the CAS guard makes a second SetOffline
// call a no-op, so it can't double-close remergeStop (which would panic) or
// re-run shutdown side effects.
func TestSetOfflineIsIdempotent(t *testing.T) {
	rs := &RemoteSlave{
		commandNotify:    make(chan struct{}, 1),
		remergeQueue:     make(chan *protocol.AsyncResponseRemerge, 1),
		remergeDrained:   make(chan struct{}, 1),
		remergeStop:      make(chan struct{}),
		heartbeatTimeout: time.Second,
	}
	rs.online.Store(true)

	rs.SetOffline("first")
	if rs.IsOnline() {
		t.Fatalf("expected slave offline after first SetOffline call")
	}

	rs.SetOffline("second")
}
