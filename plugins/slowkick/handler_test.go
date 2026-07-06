package slowkick

import (
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"weaveftpd/internal/plugin"
	"weaveftpd/internal/user"
)

func testServices() *plugin.Services {
	return &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{
				{ID: 1, User: "tester", LoggedIn: true},
				{ID: 2, User: "other", LoggedIn: true},
			}
		},
	}
}

func TestCheckActiveTransfersAbortsSlowSlaveUpload(t *testing.T) {
	started := time.Now().Add(-8 * time.Second)
	var aborted bool
	var eventType string

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{{
				ID:                1,
				User:              "tester",
				PrimaryGroup:      "USERS",
				LoggedIn:          true,
				TransferDirection: "upload",
				TransferPath:      "/TV/release/file.r00",
				TransferStartedAt: started,
				TransferSlaveName: "SLAVE1",
				TransferSlaveIdx:  42,
			}}
		},
		GetLiveTransferStats: func() []plugin.LiveTransferStat {
			return []plugin.LiveTransferStat{{
				SlaveName:     "SLAVE1",
				TransferIndex: 42,
				Path:          "/TV/release/file.r00",
				StartedAt:     started,
				Transferred:   1024,
				SpeedBytes:    1024,
			}}
		},
		AbortTransfer: func(slaveName string, transferIndex int32, reason string) bool {
			aborted = true
			if slaveName != "SLAVE1" || transferIndex != 42 {
				t.Fatalf("unexpected abort target %s/%d", slaveName, transferIndex)
			}
			if !strings.Contains(reason, "slowkick") {
				t.Fatalf("expected slowkick abort reason, got %q", reason)
			}
			return true
		},
		EmitEvent: func(evtType, evtPath, filename, section string, size int64, speed float64, data map[string]string) {
			eventType = evtType
		},
	}
	h.monitorUploads = true
	h.minUsersOnline = 0
	h.minUploadSpeedBytes = 5 * 1024
	h.uploadGrace = 7 * time.Second
	h.announceKick = true
	h.tempbanAfterKick = false

	h.checkActiveTransfers(started.Add(8 * time.Second))

	if !aborted {
		t.Fatal("expected slow slave upload to be aborted")
	}
	if eventType != "SLOWUPLOADKICK" {
		t.Fatalf("expected SLOWUPLOADKICK event, got %q", eventType)
	}
}

func TestCheckActiveTransfersSkipsExcludedExtension(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	var aborted bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{{
				ID:                1,
				User:              "tester",
				PrimaryGroup:      "USERS",
				LoggedIn:          true,
				TransferDirection: "upload",
				TransferPath:      "/XXX/release/release.nfo",
				TransferStartedAt: started,
			}}
		},
		DisconnectSession: func(id uint64) bool {
			aborted = true
			return true
		},
	}
	h.monitorUploads = true
	h.minUsersOnline = 0
	h.minUploadSpeedBytes = 5 * 1024
	h.excludeExtensions = lowerSet([]string{"sfv", "nfo"})

	h.checkActiveTransfers(started.Add(10 * time.Second))

	if aborted {
		t.Fatal("expected excluded extension to skip slowkick")
	}
}

func TestCheckActiveTransfersSkipsWhenUsersBelowMinimum(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	var aborted bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{{
				ID:                1,
				User:              "tester",
				PrimaryGroup:      "USERS",
				LoggedIn:          true,
				TransferDirection: "upload",
				TransferPath:      "/TV/release/file.r00",
				TransferStartedAt: started,
			}}
		},
		DisconnectSession: func(id uint64) bool {
			aborted = true
			return true
		},
	}
	h.monitorUploads = true
	h.minUsersOnline = 2
	h.minUploadSpeedBytes = 5 * 1024

	h.checkActiveTransfers(started.Add(10 * time.Second))

	if aborted {
		t.Fatal("expected slowkick to stay disabled below min users")
	}
}

func TestHandleSlowTransferSetsTempBanAndEmitsKick(t *testing.T) {
	var eventType string
	var eventPath string
	var eventData map[string]string

	h := New()
	h.svc = testServices()
	h.svc.EmitEvent = func(evtType, evtPath, filename, section string, size int64, speed float64, data map[string]string) {
		eventType = evtType
		eventPath = evtPath
		eventData = data
	}
	h.monitorUploads = true
	h.minUsersOnline = 2
	h.minUploadSpeedBytes = 5 * 1024
	h.announceKick = true
	h.tempbanAfterKick = true
	h.tempbanDuration = 5 * time.Second

	h.HandleSlowTransfer("tester", "USERS", "/TV/release/file.r00", "upload", "LOCAL", 42, 1024, 5*1024)

	if eventType != "SLOWUPLOADKICK" {
		t.Fatalf("expected SLOWUPLOADKICK event, got %q", eventType)
	}
	if eventPath != "/TV/release/file.r00" {
		t.Fatalf("expected event path to match transfer path, got %q", eventPath)
	}
	if eventData["tempban_seconds"] != "5" {
		t.Fatalf("expected tempban_seconds=5, got %+v", eventData)
	}
	if err := h.ValidateLogin(&user.User{Name: "tester", PrimaryGroup: "USERS"}, "127.0.0.1"); err == nil {
		t.Fatal("expected kicked user to be tempbanned")
	}
}

func TestHandleSlowTransferSkipsExcludedPath(t *testing.T) {
	var emitted bool

	h := New()
	h.svc = testServices()
	h.svc.EmitEvent = func(evtType, evtPath, filename, section string, size int64, speed float64, data map[string]string) {
		emitted = true
	}
	h.monitorUploads = true
	h.minUsersOnline = 2
	h.minUploadSpeedBytes = 5 * 1024
	h.announceKick = true
	h.tempbanAfterKick = true
	h.tempbanDuration = 5 * time.Second

	h.HandleSlowTransfer("tester", "USERS", "/REQUESTS/Test/file.r00", "upload", "LOCAL", 42, 1024, 5*1024)

	if emitted {
		t.Fatal("did not expect event for excluded path")
	}
	if err := h.ValidateLogin(&user.User{Name: "tester", PrimaryGroup: "USERS"}, "127.0.0.1"); err != nil {
		t.Fatalf("did not expect excluded path to tempban user, got %v", err)
	}
}

func TestValidateLoginAllowsUserAfterTempBanExpires(t *testing.T) {
	h := New()
	h.tempbanAfterKick = true
	h.tempbanDuration = 15 * time.Second
	h.excludeUsers = map[string]struct{}{}
	h.excludeGroups = map[string]struct{}{}
	h.tempBans = map[string]time.Time{
		"slowpoke": time.Now().Add(-time.Second),
	}

	if err := h.ValidateLogin(&user.User{Name: "slowpoke", PrimaryGroup: "USERS"}, "127.0.0.1"); err != nil {
		t.Fatalf("expected expired tempban to be ignored, got %v", err)
	}
}

func TestValidateLoginReturnsRemainingSeconds(t *testing.T) {
	h := New()
	h.tempbanAfterKick = true
	h.tempbanDuration = 15 * time.Second
	h.excludeUsers = map[string]struct{}{}
	h.excludeGroups = map[string]struct{}{}
	h.tempBans = map[string]time.Time{
		"slowpoke": time.Now().Add(5 * time.Second),
	}

	err := h.ValidateLogin(&user.User{Name: "slowpoke", PrimaryGroup: "USERS"}, "127.0.0.1")
	if err == nil {
		t.Fatal("expected active tempban to reject login")
	}
	if !strings.Contains(err.Error(), "retry in") {
		t.Fatalf("expected retry hint in error, got %v", err)
	}
}

func TestReloadConfigCanDisableSlowkick(t *testing.T) {
	h := New()
	h.svc = testServices()
	h.setTempBan("tester", time.Now().Add(time.Minute))

	if err := h.ReloadConfig(map[string]interface{}{
		"enabled":          false,
		"min_users_online": 0,
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}

	if err := h.ValidateLogin(&user.User{Name: "tester", PrimaryGroup: "USERS"}, "127.0.0.1"); err != nil {
		t.Fatalf("expected disabled slowkick to ignore tempban, got %v", err)
	}
}

func TestReloadConfigCanClearDefaultExclusions(t *testing.T) {
	started := time.Now().Add(-10 * time.Second)
	var aborted bool

	h := New()
	h.svc = &plugin.Services{
		Logger: log.New(os.Stderr, "", 0),
		ListActiveSessions: func() []plugin.ActiveSession {
			return []plugin.ActiveSession{{
				ID:                1,
				User:              "tester",
				PrimaryGroup:      "USERS",
				LoggedIn:          true,
				TransferDirection: "upload",
				TransferPath:      "/REQUESTS/Test/file.sfv",
				TransferStartedAt: started,
			}}
		},
		DisconnectSession: func(id uint64) bool {
			aborted = true
			return true
		},
	}

	h.checkActiveTransfers(started.Add(10 * time.Second))
	if aborted {
		t.Fatal("expected default request/sfv exclusions before reload")
	}

	if err := h.ReloadConfig(map[string]interface{}{
		"monitor_uploads":         true,
		"min_upload_speed_kbps":   5,
		"min_users_online":        0,
		"exclude_paths":           []interface{}{},
		"exclude_extensions":      []interface{}{},
		"tempban_after_kick":      false,
		"verify_upload_seconds":   3,
		"verify_download_seconds": 3,
	}); err != nil {
		t.Fatalf("ReloadConfig failed: %v", err)
	}

	h.checkActiveTransfers(started.Add(10 * time.Second))

	if !aborted {
		t.Fatal("expected cleared exclusions to allow slowkick")
	}
}
