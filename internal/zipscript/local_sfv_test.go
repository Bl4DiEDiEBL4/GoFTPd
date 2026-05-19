package zipscript

import (
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
