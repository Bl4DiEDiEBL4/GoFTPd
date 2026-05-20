package zipscript

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLocalSFVEntryLine(t *testing.T) {
	fileName, crc, ok := ParseLocalSFVEntryLine("release.r00  A1B2C3D4")
	if !ok {
		t.Fatalf("expected SFV line to parse")
	}
	if fileName != "release.r00" {
		t.Fatalf("expected file name to be preserved, got %q", fileName)
	}
	if crc != 0xA1B2C3D4 {
		t.Fatalf("expected CRC to parse, got %08X", crc)
	}
}

func TestLocalExpectedCRCForFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "release.sfv"), []byte("; comment\nsample.rar AABBCCDD\n"), 0644); err != nil {
		t.Fatalf("write sfv: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.rar"), []byte("payload"), 0644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	crc, ok := LocalExpectedCRCForFile(filepath.Join(root, "sample.rar"))
	if !ok {
		t.Fatalf("expected local SFV lookup to find payload entry")
	}
	if crc != 0xAABBCCDD {
		t.Fatalf("expected CRC AABBCCDD, got %08X", crc)
	}
}

func TestLocalSFVEntriesForDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "release.sfv"), []byte("Sample.RAR 11223344\nother.r00 AABBCCDD\n"), 0644); err != nil {
		t.Fatalf("write sfv: %v", err)
	}

	entries := LocalSFVEntriesForDir(root)
	if len(entries) != 2 {
		t.Fatalf("expected two parsed SFV entries, got %d", len(entries))
	}
	if crc, ok := CachedExpectedCRC(entries, "sample.rar"); !ok || crc != 0x11223344 {
		t.Fatalf("expected case-insensitive lookup to work, got %08X %v", crc, ok)
	}
}

func TestLocalShouldTreatDownloadAsMissingCreatesMarkerWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	payload := []byte("bad payload")
	expected := crc32.ChecksumIEEE([]byte("good payload"))
	if err := os.WriteFile(filepath.Join(root, "release.sfv"), []byte("sample.rar "+crcHex(expected)+"\n"), 0644); err != nil {
		t.Fatalf("write sfv: %v", err)
	}
	localPath := filepath.Join(root, "sample.rar")
	if err := os.WriteFile(localPath, payload, 0644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	cfg := Config{
		Enabled: true,
		Sections: SectionsConfig{
			SFV: []string{filepath.ToSlash(root)},
		},
		List: ListConfig{
			MissingFiles: boolPtr(true),
		},
	}

	if !LocalShouldTreatDownloadAsMissing(cfg, "/X265/release/sample.rar", localPath) {
		t.Fatalf("expected bad local checksum to be treated as missing")
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("expected payload to remain when delete-bad is disabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sample.rar-MISSING")); err != nil {
		t.Fatalf("expected missing marker to be created: %v", err)
	}
}

func crcHex(crc uint32) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = digits[crc&0xF]
		crc >>= 4
	}
	return string(out)
}
