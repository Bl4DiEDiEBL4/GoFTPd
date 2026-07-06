package zipscript

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if err := os.WriteFile(filepath.Join(root, "release.sfv"), []byte("\ufeffSample.RAR 11223344\n./disc/other.r00 AABBCCDD\n"), 0644); err != nil {
		t.Fatalf("write sfv: %v", err)
	}

	entries := LocalSFVEntriesForDir(root)
	if len(entries) != 2 {
		t.Fatalf("expected two parsed SFV entries, got %d", len(entries))
	}
	if crc, ok := CachedExpectedCRC(entries, "sample.rar"); !ok || crc != 0x11223344 {
		t.Fatalf("expected case-insensitive lookup to work, got %08X %v", crc, ok)
	}
	if crc, ok := CachedExpectedCRC(entries, "other.r00"); !ok || crc != 0xAABBCCDD {
		t.Fatalf("expected path-prefixed SFV entry lookup to work, got %08X %v", crc, ok)
	}
}

func TestLocalSFVEntriesForDirInvalidatesOnSFVChange(t *testing.T) {
	root := t.TempDir()
	sfvPath := filepath.Join(root, "release.sfv")
	firstTime := time.Now().Add(-time.Hour)
	if err := os.WriteFile(sfvPath, []byte("sample.rar 11111111\n"), 0644); err != nil {
		t.Fatalf("write sfv: %v", err)
	}
	if err := os.Chtimes(sfvPath, firstTime, firstTime); err != nil {
		t.Fatalf("chtime sfv: %v", err)
	}

	entries := LocalSFVEntriesForDir(root)
	if crc, ok := CachedExpectedCRC(entries, "sample.rar"); !ok || crc != 0x11111111 {
		t.Fatalf("expected initial CRC, got %08X %v", crc, ok)
	}

	secondTime := firstTime.Add(time.Minute)
	if err := os.WriteFile(sfvPath, []byte("sample.rar 22222222\nother.r00 33333333\n"), 0644); err != nil {
		t.Fatalf("rewrite sfv: %v", err)
	}
	if err := os.Chtimes(sfvPath, secondTime, secondTime); err != nil {
		t.Fatalf("chtime rewritten sfv: %v", err)
	}

	entries = LocalSFVEntriesForDir(root)
	if crc, ok := CachedExpectedCRC(entries, "sample.rar"); !ok || crc != 0x22222222 {
		t.Fatalf("expected updated CRC, got %08X %v", crc, ok)
	}
	if crc, ok := CachedExpectedCRC(entries, "other.r00"); !ok || crc != 0x33333333 {
		t.Fatalf("expected new entry, got %08X %v", crc, ok)
	}
}

func TestLocalFileCRCInvalidatesOnFileChange(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, "sample.rar")
	firstTime := time.Now().Add(-time.Hour)
	if err := os.WriteFile(localPath, []byte("first payload"), 0644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.Chtimes(localPath, firstTime, firstTime); err != nil {
		t.Fatalf("chtime payload: %v", err)
	}

	firstCRC, err := LocalFileCRC(localPath)
	if err != nil {
		t.Fatalf("first CRC: %v", err)
	}
	if again, err := LocalFileCRC(localPath); err != nil || again != firstCRC {
		t.Fatalf("expected cached CRC %08X, got %08X err=%v", firstCRC, again, err)
	}

	secondTime := firstTime.Add(time.Minute)
	if err := os.WriteFile(localPath, []byte("second payload changed"), 0644); err != nil {
		t.Fatalf("rewrite payload: %v", err)
	}
	if err := os.Chtimes(localPath, secondTime, secondTime); err != nil {
		t.Fatalf("chtime rewritten payload: %v", err)
	}

	secondCRC, err := LocalFileCRC(localPath)
	if err != nil {
		t.Fatalf("second CRC: %v", err)
	}
	if secondCRC == firstCRC {
		t.Fatalf("expected CRC to change after file update, still %08X", secondCRC)
	}
	if want := crc32.ChecksumIEEE([]byte("second payload changed")); secondCRC != want {
		t.Fatalf("expected updated CRC %08X, got %08X", want, secondCRC)
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
