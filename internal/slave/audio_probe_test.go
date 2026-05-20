package slave

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeFLACMetadataSkipsNonEssentialBlocks(t *testing.T) {
	tmp := t.TempDir()
	fullPath := filepath.Join(tmp, "track.flac")

	content := append([]byte("fLaC"), flacMetadataBlock(false, 0, testFLACStreamInfo(44100, 2, 441000))...)
	content = append(content, flacMetadataBlock(false, 1, bytes.Repeat([]byte{0}, 128*1024))...)
	content = append(content, flacMetadataBlock(true, 4, testVorbisCommentBlock("ARTIST=Unit Test", "GENRE=Rock"))...)
	content = append(content, bytes.Repeat([]byte{0xaa}, 4096)...)

	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		t.Fatalf("write test flac: %v", err)
	}

	fields, err := probeFLACMetadata(fullPath)
	if err != nil {
		t.Fatalf("probeFLACMetadata: %v", err)
	}
	if fields["audio_format"] != "FLAC" {
		t.Fatalf("audio_format = %q, want FLAC", fields["audio_format"])
	}
	if fields["sample_rate"] != "44100" || fields["channels"] != "Stereo" || fields["duration"] != "10s" {
		t.Fatalf("unexpected normalized audio fields: %+v", fields)
	}
	if fields["artist"] != "Unit Test" || fields["genre"] != "Rock" {
		t.Fatalf("unexpected vorbis comments: %+v", fields)
	}
	if fields["bitrate"] == "" {
		t.Fatalf("expected bitrate to be derived from file size and duration")
	}
}

func flacMetadataBlock(last bool, blockType byte, payload []byte) []byte {
	headerType := blockType & 0x7f
	if last {
		headerType |= 0x80
	}
	out := []byte{headerType, byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return append(out, payload...)
}

func testFLACStreamInfo(sampleRate uint32, channels int, totalSamples uint64) []byte {
	block := make([]byte, 34)
	word := (uint64(sampleRate)&0xfffff)<<44 |
		(uint64(channels-1)&0x7)<<41 |
		(15 << 36) |
		(totalSamples & 0xfffffffff)
	binary.BigEndian.PutUint64(block[10:18], word)
	return block
}

func testVorbisCommentBlock(entries ...string) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(entries)))
	for _, entry := range entries {
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(entry)))
		buf.WriteString(entry)
	}
	return buf.Bytes()
}
