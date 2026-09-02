package slave

import (
	"bufio"
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

var mp3Bitrates = [4][16]int{
	0: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
}

var mp3SampleRates = [4][3]int{
	0: {11025, 12000, 8000},
	2: {22050, 24000, 16000},
	3: {44100, 48000, 32000},
}

var id3v1Genres = []string{
	"Blues", "Classic Rock", "Country", "Dance", "Disco", "Funk", "Grunge", "Hip-Hop",
	"Jazz", "Metal", "New Age", "Oldies", "Other", "Pop", "R&B", "Rap",
	"Reggae", "Rock", "Techno", "Industrial", "Alternative", "Ska", "Death Metal", "Pranks",
	"Soundtrack", "Euro-Techno", "Ambient", "Trip-Hop", "Vocal", "Jazz+Funk", "Fusion", "Trance",
	"Classical", "Instrumental", "Acid", "House", "Game", "Sound Clip", "Gospel", "Noise",
	"AlternRock", "Bass", "Soul", "Punk", "Space", "Meditative", "Instrumental Pop", "Instrumental Rock",
	"Ethnic", "Gothic", "Darkwave", "Techno-Industrial", "Electronic", "Pop-Folk", "Eurodance", "Dream",
	"Southern Rock", "Comedy", "Cult", "Gangsta", "Top 40", "Christian Rap", "Pop/Funk", "Jungle",
	"Native American", "Cabaret", "New Wave", "Psychedelic", "Rave", "Showtunes", "Trailer", "Lo-Fi",
	"Tribal", "Acid Punk", "Acid Jazz", "Polka", "Retro", "Musical", "Rock & Roll", "Hard Rock",
	"Folk", "Folk-Rock", "National Folk", "Swing", "Fast Fusion", "Bebob", "Latin", "Revival",
	"Celtic", "Bluegrass", "Avantgarde", "Gothic Rock", "Progressive Rock", "Psychedelic Rock", "Symphonic Rock", "Slow Rock",
	"Big Band", "Chorus", "Easy Listening", "Acoustic", "Humour", "Speech", "Chanson", "Opera",
	"Chamber Music", "Sonata", "Symphony", "Booty Bass", "Primus", "Porn Groove", "Satire", "Slow Jam",
	"Club", "Tango", "Samba", "Folklore", "Ballad", "Power Ballad", "Rhythmic Soul", "Freestyle",
	"Duet", "Punk Rock", "Drum Solo", "A Cappella", "Euro-House", "Dance Hall",
}

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

	firstBitrate, sampleRate, channels, stereoMode, bitrates, err := scanMP3Frames(f)
	if err != nil {
		return nil, err
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
	readID3v1Tags(f, info.Size(), fields)
	deriveMediaInfoFields(fields)
	return fields, nil
}

func scanMP3Frames(r io.Reader) (firstBitrate int, sampleRate int, channels string, stereoMode string, bitrates []int, err error) {
	br := bufio.NewReaderSize(r, 64*1024)
	var header [4]byte
	if _, err := io.ReadFull(br, header[:]); err != nil {
		return 0, 0, "", "", nil, err
	}
	for scans := 0; scans < 1_000_000; scans++ {
		h := binary.BigEndian.Uint32(header[:])
		bitrate, sr, mode, frameLen, ok := parseMP3FrameHeader(h)
		if ok {
			firstBitrate = bitrate
			sampleRate = sr
			channels, stereoMode = mp3ChannelsForMode(mode)
			bitrates = append(bitrates, bitrate)
			if frameLen >= 4 {
				if _, skipErr := io.CopyN(io.Discard, br, int64(frameLen-4)); skipErr != nil {
					return firstBitrate, sampleRate, channels, stereoMode, bitrates, nil
				}
			}
			for len(bitrates) < 12 {
				if _, err := io.ReadFull(br, header[:]); err != nil {
					break
				}
				h = binary.BigEndian.Uint32(header[:])
				bitrate, _, _, frameLen, ok = parseMP3FrameHeader(h)
				if !ok || frameLen < 4 {
					break
				}
				bitrates = append(bitrates, bitrate)
				if _, err := io.CopyN(io.Discard, br, int64(frameLen-4)); err != nil {
					break
				}
			}
			return firstBitrate, sampleRate, channels, stereoMode, bitrates, nil
		}
		next, err := br.ReadByte()
		if err != nil {
			return 0, 0, "", "", nil, fmt.Errorf("no valid mp3 frame found")
		}
		header[0], header[1], header[2], header[3] = header[1], header[2], header[3], next
	}
	return 0, 0, "", "", nil, fmt.Errorf("no valid mp3 frame found")
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
				setIfEmpty(fields, "genre", normalizeID3Genre(text))
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

func readID3v1Tags(f *os.File, fileSize int64, fields map[string]string) {
	if f == nil || fileSize < 128 {
		return
	}
	buf := make([]byte, 128)
	if _, err := f.ReadAt(buf, fileSize-128); err != nil {
		return
	}
	if string(buf[:3]) != "TAG" {
		return
	}
	setIfEmpty(fields, "title", trimNullLatin1(buf[3:33]))
	setIfEmpty(fields, "artist", trimNullLatin1(buf[33:63]))
	setIfEmpty(fields, "album", trimNullLatin1(buf[63:93]))
	setIfEmpty(fields, "year", trimNullLatin1(buf[93:97]))
	if genre := id3GenreByIndex(int(buf[127])); genre != "" {
		setIfEmpty(fields, "genre", genre)
	}
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

func normalizeID3Genre(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if genre := id3GenreToken(value); genre != "" {
		return genre
	}

	rest := value
	genres := make([]string, 0, 2)
	for strings.HasPrefix(rest, "(") {
		end := strings.IndexByte(rest, ')')
		if end <= 1 {
			break
		}
		genre := id3GenreToken(rest[1:end])
		if genre == "" {
			break
		}
		genres = appendUniqueString(genres, genre)
		rest = strings.TrimSpace(rest[end+1:])
	}
	if rest != "" {
		if genre := id3GenreToken(rest); genre != "" {
			genres = appendUniqueString(genres, genre)
			if len(genres) > 0 {
				return strings.Join(genres, ", ")
			}
		}
		if len(genres) > 0 && !strings.HasPrefix(rest, "(") {
			return rest
		}
		return value
	}
	if len(genres) > 0 {
		return strings.Join(genres, ", ")
	}
	return value
}

func id3GenreToken(token string) string {
	token = strings.TrimSpace(token)
	switch strings.ToUpper(token) {
	case "RX":
		return "Remix"
	case "CR":
		return "Cover"
	}
	index, err := strconv.Atoi(token)
	if err != nil {
		return ""
	}
	return id3GenreByIndex(index)
}

func id3GenreByIndex(index int) string {
	if index < 0 || index >= len(id3v1Genres) {
		return ""
	}
	return id3v1Genres[index]
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if strings.EqualFold(existing, value) {
			return values
		}
	}
	return append(values, value)
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
	brKbps := mp3Bitrates[versionID][bitrateIdx]
	if brKbps == 0 {
		return
	}
	sampleRate = mp3SampleRates[versionID][sampleIdx]
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
			var streamInfo [34]byte
			readLen := blockLen
			if readLen > len(streamInfo) {
				readLen = len(streamInfo)
			}
			if _, err := io.ReadFull(f, streamInfo[:readLen]); err != nil {
				return nil, err
			}
			if blockLen > readLen {
				if _, err := f.Seek(int64(blockLen-readLen), io.SeekCurrent); err != nil {
					return nil, err
				}
			}
			if readLen >= len(streamInfo) {
				word := uint64(streamInfo[10])<<56 | uint64(streamInfo[11])<<48 | uint64(streamInfo[12])<<40 | uint64(streamInfo[13])<<32 |
					uint64(streamInfo[14])<<24 | uint64(streamInfo[15])<<16 | uint64(streamInfo[16])<<8 | uint64(streamInfo[17])
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
