package slave

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"testing"
)

func TestProbeMKVMetadataParsesSegmentContainer(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "*.mkv")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	defer tmp.Close()

	infoPayload := append(
		mkvTestElement([]byte{0x2A, 0xD7, 0xB1}, []byte{0x0F, 0x42, 0x40}),
		mkvTestElement([]byte{0x44, 0x89}, mkvFloat64Bytes(80_000.0))...,
	)
	videoPayload := append(
		mkvTestElement([]byte{0xB0}, []byte{0x07, 0x80}),
		mkvTestElement([]byte{0xBA}, []byte{0x04, 0x38})...,
	)
	trackPayload := append(
		mkvTestElement([]byte{0x83}, []byte{0x01}),
		mkvTestElement([]byte{0x86}, []byte("V_MPEG4/ISO/AVC"))...,
	)
	trackPayload = append(trackPayload, mkvTestElement([]byte{0xE0}, videoPayload)...)
	tracksPayload := mkvTestElement([]byte{0xAE}, trackPayload)
	segmentPayload := append(
		mkvTestElement([]byte{0x15, 0x49, 0xA9, 0x66}, infoPayload),
		mkvTestElement([]byte{0x16, 0x54, 0xAE, 0x6B}, tracksPayload)...,
	)
	fileBytes := append(
		mkvTestElement([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte{0x42, 0x86, 0x81, 0x01}),
		mkvTestElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)...,
	)
	if _, err := tmp.Write(fileBytes); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	fields, err := probeMKVMetadata(tmp.Name())
	if err != nil {
		t.Fatalf("probeMKVMetadata failed: %v", err)
	}
	if got := fields["video_format"]; got != "AVC" {
		t.Fatalf("video_format = %q, want AVC", got)
	}
	if got := fields["width"]; got != "1920" {
		t.Fatalf("width = %q, want 1920", got)
	}
	if got := fields["height"]; got != "1080" {
		t.Fatalf("height = %q, want 1080", got)
	}
	if got := fields["duration"]; got != "1m20s" {
		t.Fatalf("duration = %q, want 1m20s", got)
	}
}

func TestParseMP4SampleDescriptionReturnsVisualDimensions(t *testing.T) {
	buf := make([]byte, 16+78)
	buf[7] = 1
	entrySize := uint32(78)
	buf[8] = byte(entrySize >> 24)
	buf[9] = byte(entrySize >> 16)
	buf[10] = byte(entrySize >> 8)
	buf[11] = byte(entrySize)
	copy(buf[12:16], []byte("avc1"))
	buf[40] = 0x07
	buf[41] = 0x80
	buf[42] = 0x04
	buf[43] = 0x38

	codec, channels, width, height, err := parseMP4SampleDescription(newReadSeeker(buf), 0, int64(len(buf)), "vide")
	if err != nil {
		t.Fatalf("parseMP4SampleDescription failed: %v", err)
	}
	if codec != "avc1" {
		t.Fatalf("codec = %q, want avc1", codec)
	}
	if channels != 0 {
		t.Fatalf("channels = %d, want 0", channels)
	}
	if width != 1920 || height != 1080 {
		t.Fatalf("dimensions = %dx%d, want 1920x1080", width, height)
	}
}

func TestParseMP4TrackHeaderVersion0ReturnsDimensions(t *testing.T) {
	buf := make([]byte, 84)
	buf[76] = 0x07
	buf[77] = 0x80
	buf[80] = 0x04
	buf[81] = 0x38

	width, height, err := parseMP4TrackHeader(newReadSeeker(buf), 0, int64(len(buf)))
	if err != nil {
		t.Fatalf("parseMP4TrackHeader failed: %v", err)
	}
	if width != 1920 || height != 1080 {
		t.Fatalf("dimensions = %dx%d, want 1920x1080", width, height)
	}
}

func TestBestVideoTrackPrefersTrackWithDimensions(t *testing.T) {
	state := mp4ProbeState{
		tracks: []mp4Track{
			{handler: "vide", codec: "avc1", durationSeconds: 64},
			{handler: "vide", codec: "avc1", width: 3840, height: 2160, durationSeconds: 64},
		},
	}

	track := state.bestVideoTrack()
	if track == nil {
		t.Fatalf("bestVideoTrack returned nil")
	}
	if track.width != 3840 || track.height != 2160 {
		t.Fatalf("picked dimensions = %dx%d, want 3840x2160", track.width, track.height)
	}
}

func TestFindMPEG2SequenceDimensions(t *testing.T) {
	data := []byte{
		0x00, 0x00, 0x01, 0xB3,
		0x78, 0x04, 0x38,
		0x00, 0x00,
	}
	width, height, ok := findMPEG2SequenceDimensions(data)
	if !ok {
		t.Fatalf("expected MPEG-2 dimensions")
	}
	if width != 1920 || height != 1080 {
		t.Fatalf("dimensions = %dx%d, want 1920x1080", width, height)
	}
}

func TestParseHEVCSPSReturnsDimensions(t *testing.T) {
	bw := &testBitWriter{}
	bw.writeBits(0, 4)
	bw.writeBits(0, 3)
	bw.writeBits(1, 1)
	bw.writeBits(0, 2)
	bw.writeBits(0, 1)
	bw.writeBits(1, 5)
	bw.writeBits(0, 32)
	bw.writeBits(0, 16)
	bw.writeBits(0, 16)
	bw.writeBits(0, 16)
	bw.writeBits(120, 8)
	bw.writeUE(0)
	bw.writeUE(1)
	bw.writeUE(1920)
	bw.writeUE(1080)
	bw.writeBits(0, 1)

	width, height, ok := parseHEVCSPS(bw.bytes())
	if !ok {
		t.Fatalf("expected HEVC dimensions")
	}
	if width != 1920 || height != 1080 {
		t.Fatalf("dimensions = %dx%d, want 1920x1080", width, height)
	}
}

type testReadSeeker struct {
	data []byte
	pos  int64
}

func newReadSeeker(data []byte) *testReadSeeker {
	return &testReadSeeker{data: data}
}

func (r *testReadSeeker) Read(p []byte) (int, error) {
	if r.pos >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += int64(n)
	return n, nil
}

func (r *testReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case 0:
		next = offset
	case 1:
		next = r.pos + offset
	case 2:
		next = int64(len(r.data)) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if next < 0 {
		return 0, errors.New("negative seek")
	}
	r.pos = next
	return r.pos, nil
}

type testBitWriter struct {
	data []byte
	bits int
}

func (w *testBitWriter) writeBits(value uint, n int) {
	for i := n - 1; i >= 0; i-- {
		bit := (value >> i) & 1
		byteIdx := w.bits / 8
		bitIdx := 7 - (w.bits % 8)
		if byteIdx >= len(w.data) {
			w.data = append(w.data, 0)
		}
		if bit == 1 {
			w.data[byteIdx] |= 1 << bitIdx
		}
		w.bits++
	}
}

func (w *testBitWriter) writeUE(value uint) {
	codeNum := value + 1
	bits := 0
	for temp := codeNum; temp > 0; temp >>= 1 {
		bits++
	}
	for i := 0; i < bits-1; i++ {
		w.writeBits(0, 1)
	}
	w.writeBits(codeNum, bits)
}

func (w *testBitWriter) bytes() []byte {
	out := append([]byte(nil), w.data...)
	if len(out) == 0 {
		return []byte{0}
	}
	return out
}

func mkvTestElement(id []byte, payload []byte) []byte {
	out := append([]byte{}, id...)
	out = append(out, mkvTestSize(uint64(len(payload)))...)
	out = append(out, payload...)
	return out
}

func mkvTestSize(v uint64) []byte {
	switch {
	case v < 0x7F:
		return []byte{byte(0x80 | v)}
	case v < 0x3FFF:
		return []byte{byte(0x40 | (v >> 8)), byte(v)}
	case v < 0x1FFFFF:
		return []byte{byte(0x20 | (v >> 16)), byte(v >> 8), byte(v)}
	default:
		return []byte{byte(0x10 | (v >> 24)), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

func mkvFloat64Bytes(v float64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, math.Float64bits(v))
	return out
}
