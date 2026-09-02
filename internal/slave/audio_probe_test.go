package slave

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeID3GenreMapsNumericTCON(t *testing.T) {
	tests := map[string]string{
		"(15)":       "Rap",
		"15":         "Rap",
		"(17)":       "Rock",
		"(13)(15)":   "Pop, Rap",
		"(15)Rap":    "Rap",
		"(RX)":       "Remix",
		"Electronic": "Electronic",
		"(255)":      "(255)",
	}
	for input, want := range tests {
		if got := normalizeID3Genre(input); got != want {
			t.Fatalf("normalizeID3Genre(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReadID3v2TagsMapsNumericGenreFrame(t *testing.T) {
	tmp := t.TempDir()
	fullPath := filepath.Join(tmp, "track.mp3")
	framePayload := append([]byte{3}, []byte("(15)")...)
	var frame bytes.Buffer
	frame.WriteString("TCON")
	_ = binary.Write(&frame, binary.BigEndian, uint32(len(framePayload)))
	frame.Write([]byte{0, 0})
	frame.Write(framePayload)

	tag := frame.Bytes()
	header := []byte{'I', 'D', '3', 3, 0, 0, byte(len(tag) >> 21), byte(len(tag) >> 14), byte(len(tag) >> 7), byte(len(tag))}
	if err := os.WriteFile(fullPath, append(header, tag...), 0o644); err != nil {
		t.Fatalf("write test id3v2 tag: %v", err)
	}
	f, err := os.Open(fullPath)
	if err != nil {
		t.Fatalf("open test id3v2 tag: %v", err)
	}
	defer f.Close()

	fields := map[string]string{}
	offset, err := readID3v2Tags(f, fields)
	if err != nil {
		t.Fatalf("readID3v2Tags: %v", err)
	}
	if offset != int64(10+len(tag)) {
		t.Fatalf("offset = %d, want %d", offset, 10+len(tag))
	}
	if fields["genre"] != "Rap" {
		t.Fatalf("genre = %q, want Rap; fields=%+v", fields["genre"], fields)
	}
}

func TestReadID3v1TagsMapsGenreByte(t *testing.T) {
	tmp := t.TempDir()
	fullPath := filepath.Join(tmp, "track.mp3")
	tag := make([]byte, 128)
	copy(tag[:3], "TAG")
	copy(tag[3:33], "Unit Title")
	copy(tag[33:63], "Unit Artist")
	copy(tag[63:93], "Unit Album")
	copy(tag[93:97], "2026")
	tag[127] = 15
	if err := os.WriteFile(fullPath, tag, 0o644); err != nil {
		t.Fatalf("write test tag: %v", err)
	}
	f, err := os.Open(fullPath)
	if err != nil {
		t.Fatalf("open test tag: %v", err)
	}
	defer f.Close()

	fields := map[string]string{}
	readID3v1Tags(f, int64(len(tag)), fields)
	if fields["genre"] != "Rap" {
		t.Fatalf("genre = %q, want Rap; fields=%+v", fields["genre"], fields)
	}
}

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
