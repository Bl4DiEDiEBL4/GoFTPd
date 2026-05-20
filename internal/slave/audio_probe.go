package slave

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

func probeFastAudioMetadata(fullPath string) (map[string]string, bool, error) {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(fullPath), ".")) {
	case "mp3":
		fields, err := probeMP3Metadata(fullPath)
		return fields, true, err
	case "flac":
		fields, err := probeFLACMetadata(fullPath)
		return fields, true, err
	default:
		return nil, false, nil
	}
}

func probeMP3Metadata(fullPath string) (map[string]string, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	fields := map[string]string{}
	audioOffset, err := readID3v2Tags(f, fields)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(audioOffset, io.SeekStart); err != nil {
		return nil, err
	}

	var bitrates []int
	var sampleRate int
	var channels string
	var stereoMode string
	var firstBitrate int

	buf := make([]byte, 4)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, err
	}
	pos := audioOffset
	for scans := 0; scans < 1_000_000; scans++ {
		h := binary.BigEndian.Uint32(buf)
		br, sr, mode, frameLen, ok := parseMP3FrameHeader(h)
		if ok {
			firstBitrate = br
			sampleRate = sr
			channels, stereoMode = mp3ChannelsForMode(mode)
			bitrates = append(bitrates, br)
			pos += 4
			if frameLen < 4 {
				break
			}
			if _, err := f.Seek(int64(frameLen-4), io.SeekCurrent); err != nil {
				break
			}
			pos += int64(frameLen - 4)
			for len(bitrates) < 12 {
				if _, err := io.ReadFull(f, buf); err != nil {
					break
				}
				h = binary.BigEndian.Uint32(buf)
				br, _, _, frameLen, ok = parseMP3FrameHeader(h)
				if !ok || frameLen < 4 {
					break
				}
				bitrates = append(bitrates, br)
				if _, err := f.Seek(int64(frameLen-4), io.SeekCurrent); err != nil {
					break
				}
			}
			break
		}
		pos++
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, fmt.Errorf("no valid mp3 frame found")
		}
	}
	if firstBitrate <= 0 || sampleRate <= 0 {
		return nil, fmt.Errorf("no valid mp3 frame found")
	}

	mode := "CBR"
	for _, br := range bitrates {
		if br != firstBitrate {
			mode = "VBR"
			break
		}
	}

	durationSeconds := 0.0
	if firstBitrate > 0 {
		durationSeconds = float64(info.Size()*8) / float64(firstBitrate)
	}

	fields["audio_format"] = "MP3"
	fields["bitrate"] = strconv.Itoa(firstBitrate)
	fields["bitrate_mode"] = mode
	fields["sample_rate"] = strconv.Itoa(sampleRate)
	fields["sampling_rate"] = strconv.Itoa(sampleRate)
	fields["samplerate"] = strconv.Itoa(sampleRate)
	fields["channels"] = channels
	fields["stereomode"] = stereoMode
	fields["duration"] = fmt.Sprintf("%.0f", durationSeconds)
	deriveMediaInfoFields(fields)
	readID3v1Tags(fullPath, fields)
	deriveMediaInfoFields(fields)
	return fields, nil
}

func readID3v2Tags(f *os.File, fields map[string]string) (int64, error) {
	header := make([]byte, 10)
	if _, err := f.ReadAt(header, 0); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil
		}
		return 0, err
	}
	if string(header[:3]) != "ID3" {
		return 0, nil
	}
	version := header[3]
	tagSize := syncSafeInt(header[6:10])
	if tagSize <= 0 {
		return 10, nil
	}
	tagData := make([]byte, tagSize)
	if _, err := f.ReadAt(tagData, 10); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, err
	}

	pos := 0
	for pos+10 <= len(tagData) {
		frameID := string(tagData[pos : pos+4])
		if strings.Trim(frameID, "\x00") == "" {
			break
		}
		var frameSize int
		switch version {
		case 4:
			frameSize = syncSafeInt(tagData[pos+4 : pos+8])
		default:
			frameSize = int(binary.BigEndian.Uint32(tagData[pos+4 : pos+8]))
		}
		if frameSize <= 0 || pos+10+frameSize > len(tagData) {
			break
		}
		payload := tagData[pos+10 : pos+10+frameSize]
		if text := decodeID3Text(payload); text != "" {
			switch frameID {
			case "TIT2":
				setIfEmpty(fields, "title", text)
			case "TPE1":
				setIfEmpty(fields, "artist", text)
			case "TALB":
				setIfEmpty(fields, "album", text)
			case "TCON":
				setIfEmpty(fields, "genre", text)
			case "TYER", "TDRC":
				setIfEmpty(fields, "year", text)
			case "TRCK":
				setIfEmpty(fields, "track", text)
			}
		}
		pos += 10 + frameSize
	}
	return int64(10 + tagSize), nil
}

func readID3v1Tags(fullPath string, fields map[string]string) {
	f, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < 128 {
		return
	}
	buf := make([]byte, 128)
	if _, err := f.ReadAt(buf, info.Size()-128); err != nil {
		return
	}
	if string(buf[:3]) != "TAG" {
		return
	}
	setIfEmpty(fields, "title", trimNullLatin1(buf[3:33]))
	setIfEmpty(fields, "artist", trimNullLatin1(buf[33:63]))
	setIfEmpty(fields, "album", trimNullLatin1(buf[63:93]))
	setIfEmpty(fields, "year", trimNullLatin1(buf[93:97]))
}

func decodeID3Text(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	enc := payload[0]
	data := payload[1:]
	switch enc {
	case 0, 3:
		return strings.TrimSpace(trimNullLatin1(data))
	case 1, 2:
		if len(data) < 2 {
			return ""
		}
		be := true
		if enc == 1 {
			if data[0] == 0xFF && data[1] == 0xFE {
				be = false
				data = data[2:]
			} else if data[0] == 0xFE && data[1] == 0xFF {
				be = true
				data = data[2:]
			}
		}
		if len(data)%2 != 0 {
			data = data[:len(data)-1]
		}
		u16 := make([]uint16, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			var v uint16
			if be {
				v = binary.BigEndian.Uint16(data[i : i+2])
			} else {
				v = binary.LittleEndian.Uint16(data[i : i+2])
			}
			if v == 0 {
				break
			}
			u16 = append(u16, v)
		}
		return strings.TrimSpace(string(utf16.Decode(u16)))
	default:
		return strings.TrimSpace(trimNullLatin1(data))
	}
}

func syncSafeInt(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func trimNullLatin1(b []byte) string {
	return strings.TrimSpace(string(bytes.TrimRight(b, "\x00 ")))
}

func setIfEmpty(fields map[string]string, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" || fields[key] != "" {
		return
	}
	fields[key] = value
}

func parseMP3FrameHeader(h uint32) (bitrate int, sampleRate int, mode int, frameLen int, ok bool) {
	if (h>>21)&0x7FF != 0x7FF {
		return
	}
	versionID := (h >> 19) & 0x3
	layerID := (h >> 17) & 0x3
	bitrateIdx := (h >> 12) & 0xF
	sampleIdx := (h >> 10) & 0x3
	padding := (h >> 9) & 0x1
	mode = int((h >> 6) & 0x3)
	if versionID == 1 || layerID != 1 || bitrateIdx == 0 || bitrateIdx == 15 || sampleIdx == 3 {
		return
	}
	versionKey := versionID
	mp3Bitrates := map[uint32][]int{
		3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
		2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		0: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	}
	sampleRates := map[uint32][]int{
		3: {44100, 48000, 32000},
		2: {22050, 24000, 16000},
		0: {11025, 12000, 8000},
	}
	brKbps := mp3Bitrates[versionKey][bitrateIdx]
	if brKbps == 0 {
		return
	}
	sampleRate = sampleRates[versionKey][sampleIdx]
	if sampleRate == 0 {
		return
	}
	bitrate = brKbps * 1000
	frameLen = (144*bitrate)/sampleRate + int(padding)
	ok = true
	return
}

func mp3ChannelsForMode(mode int) (string, string) {
	switch mode {
	case 3:
		return "Mono", "Mono"
	default:
		return "Stereo", "Stereo"
	}
}

func probeFLACMetadata(fullPath string) (map[string]string, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	marker := make([]byte, 4)
	if _, err := io.ReadFull(f, marker); err != nil {
		return nil, err
	}
	if string(marker) != "fLaC" {
		return nil, fmt.Errorf("invalid flac header")
	}

	fields := map[string]string{"audio_format": "FLAC"}
	var sampleRate uint32
	var totalSamples uint64
	var channels int
	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(f, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, err
		}
		last := header[0]&0x80 != 0
		blockType := header[0] & 0x7F
		blockLen := int(header[1])<<16 | int(header[2])<<8 | int(header[3])
		switch blockType {
		case 0:
			block := make([]byte, blockLen)
			if _, err := io.ReadFull(f, block); err != nil {
				return nil, err
			}
			if len(block) >= 34 {
				word := uint64(block[10])<<56 | uint64(block[11])<<48 | uint64(block[12])<<40 | uint64(block[13])<<32 |
					uint64(block[14])<<24 | uint64(block[15])<<16 | uint64(block[16])<<8 | uint64(block[17])
				sampleRate = uint32((word >> 44) & 0xFFFFF)
				channels = int(((word >> 41) & 0x7) + 1)
				totalSamples = word & 0xFFFFFFFFF
			}
		case 4:
			block := make([]byte, blockLen)
			if _, err := io.ReadFull(f, block); err != nil {
				return nil, err
			}
			readVorbisComment(block, fields)
		default:
			if _, err := f.Seek(int64(blockLen), io.SeekCurrent); err != nil {
				return nil, err
			}
		}
		if last {
			break
		}
	}
	if sampleRate == 0 {
		return nil, fmt.Errorf("missing flac streaminfo")
	}
	durationSeconds := 0.0
	if totalSamples > 0 {
		durationSeconds = float64(totalSamples) / float64(sampleRate)
	}
	fields["sampling_rate"] = strconv.Itoa(int(sampleRate))
	fields["sample_rate"] = strconv.Itoa(int(sampleRate))
	fields["samplerate"] = strconv.Itoa(int(sampleRate))
	fields["channels"] = strconv.Itoa(channels)
	if durationSeconds > 0 {
		fields["duration"] = fmt.Sprintf("%.0f", durationSeconds)
		fields["bitrate"] = strconv.Itoa(int((float64(info.Size())*8.0)/durationSeconds + 0.5))
	}
	fields["bitrate_mode"] = "VBR"
	deriveMediaInfoFields(fields)
	return fields, nil
}

func readVorbisComment(block []byte, fields map[string]string) {
	if len(block) < 8 {
		return
	}
	off := 0
	readU32 := func() (uint32, bool) {
		if off+4 > len(block) {
			return 0, false
		}
		v := binary.LittleEndian.Uint32(block[off : off+4])
		off += 4
		return v, true
	}
	vendorLen, ok := readU32()
	if !ok || off+int(vendorLen) > len(block) {
		return
	}
	off += int(vendorLen)
	count, ok := readU32()
	if !ok {
		return
	}
	for i := 0; i < int(count); i++ {
		n, ok := readU32()
		if !ok || off+int(n) > len(block) {
			return
		}
		entry := string(block[off : off+int(n)])
		off += int(n)
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "ARTIST":
			setIfEmpty(fields, "artist", value)
		case "ALBUM":
			setIfEmpty(fields, "album", value)
		case "GENRE":
			setIfEmpty(fields, "genre", value)
		case "DATE", "YEAR":
			setIfEmpty(fields, "year", value)
		case "TITLE":
			setIfEmpty(fields, "title", value)
		case "TRACKNUMBER":
			setIfEmpty(fields, "track", value)
		}
	}
}
