package speedtest

import (
	"sync"
	"testing"

	"weaveftpd/internal/plugin"
)

type testBridge struct {
	mu      sync.Mutex
	deleted []string
}

func (b *testBridge) PluginListDir(path string) []plugin.FileEntry                        { return nil }
func (b *testBridge) MakeDir(path, owner, group string) error                             { return nil }
func (b *testBridge) Symlink(linkPath, targetPath string) error                           { return nil }
func (b *testBridge) VFSSymlink(linkPath, targetPath string) error                        { return nil }
func (b *testBridge) Chmod(path string, mode uint32) error                                { return nil }
func (b *testBridge) CreateSparseFile(path string, size int64, owner, group string) error { return nil }
func (b *testBridge) DeleteFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted = append(b.deleted, path)
	return nil
}
func (b *testBridge) RenameFile(from, toDir, toName string) error                       { return nil }
func (b *testBridge) RelocatePath(from, toDir, toName string) error                     { return nil }
func (b *testBridge) RelocatePathToSlave(from, toDir, toName, targetSlave string) error { return nil }
func (b *testBridge) ScrubReleaseRaceMetadata(dirPath, owner, group string) error       { return nil }
func (b *testBridge) WriteFile(path string, content []byte) error                       { return nil }
func (b *testBridge) ReadFile(path string) ([]byte, error)                              { return nil, nil }
func (b *testBridge) ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error) {
	return nil, nil
}
func (b *testBridge) CacheMediaInfo(path string, fields map[string]string) {}
func (b *testBridge) FileExists(path string) bool                          { return false }
func (b *testBridge) GetFileSize(path string) int64                        { return 0 }
func (b *testBridge) GetSFVData(dirPath string) map[string]uint32          { return nil }
func (b *testBridge) GetRequestData(dirPath string) ([]plugin.RequestRecord, []plugin.RequestFillRecord) {
	return nil, nil
}
func (b *testBridge) SetRequestData(dirPath string, requests []plugin.RequestRecord, fills []plugin.RequestFillRecord) {
}
func (b *testBridge) GetDirMediaInfo(dirPath string) map[string]string { return nil }
func (b *testBridge) PluginGetVFSRaceStats(dirPath string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	return nil, nil, 0, 0, 0
}
func (b *testBridge) PluginGetVFSReleaseStats(dirPath string) ([]plugin.RaceUser, []plugin.RaceGroup, int64, int, int) {
	return b.PluginGetVFSRaceStats(dirPath)
}

func TestUploadEventDeletesUploadedSpeedtestFile(t *testing.T) {
	bridge := &testBridge{}
	h := &Handler{
		svc: &plugin.Services{
			Bridge: bridge,
		},
		dir:   "/SPEEDTEST",
		files: nil,
	}

	if err := h.OnEvent(&plugin.Event{
		Type:     plugin.EventUpload,
		Path:     "/SPEEDTEST/upload-test.bin",
		Filename: "upload-test.bin",
		Size:     1024,
		Speed:    100,
	}); err != nil {
		t.Fatalf("OnEvent() error = %v", err)
	}

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if len(bridge.deleted) != 1 || bridge.deleted[0] != "/SPEEDTEST/upload-test.bin" {
		t.Fatalf("expected speedtest upload cleanup, got %#v", bridge.deleted)
	}
}
