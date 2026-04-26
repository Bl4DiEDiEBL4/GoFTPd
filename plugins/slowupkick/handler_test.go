package slowupkick

import (
	"log"
	"os"
	"testing"
	"time"

	"goftpd/internal/plugin"
)

func TestEvaluateKicksVerifiedSlowUpload(t *testing.T) {
	var aborted bool
	var disconnected bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{
				{
					ID:                1,
					User:              "slowpoke",
					PrimaryGroup:      "USERS",
					LoggedIn:          true,
					TransferDirection: "upload",
					TransferPath:      "/0DAY/2026-04-26/Test.Release-GRP/file.r00",
					TransferSlaveName: "SLAVE1",
					TransferSlaveIdx:  42,
				},
				{
					ID:       2,
					User:     "other",
					LoggedIn: true,
				},
			}
		},
		GetLiveTransferStats: func() []plugin.LiveTransferStat {
			return []plugin.LiveTransferStat{
				{
					SlaveName:     "SLAVE1",
					TransferIndex: 42,
					Direction:     "upload",
					SpeedBytes:    10 * 1024,
				},
			}
		},
		AbortTransfer: func(slaveName string, transferIndex int32, reason string) bool {
			aborted = slaveName == "SLAVE1" && transferIndex == 42
			return true
		},
		DisconnectSession: func(id uint64) bool {
			disconnected = id == 1
			return true
		},
	}
	h.enabled = true
	h.interval = time.Second
	h.monitorUploads = true
	h.monitorDownloads = true
	h.uploadVerifyDelay = 5 * time.Second
	h.minUploadSpeedBytes = 25 * 1024
	h.minUsersOnline = 2
	h.excludeUsers = map[string]struct{}{}
	h.excludeGroups = map[string]struct{}{}
	h.excludePaths = normalizePaths([]string{"/PRE", "/REQUESTS", "/SPEEDTEST"})
	h.candidates = map[uint64]candidate{
		1: {
			SessionID: 1,
			User:      "slowpoke",
			Path:      "/0DAY/2026-04-26/Test.Release-GRP/file.r00",
			Direction: "upload",
			FirstSeen: time.Now().Add(-10 * time.Second),
		},
	}

	h.evaluate(time.Now())

	if !aborted {
		t.Fatalf("expected abort callback to be called")
	}
	if !disconnected {
		t.Fatalf("expected disconnect callback to be called")
	}
	if _, ok := h.getCandidate(1); ok {
		t.Fatalf("expected candidate to be removed after kick")
	}
}

func TestEvaluateSkipsExcludedPath(t *testing.T) {
	var aborted bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{
				{
					ID:                1,
					User:              "slowpoke",
					PrimaryGroup:      "USERS",
					LoggedIn:          true,
					TransferDirection: "upload",
					TransferPath:      "/REQUESTS/Test/file.r00",
					TransferSlaveName: "SLAVE1",
					TransferSlaveIdx:  42,
				},
				{
					ID:       2,
					User:     "other",
					LoggedIn: true,
				},
			}
		},
		GetLiveTransferStats: func() []plugin.LiveTransferStat {
			return []plugin.LiveTransferStat{
				{
					SlaveName:     "SLAVE1",
					TransferIndex: 42,
					Direction:     "upload",
					SpeedBytes:    10 * 1024,
				},
			}
		},
		AbortTransfer: func(slaveName string, transferIndex int32, reason string) bool {
			aborted = true
			return true
		},
		DisconnectSession: func(id uint64) bool { return true },
	}
	h.enabled = true
	h.monitorUploads = true
	h.monitorDownloads = true
	h.uploadVerifyDelay = 5 * time.Second
	h.minUploadSpeedBytes = 25 * 1024
	h.minUsersOnline = 2
	h.excludeUsers = map[string]struct{}{}
	h.excludeGroups = map[string]struct{}{}
	h.excludePaths = normalizePaths([]string{"/PRE", "/REQUESTS", "/SPEEDTEST"})
	h.candidates = map[uint64]candidate{}

	h.evaluate(time.Now())

	if aborted {
		t.Fatalf("did not expect excluded path to be kicked")
	}
	if len(h.candidates) != 0 {
		t.Fatalf("did not expect excluded path to enter candidate tracking")
	}
}

func TestEvaluateKicksVerifiedSlowDownload(t *testing.T) {
	var aborted bool
	var disconnected bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{
				{
					ID:                7,
					User:              "crawler",
					PrimaryGroup:      "USERS",
					LoggedIn:          true,
					TransferDirection: "download",
					TransferPath:      "/0DAY/2026-04-26/Test.Release-GRP/file.r00",
					TransferSlaveName: "SLAVE1",
					TransferSlaveIdx:  99,
				},
				{
					ID:       8,
					User:     "other",
					LoggedIn: true,
				},
			}
		},
		GetLiveTransferStats: func() []plugin.LiveTransferStat {
			return []plugin.LiveTransferStat{
				{
					SlaveName:     "SLAVE1",
					TransferIndex: 99,
					Direction:     "download",
					SpeedBytes:    10 * 1024,
				},
			}
		},
		AbortTransfer: func(slaveName string, transferIndex int32, reason string) bool {
			aborted = slaveName == "SLAVE1" && transferIndex == 99
			return true
		},
		DisconnectSession: func(id uint64) bool {
			disconnected = id == 7
			return true
		},
	}
	h.enabled = true
	h.monitorUploads = true
	h.monitorDownloads = true
	h.downloadVerifyDelay = 5 * time.Second
	h.minDownloadSpeedBytes = 50 * 1024
	h.minUsersOnline = 2
	h.excludeUsers = map[string]struct{}{}
	h.excludeGroups = map[string]struct{}{}
	h.excludePaths = normalizePaths([]string{"/PRE", "/REQUESTS", "/SPEEDTEST"})
	h.candidates = map[uint64]candidate{
		7: {
			SessionID: 7,
			User:      "crawler",
			Path:      "/0DAY/2026-04-26/Test.Release-GRP/file.r00",
			Direction: "download",
			FirstSeen: time.Now().Add(-10 * time.Second),
		},
	}

	h.evaluate(time.Now())

	if !aborted {
		t.Fatalf("expected download abort callback to be called")
	}
	if !disconnected {
		t.Fatalf("expected download disconnect callback to be called")
	}
}
