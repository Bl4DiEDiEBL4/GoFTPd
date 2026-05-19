package zipscript

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

func WriteUploadSFVStatus(conn io.Writer, checksum uint32, expectedCRC uint32, hasExpected bool, fileSize int64) {
	if !hasExpected {
		return
	}
	if checksum == expectedCRC && checksum != 0 {
		fmt.Fprintf(conn, "226- checksum match: SLAVE/SFV:%08X\r\n", checksum)
		return
	}
	if checksum == 0 && fileSize > 0 {
		fmt.Fprintf(conn, "226- checksum match: SLAVE/SFV: DISABLED\r\n")
	}
}

func WriteUploadNoSFVEntryStatus(conn io.Writer, sfvEntries map[string]uint32, fileName string) {
	if sfvEntries == nil {
		return
	}
	if _, ok := CachedExpectedCRC(sfvEntries, fileName); ok {
		return
	}
	fmt.Fprintf(conn, "226- zipscript - no entry in sfv for file\r\n")
}

func CachedExpectedCRC(sfvEntries map[string]uint32, fileName string) (uint32, bool) {
	if sfvEntries == nil {
		return 0, false
	}
	crc, ok := sfvEntries[raceEntryKey(fileName)]
	return crc, ok
}

func LocalExpectedCRCForFile(localPath string) (uint32, bool) {
	dirPath := filepath.Dir(localPath)
	baseName := filepath.Base(localPath)
	return CachedExpectedCRC(LocalSFVEntriesForDir(dirPath), baseName)
}

func LocalSFVEntriesForDir(dirPath string) map[string]uint32 {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	sfvNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".sfv") {
			continue
		}
		sfvNames = append(sfvNames, entry.Name())
	}
	sort.Strings(sfvNames)

	parsed := map[string]uint32{}
	for _, sfvName := range sfvNames {
		data, err := os.ReadFile(filepath.Join(dirPath, sfvName))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			entryName, crc, ok := ParseLocalSFVEntryLine(line)
			if !ok {
				continue
			}
			parsed[raceEntryKey(entryName)] = crc
		}
	}
	if len(parsed) == 0 {
		return nil
	}
	return parsed
}

func SyncLocalSFVMissingMarkers(cfg Config, dirPath string) {
	if !ShowMissingFilesForDir(cfg, filepath.ToSlash(dirPath)) {
		return
	}
	sfvEntries := LocalSFVEntriesForDir(dirPath)
	if sfvEntries == nil {
		return
	}
	for trackedName := range sfvEntries {
		missingPath := filepath.Join(dirPath, trackedName+"-MISSING")
		if _, err := os.Stat(filepath.Join(dirPath, trackedName)); err == nil {
			_ = os.Remove(missingPath)
			continue
		}
		if _, err := os.Stat(missingPath); err != nil {
			_ = os.WriteFile(missingPath, []byte{}, 0644)
		}
	}
}

func ClearLocalSFVMissingMarker(dirPath, fileName string) {
	_ = os.Remove(filepath.Join(dirPath, fileName+"-MISSING"))
}

func CreateLocalSFVMissingMarker(cfg Config, dirPath, fileName string) {
	if !ShowMissingFilesForDir(cfg, filepath.ToSlash(dirPath)) {
		return
	}
	missingPath := filepath.Join(dirPath, fileName+"-MISSING")
	if _, err := os.Stat(missingPath); err == nil {
		return
	}
	_ = os.WriteFile(missingPath, []byte{}, 0644)
}

func ParseLocalSFVEntryLine(line string) (string, uint32, bool) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return "", 0, false
	}
	if strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), ";") {
		return "", 0, false
	}
	if len(line) < 9 {
		return "", 0, false
	}

	end := len(line)
	for end > 0 && unicode.IsSpace(rune(line[end-1])) {
		end--
	}
	if end < 8 {
		return "", 0, false
	}

	crcStr := line[end-8 : end]
	crc, err := strconv.ParseUint(crcStr, 16, 32)
	if err != nil {
		return "", 0, false
	}

	sep := end - 8
	if sep <= 0 || !unicode.IsSpace(rune(line[sep-1])) {
		return "", 0, false
	}
	for sep > 0 && unicode.IsSpace(rune(line[sep-1])) {
		sep--
	}

	fileName := strings.TrimSpace(line[:sep])
	if fileName == "" {
		return "", 0, false
	}

	return fileName, uint32(crc), true
}

func LocalShouldTreatDownloadAsMissing(cfg Config, filePath, localPath string) bool {
	expectedCRC, exists := LocalExpectedCRCForFile(localPath)
	if !exists || expectedCRC == 0 {
		return false
	}

	checksum, err := LocalFileCRC(localPath)
	if err != nil {
		return false
	}
	if checksum == expectedCRC {
		return false
	}

	if ShouldDeleteBadCRCForDir(cfg, filepath.ToSlash(filepath.Dir(localPath))) {
		_ = os.Remove(localPath)
	}
	_ = filePath
	return true
}

func LocalFileCRC(localPath string) (uint32, error) {
	file, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, err
	}
	return hash.Sum32(), nil
}
