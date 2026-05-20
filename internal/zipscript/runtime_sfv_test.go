package zipscript

import "testing"

type fakeSFVRuntimeBridge struct {
	sfvEntries map[string]uint32
	checksums  map[string]uint32
	files      map[string]bool
	writes     []string
	deletes    []string
	missing    []string
}

func (b *fakeSFVRuntimeBridge) GetSFVData(dirPath string) map[string]uint32 {
	return b.sfvEntries
}

func (b *fakeSFVRuntimeBridge) GetKnownChecksum(filePath string) (uint32, bool) {
	checksum, ok := b.checksums[filePath]
	return checksum, ok
}

func (b *fakeSFVRuntimeBridge) DeleteFile(filePath string) error {
	b.deletes = append(b.deletes, filePath)
	delete(b.files, filePath)
	return nil
}

func (b *fakeSFVRuntimeBridge) MarkFileMissing(filePath string) error {
	b.missing = append(b.missing, filePath)
	return nil
}

func (b *fakeSFVRuntimeBridge) GetFileSize(filePath string) int64 {
	if b.files[filePath] {
		return 1
	}
	return -1
}

func (b *fakeSFVRuntimeBridge) WriteFile(filePath string, data []byte) error {
	b.writes = append(b.writes, filePath)
	b.files[filePath] = true
	return nil
}

func TestShouldTreatDownloadAsMissingCreatesMarkerWithoutDeleting(t *testing.T) {
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{"/X265/*"},
		},
		List: ListConfig{
			MissingFiles: boolPtr(true),
		},
	}
	bridge := &fakeSFVRuntimeBridge{
		sfvEntries: map[string]uint32{"file.r00": 0x12345678},
		checksums:  map[string]uint32{"/X265/release/file.r00": 0x87654321},
		files:      map[string]bool{"/X265/release/file.r00": true},
	}

	if !ShouldTreatDownloadAsMissing(cfg, bridge, "/X265/release/file.r00", nil) {
		t.Fatalf("expected bad known checksum to be treated as missing")
	}
	if len(bridge.writes) != 1 || bridge.writes[0] != "/X265/release/file.r00-MISSING" {
		t.Fatalf("expected missing marker write, got %#v", bridge.writes)
	}
	if len(bridge.deletes) != 0 || len(bridge.missing) != 0 {
		t.Fatalf("did not expect delete/MarkFileMissing when delete-bad is disabled, deletes=%#v missing=%#v", bridge.deletes, bridge.missing)
	}
}
