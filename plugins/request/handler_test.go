package request

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"weaveftpd/internal/plugin"
)

func TestRequestStoresMetadata(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Some.Release-TEST"})

	if len(bridge.requestData) != 1 {
		t.Fatalf("expected one request metadata entry, got %+v", bridge.requestData)
	}
	if bridge.requestData[0].Release != "Some.Release-TEST" || bridge.requestData[0].By != "alice" {
		t.Fatalf("unexpected request metadata %+v", bridge.requestData[0])
	}
	if _, ok := bridge.files["/REQUESTS/.requests"]; ok {
		t.Fatalf("did not expect legacy request sidecar file to be written")
	}
	if _, ok := bridge.dirs["/REQUESTS/REQ-Some.Release-TEST"]; !ok {
		t.Fatalf("expected request directory to be created")
	}
	if got := bridge.dirs["/REQUESTS/REQ-Some.Release-TEST"].Owner; got != "alice" {
		t.Fatalf("expected request dir owner to be FTP user, got %q", got)
	}
}

func TestFTPRequestEmitsAnnounceEvent(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	var eventType, eventPath string
	var eventData map[string]string
	p.svc = &plugin.Services{
		Bridge: bridge,
		EmitEvent: func(evtType, evtPath, filename, section string, size int64, speed float64, data map[string]string) {
			eventType = evtType
			eventPath = evtPath
			eventData = data
		},
	}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Some.Release-TEST"})

	if eventType != "CUSTOM" {
		t.Fatalf("expected CUSTOM event, got %q", eventType)
	}
	if eventPath != "/REQUESTS/REQ-Some.Release-TEST" {
		t.Fatalf("unexpected event path %q", eventPath)
	}
	if eventData["template"] != "REQUESTADD" || eventData["requester"] != "alice" {
		t.Fatalf("unexpected request event data %#v", eventData)
	}
}

func TestFTPReqFillEmitsAnnounceEvent(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	var eventType, eventPath string
	var eventData map[string]string
	p.svc = &plugin.Services{
		Bridge: bridge,
		EmitEvent: func(evtType, evtPath, filename, section string, size int64, speed float64, data map[string]string) {
			eventType = evtType
			eventPath = evtPath
			eventData = data
		},
	}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Some.Release-TEST"})
	bridge.files["/REQUESTS/REQ-Some.Release-TEST/file.rar"] = []byte("data")

	eventType = ""
	eventPath = ""
	eventData = nil
	p.HandleSiteCommand(ctx, "REQFILL", []string{"Some.Release-TEST"})

	if eventType != "CUSTOM" {
		t.Fatalf("expected CUSTOM event, got %q", eventType)
	}
	if eventPath != "/REQUESTS/FILLED-Some.Release-TEST" {
		t.Fatalf("unexpected event path %q", eventPath)
	}
	if eventData["template"] != "REQUESTFILL" || eventData["filled_by"] != "alice" {
		t.Fatalf("unexpected fill event data %#v", eventData)
	}
}

func TestProxyRequestDoesNotEmitDuplicateAnnounceEvent(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	emitted := false
	p.svc = &plugin.Services{
		Bridge: bridge,
		EmitEvent: func(string, string, string, string, int64, float64, map[string]string) {
			emitted = true
		},
	}

	bot := &requestTestCtx{user: "weaveftpd", group: "sitebot", flags: "1"}
	p.HandleSiteCommand(bot, "REQUEST", []string{"-by:ircUser", "Proxy.Request-TEST"})

	if emitted {
		t.Fatalf("did not expect proxied sitebot request to emit a duplicate announce event")
	}
}

func TestReqFillRecoversExistingRequestDirWithoutMetadata(t *testing.T) {
	bridge := newRequestTestBridge()
	bridge.addDir("/REQUESTS", "WeaveFTPd", "WeaveFTPd")
	bridge.addDir("/REQUESTS/REQ-Old.Release-TEST", "alice", "iND")
	bridge.files["/REQUESTS/REQ-Old.Release-TEST/file.rar"] = []byte("data")

	p := New()
	p.svc = &plugin.Services{Bridge: bridge}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQFILL", []string{"Old.Release-TEST"})

	if _, ok := bridge.dirs["/REQUESTS/FILLED-Old.Release-TEST"]; !ok {
		t.Fatalf("expected existing request dir to be renamed to filled dir")
	}
	if len(bridge.fillData) != 1 || bridge.fillData[0].FilledBy != "alice" {
		t.Fatalf("expected fill metadata to track filler, got %+v", bridge.fillData)
	}
	if _, ok := bridge.files["/REQUESTS/.reqfills"]; ok {
		t.Fatalf("did not expect legacy fill sidecar file to be written")
	}

	p.HandleSiteCommand(ctx, "REQTOP", nil)
	if !ctx.hasReply("alice - 1 fill(s)") {
		t.Fatalf("expected REQTOP to show filler, got %#v", ctx.replies)
	}
}

func TestRequestStateFileHydratesFillStats(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request_state.json")
	bridge := newRequestTestBridge()
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}
	p.stateFile = statePath

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Saved.Release-TEST"})
	bridge.files["/REQUESTS/REQ-Saved.Release-TEST/file.rar"] = []byte("data")
	p.HandleSiteCommand(ctx, "REQFILL", []string{"Saved.Release-TEST"})

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("expected request state file: %v", err)
	}
	var state requestStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode request state: %v", err)
	}
	if len(state.Fills) != 1 || state.Fills[0].FilledBy != "alice" {
		t.Fatalf("unexpected saved fill state %+v", state.Fills)
	}

	loadedBridge := newRequestTestBridge()
	loaded := New()
	loaded.svc = &plugin.Services{Bridge: loadedBridge}
	loaded.stateFile = statePath
	if err := loaded.hydrateRequestState(); err != nil {
		t.Fatalf("hydrate request state: %v", err)
	}
	if len(loadedBridge.fillData) != 1 || loadedBridge.fillData[0].FilledBy != "alice" {
		t.Fatalf("expected fill metadata to hydrate, got %+v", loadedBridge.fillData)
	}

	loadedCtx := &requestTestCtx{user: "siteop", group: "iND", flags: "1"}
	loaded.HandleSiteCommand(loadedCtx, "REQTOP", nil)
	if !loadedCtx.hasReply("alice - 1 fill(s)") {
		t.Fatalf("expected REQTOP to show hydrated filler, got %#v", loadedCtx.replies)
	}
}

func TestRequestStateHydrateDedupesRestoredFillStats(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "request_state.json")
	if err := os.WriteFile(statePath, []byte(`{
  "version": 1,
  "dir": "/REQUESTS",
  "requests": [],
  "fills": [
    {
      "Release": "Fire.Birds.1990.MULTi.COMPLETE.UHD.BLURAY-MONUMENT",
      "RequestedBy": "restore",
      "FilledBy": "Hawkeye",
      "Date": "2026-06-24 00:58"
    },
    {
      "Release": "Angels.Egg.1985.ANiME.MULTi.COMPLETE.BLURAY-MONUMENT",
      "RequestedBy": "restore",
      "FilledBy": "Hawkeye",
      "Date": "2026-06-22 04:01"
    }
  ]
}
`), 0644); err != nil {
		t.Fatalf("write request state: %v", err)
	}

	bridge := newRequestTestBridge()
	bridge.fillData = []plugin.RequestFillRecord{
		{
			Release:     "Fire.Birds.1990.MULTi.COMPLETE.UHD.BLURAY-MONUMENT",
			RequestedBy: "CokxOk",
			FilledBy:    "Hawkeye",
			Date:        "2026-06-24 00:58",
		},
		{
			Release:     "Angels.Egg.1985.ANiME.MULTi.COMPLETE.BLURAY-MONUMENT",
			RequestedBy: "unknown",
			FilledBy:    "Hawkeye",
			Date:        "2026-06-22 04:01",
		},
	}
	loaded := New()
	loaded.svc = &plugin.Services{Bridge: bridge}
	loaded.stateFile = statePath
	if err := loaded.hydrateRequestState(); err != nil {
		t.Fatalf("hydrate request state: %v", err)
	}

	if len(bridge.fillData) != 2 {
		t.Fatalf("expected duplicate fill records to collapse, got %+v", bridge.fillData)
	}
	for _, fill := range bridge.fillData {
		if fill.Release == "Fire.Birds.1990.MULTi.COMPLETE.UHD.BLURAY-MONUMENT" && fill.RequestedBy != "CokxOk" {
			t.Fatalf("expected stronger requester to win, got %+v", fill)
		}
	}

	ctx := &requestTestCtx{user: "siteop", group: "iND", flags: "1"}
	loaded.HandleSiteCommand(ctx, "REQTOP", nil)
	if !ctx.hasReply("Hawkeye - 2 fill(s)") {
		t.Fatalf("expected deduped REQTOP count, got %#v", ctx.replies)
	}
}

func TestDuplicateRequestRepairsMissingDirectory(t *testing.T) {
	bridge := newRequestTestBridge()
	bridge.addDir("/REQUESTS", "WeaveFTPd", "WeaveFTPd")
	bridge.requestData = []plugin.RequestRecord{{
		Release: "Repair.Release-TEST",
		By:      "alice",
		Mode:    "gl",
		Date:    "2026-05-16 02:00",
	}}
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Repair.Release-TEST"})

	if _, ok := bridge.dirs["/REQUESTS/REQ-Repair.Release-TEST"]; !ok {
		t.Fatalf("expected missing request directory to be repaired")
	}
	if !ctx.hasReply("has already been requested") {
		t.Fatalf("expected duplicate reply, got %#v", ctx.replies)
	}
}

func TestReqFillProxyTracksProvidedUser(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	emitted := false
	p.svc = &plugin.Services{
		Bridge: bridge,
		EmitEvent: func(string, string, string, string, int64, float64, map[string]string) {
			emitted = true
		},
	}

	requester := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(requester, "REQUEST", []string{"Proxy.Release-TEST"})
	bridge.files["/REQUESTS/REQ-Proxy.Release-TEST/file.rar"] = []byte("data")
	emitted = false

	bot := &requestTestCtx{user: "weaveftpd", group: "sitebot", flags: "1"}
	p.HandleSiteCommand(bot, "REQFILL", []string{"-by:ircUser", "Proxy.Release-TEST"})

	if len(bridge.fillData) != 1 || bridge.fillData[0].FilledBy != "ircUser" {
		t.Fatalf("expected proxy filler to be tracked, got %+v", bridge.fillData)
	}
	if emitted {
		t.Fatalf("did not expect proxied reqfill to emit a duplicate announce event")
	}
}

func TestRequestProxyTracksProvidedUser(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}

	bot := &requestTestCtx{user: "weaveftpd", group: "sitebot", flags: "1"}
	p.HandleSiteCommand(bot, "REQUEST", []string{"-by:ircUser", "Proxy.Request-TEST"})

	if len(bridge.requestData) != 1 || bridge.requestData[0].By != "ircUser" {
		t.Fatalf("expected proxied requester to be tracked, got %+v", bridge.requestData)
	}
	dir := bridge.dirs["/REQUESTS/REQ-Proxy.Request-TEST"]
	if dir.Owner != "ircUser" {
		t.Fatalf("expected request dir owner to be proxied user, got %q", dir.Owner)
	}
}

func TestRequestUsesConfiguredStorageSlave(t *testing.T) {
	bridge := newRequestTestBridge()
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}
	p.storageSlave = "LOCAL"

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQUEST", []string{"Pinned.Release-TEST"})

	if got := bridge.dirSlaves["/REQUESTS"]; got != "LOCAL" {
		t.Fatalf("expected base dir on LOCAL, got %q", got)
	}
	if got := bridge.dirSlaves["/REQUESTS/REQ-Pinned.Release-TEST"]; got != "LOCAL" {
		t.Fatalf("expected request dir on LOCAL, got %q", got)
	}
	if len(bridge.requestData) != 1 || bridge.requestData[0].Release != "Pinned.Release-TEST" {
		t.Fatalf("expected request metadata to be stored, got %+v", bridge.requestData)
	}
}

func TestReqFillRelocatesMixedSlaveRequestTreeBeforeRename(t *testing.T) {
	bridge := newRequestTestBridge()
	bridge.addDir("/REQUESTS", "WeaveFTPd", "WeaveFTPd")
	bridge.addDirOnSlave("/REQUESTS/REQ-Mixed.Release-TEST", "alice", "iND", "LOCAL")
	bridge.addDirOnSlave("/REQUESTS/REQ-Mixed.Release-TEST/Mixed.Release-TEST", "alice", "iND", "OTHER")
	bridge.addFileOnSlave("/REQUESTS/REQ-Mixed.Release-TEST/Mixed.Release-TEST/file.r00", []byte("data"), "OTHER")
	p := New()
	p.svc = &plugin.Services{Bridge: bridge}

	ctx := &requestTestCtx{user: "alice", group: "iND", flags: "1"}
	p.HandleSiteCommand(ctx, "REQFILL", []string{"Mixed.Release-TEST"})

	if got := bridge.fileSlaves["/REQUESTS/FILLED-Mixed.Release-TEST/Mixed.Release-TEST/file.r00"]; got != "LOCAL" {
		t.Fatalf("expected filled request payload to be relocated to LOCAL, got %q", got)
	}
	if _, ok := bridge.files["/REQUESTS/FILLED-Mixed.Release-TEST/Mixed.Release-TEST/file.r00"]; !ok {
		t.Fatalf("expected relocated file under filled request path")
	}
}

type requestTestCtx struct {
	user    string
	group   string
	flags   string
	replies []string
}

func (c *requestTestCtx) Reply(format string, args ...interface{}) {
	c.replies = append(c.replies, fmt.Sprintf(format, args...))
}

func (c *requestTestCtx) UserName() string         { return c.user }
func (c *requestTestCtx) UserFlags() string        { return c.flags }
func (c *requestTestCtx) UserPrimaryGroup() string { return c.group }
func (c *requestTestCtx) UserGroups() []string     { return []string{c.group} }

func (c *requestTestCtx) hasReply(needle string) bool {
	for _, reply := range c.replies {
		if strings.Contains(reply, needle) {
			return true
		}
	}
	return false
}

type requestTestBridge struct {
	dirs        map[string]plugin.FileEntry
	files       map[string][]byte
	dirSlaves   map[string]string
	fileSlaves  map[string]string
	requestData []plugin.RequestRecord
	fillData    []plugin.RequestFillRecord
}

func newRequestTestBridge() *requestTestBridge {
	return &requestTestBridge{
		dirs:       map[string]plugin.FileEntry{"/": {Name: "/", IsDir: true}},
		files:      map[string][]byte{},
		dirSlaves:  map[string]string{},
		fileSlaves: map[string]string{},
	}
}

func (b *requestTestBridge) addDir(dirPath, owner, group string) {
	dirPath = cleanAbs(dirPath)
	b.dirs[dirPath] = plugin.FileEntry{
		Name:    path.Base(dirPath),
		IsDir:   true,
		Owner:   owner,
		Group:   group,
		ModTime: 1,
	}
}

func (b *requestTestBridge) addDirOnSlave(dirPath, owner, group, slave string) {
	b.addDir(dirPath, owner, group)
	b.dirs[cleanAbs(dirPath)] = plugin.FileEntry{
		Name:    path.Base(cleanAbs(dirPath)),
		IsDir:   true,
		Owner:   owner,
		Group:   group,
		ModTime: 1,
		Slave:   slave,
	}
	b.dirSlaves[cleanAbs(dirPath)] = slave
}

func (b *requestTestBridge) addFileOnSlave(filePath string, content []byte, slave string) {
	b.files[cleanAbs(filePath)] = append([]byte(nil), content...)
	b.fileSlaves[cleanAbs(filePath)] = slave
}

func (b *requestTestBridge) PluginListDir(dirPath string) []plugin.FileEntry {
	dirPath = cleanAbs(dirPath)
	var out []plugin.FileEntry
	for childPath, entry := range b.dirs {
		if childPath == "/" {
			continue
		}
		if path.Dir(childPath) == dirPath {
			out = append(out, entry)
		}
	}
	for filePath, data := range b.files {
		if path.Dir(filePath) == dirPath {
			out = append(out, plugin.FileEntry{
				Name:  path.Base(filePath),
				Size:  int64(len(data)),
				Slave: b.fileSlaves[cleanAbs(filePath)],
			})
		}
	}
	return out
}

func (b *requestTestBridge) MakeDir(dirPath, owner, group string) error {
	dirPath = cleanAbs(dirPath)
	if parent := path.Dir(dirPath); parent != "." && parent != dirPath {
		for parent != "/" && parent != "." {
			if _, ok := b.dirs[parent]; ok {
				break
			}
			b.addDir(parent, owner, group)
			parent = path.Dir(parent)
		}
	}
	if existing, ok := b.dirs[dirPath]; ok {
		existing.Owner = owner
		existing.Group = group
		b.dirs[dirPath] = existing
		return nil
	}
	b.addDir(dirPath, owner, group)
	return nil
}

func (b *requestTestBridge) MakeDirOnSlave(dirPath, owner, group, slaveName string) error {
	if err := b.MakeDir(dirPath, owner, group); err != nil {
		return err
	}
	b.dirSlaves[cleanAbs(dirPath)] = slaveName
	return nil
}

func (b *requestTestBridge) Symlink(string, string) error                         { return nil }
func (b *requestTestBridge) VFSSymlink(string, string) error                      { return nil }
func (b *requestTestBridge) Chmod(string, uint32) error                           { return nil }
func (b *requestTestBridge) CreateSparseFile(string, int64, string, string) error { return nil }

func (b *requestTestBridge) DeleteFile(target string) error {
	target = cleanAbs(target)
	delete(b.files, target)
	delete(b.dirs, target)
	prefix := strings.TrimRight(target, "/") + "/"
	for filePath := range b.files {
		if strings.HasPrefix(filePath, prefix) {
			delete(b.files, filePath)
		}
	}
	for dirPath := range b.dirs {
		if strings.HasPrefix(dirPath, prefix) {
			delete(b.dirs, dirPath)
		}
	}
	return nil
}

func (b *requestTestBridge) RenameFile(from, toDir, toName string) error {
	from = cleanAbs(from)
	to := cleanAbs(path.Join(toDir, toName))
	entry, ok := b.dirs[from]
	if !ok {
		return fmt.Errorf("path not found: %s", from)
	}
	delete(b.dirs, from)
	entry.Name = path.Base(to)
	b.dirs[to] = entry
	if slave, ok := b.dirSlaves[from]; ok {
		b.dirSlaves[to] = slave
		delete(b.dirSlaves, from)
	}

	fromPrefix := strings.TrimRight(from, "/") + "/"
	toPrefix := strings.TrimRight(to, "/") + "/"
	fileMoves := make(map[string][]byte)
	fileSlaves := make(map[string]string)
	for filePath, data := range b.files {
		if strings.HasPrefix(filePath, fromPrefix) {
			rel := strings.TrimPrefix(filePath, fromPrefix)
			newPath := toPrefix + rel
			fileMoves[newPath] = data
			if slave, ok := b.fileSlaves[filePath]; ok {
				fileSlaves[newPath] = slave
				delete(b.fileSlaves, filePath)
			}
			delete(b.files, filePath)
		}
	}
	for newPath, data := range fileMoves {
		b.files[newPath] = data
	}
	for newPath, slave := range fileSlaves {
		b.fileSlaves[newPath] = slave
	}
	dirMoves := make(map[string]plugin.FileEntry)
	dirSlaves := make(map[string]string)
	for dirPath, dirEntry := range b.dirs {
		if strings.HasPrefix(dirPath, fromPrefix) {
			rel := strings.TrimPrefix(dirPath, fromPrefix)
			newPath := toPrefix + rel
			delete(b.dirs, dirPath)
			dirEntry.Name = path.Base(newPath)
			dirMoves[newPath] = dirEntry
			if slave, ok := b.dirSlaves[dirPath]; ok {
				dirSlaves[newPath] = slave
				delete(b.dirSlaves, dirPath)
			}
		}
	}
	for newPath, dirEntry := range dirMoves {
		b.dirs[newPath] = dirEntry
	}
	for newPath, slave := range dirSlaves {
		b.dirSlaves[newPath] = slave
	}
	return nil
}

func (b *requestTestBridge) RelocatePath(string, string, string) error { return nil }
func (b *requestTestBridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error {
	from = cleanAbs(from)
	to := cleanAbs(path.Join(toDir, toName))
	if data, ok := b.files[from]; ok {
		delete(b.files, from)
		b.files[to] = data
		b.fileSlaves[to] = targetSlave
		delete(b.fileSlaves, from)
		return nil
	}
	if entry, ok := b.dirs[from]; ok {
		delete(b.dirs, from)
		entry.Name = path.Base(to)
		b.dirs[to] = entry
		b.dirSlaves[to] = targetSlave
		delete(b.dirSlaves, from)
		fromPrefix := strings.TrimRight(from, "/") + "/"
		for filePath := range b.files {
			if strings.HasPrefix(filePath, fromPrefix) {
				b.fileSlaves[filePath] = targetSlave
			}
		}
		for dirPath := range b.dirs {
			if strings.HasPrefix(dirPath, fromPrefix) {
				b.dirSlaves[dirPath] = targetSlave
			}
		}
		return nil
	}
	return fmt.Errorf("path not found: %s", from)
}

func (b *requestTestBridge) WriteFile(filePath string, content []byte) error {
	filePath = cleanAbs(filePath)
	b.files[filePath] = append([]byte(nil), content...)
	return nil
}

func (b *requestTestBridge) ReadFile(filePath string) ([]byte, error) {
	filePath = cleanAbs(filePath)
	data, ok := b.files[filePath]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", filePath)
	}
	return append([]byte(nil), data...), nil
}

func (b *requestTestBridge) ProbeMediaInfo(string, string, int) (map[string]string, error) {
	return nil, nil
}
func (b *requestTestBridge) CacheMediaInfo(string, map[string]string) {}
func (b *requestTestBridge) ScrubReleaseRaceMetadata(string, string, string) error {
	return nil
}
func (b *requestTestBridge) FileExists(p string) bool {
	p = cleanAbs(p)
	_, fileOK := b.files[p]
	_, dirOK := b.dirs[p]
	return fileOK || dirOK
}
func (b *requestTestBridge) GetFileSize(p string) int64 {
	p = cleanAbs(p)
	if data, ok := b.files[p]; ok {
		return int64(len(data))
	}
	return -1
}
func (b *requestTestBridge) GetSFVData(string) map[string]uint32 { return nil }
func (b *requestTestBridge) GetRequestData(string) ([]plugin.RequestRecord, []plugin.RequestFillRecord) {
	return append([]plugin.RequestRecord(nil), b.requestData...), append([]plugin.RequestFillRecord(nil), b.fillData...)
}
func (b *requestTestBridge) SetRequestData(_ string, requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) {
	b.requestData = append([]plugin.RequestRecord(nil), requests...)
	b.fillData = append([]plugin.RequestFillRecord(nil), fills...)
}
func (b *requestTestBridge) GetDirMediaInfo(string) map[string]string { return nil }
func (b *requestTestBridge) PluginGetVFSRaceStats(string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	return nil, nil, 0, 0, 0
}
func (b *requestTestBridge) PluginGetVFSReleaseStats(dirPath string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	return b.PluginGetVFSRaceStats(dirPath)
}
