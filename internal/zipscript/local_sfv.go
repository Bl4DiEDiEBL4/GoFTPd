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
	"sync"
	"unicode"
)

const localCacheMaxEntries = 4096

type localSFVFileSignature struct {
	name        string
	size        int64
	modUnixNano int64
}

type localSFVDirCacheEntry struct {
	signature []localSFVFileSignature
	entries   map[string]uint32
}

type localCRCCacheEntry struct {
	size        int64
	modUnixNano int64
	crc         uint32
}

var localSFVCache = struct {
	sync.Mutex
	dirs map[string]localSFVDirCacheEntry
}{
	dirs: make(map[string]localSFVDirCacheEntry),
}

var localCRCCache = struct {
	sync.Mutex
	files map[string]localCRCCacheEntry
}{
	files: make(map[string]localCRCCacheEntry),
}

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
	dirPath = filepath.Clean(dirPath)
	signature, err := localSFVSignature(dirPath)
	if err != nil {
		return nil
	}
	localSFVCache.Lock()
	if cached, ok := localSFVCache.dirs[dirPath]; ok && localSFVSignatureEqual(cached.signature, signature) {
		out := cloneLocalSFVEntries(cached.entries)
		localSFVCache.Unlock()
		return out
	}
	localSFVCache.Unlock()

	parsed := make(map[string]uint32)
	for _, sfv := range signature {
		data, err := os.ReadFile(filepath.Join(dirPath, sfv.name))
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
		localSFVCache.Lock()
		storeLocalSFVCacheLocked(dirPath, signature, nil)
		localSFVCache.Unlock()
		return nil
	}
	localSFVCache.Lock()
	storeLocalSFVCacheLocked(dirPath, signature, parsed)
	localSFVCache.Unlock()
	return cloneLocalSFVEntries(parsed)
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
	line = strings.TrimPrefix(line, "\ufeff")
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
	fileName = strings.TrimPrefix(fileName, "\ufeff")
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

	CreateLocalSFVMissingMarker(cfg, filepath.Dir(localPath), filepath.Base(localPath))
	if ShouldDeleteBadCRCForDir(cfg, filepath.ToSlash(filepath.Dir(localPath))) {
		_ = os.Remove(localPath)
	}
	_ = filePath
	return true
}

func LocalFileCRC(localPath string) (uint32, error) {
	localPath = filepath.Clean(localPath)
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, err
	}
	size := info.Size()
	modUnixNano := info.ModTime().UnixNano()
	localCRCCache.Lock()
	if cached, ok := localCRCCache.files[localPath]; ok && cached.size == size && cached.modUnixNano == modUnixNano {
		localCRCCache.Unlock()
		return cached.crc, nil
	}
	localCRCCache.Unlock()

	file, err := os.Open(localPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	hash := crc32.NewIEEE()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, err
	}
	crc := hash.Sum32()
	if after, err := file.Stat(); err == nil && after.Size() == size && after.ModTime().UnixNano() == modUnixNano {
		localCRCCache.Lock()
		if len(localCRCCache.files) >= localCacheMaxEntries {
			localCRCCache.files = make(map[string]localCRCCacheEntry)
		}
		localCRCCache.files[localPath] = localCRCCacheEntry{size: size, modUnixNano: modUnixNano, crc: crc}
		localCRCCache.Unlock()
	}
	return crc, nil
}

func localSFVSignature(dirPath string) ([]localSFVFileSignature, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	signature := make([]localSFVFileSignature, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".sfv") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		signature = append(signature, localSFVFileSignature{
			name:        entry.Name(),
			size:        info.Size(),
			modUnixNano: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(signature, func(i, j int) bool {
		return strings.ToLower(signature[i].name) < strings.ToLower(signature[j].name)
	})
	return signature, nil
}

func localSFVSignatureEqual(a, b []localSFVFileSignature) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneLocalSFVEntries(entries map[string]uint32) map[string]uint32 {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]uint32, len(entries))
	for name, crc := range entries {
		out[name] = crc
	}
	return out
}

func storeLocalSFVCacheLocked(dirPath string, signature []localSFVFileSignature, entries map[string]uint32) {
	if len(localSFVCache.dirs) >= localCacheMaxEntries {
		localSFVCache.dirs = make(map[string]localSFVDirCacheEntry)
	}
	localSFVCache.dirs[dirPath] = localSFVDirCacheEntry{
		signature: append([]localSFVFileSignature(nil), signature...),
		entries:   cloneLocalSFVEntries(entries),
	}
}
