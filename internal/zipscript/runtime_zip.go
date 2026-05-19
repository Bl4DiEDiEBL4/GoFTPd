package zipscript

import (
	"fmt"
	"hash/crc32"
	"log"
	"path"
	"sort"
	"strings"
)

type ZipEntryInfo struct {
	Name      string
	IsDir     bool
	IsSymlink bool
	Size      int64
	XferTime  int64
}

type ZipRuntimeBridge interface {
	CheckZipIntegrity(filePath string) (bool, error)
	DeleteFile(filePath string) error
	GetFileSize(filePath string) int64
	ReadZipEntry(filePath, entryName string) ([]byte, error)
	WriteFile(filePath string, data []byte) error
	GetZipExpectedParts(dirPath string) (int, bool)
	ReadFile(filePath string) ([]byte, error)
	CacheZipExpectedParts(dirPath string, expectedParts int, checksum uint32)
	GetKnownChecksum(filePath string) (uint32, bool)
	ListZipDirEntries(dirPath string) []ZipEntryInfo
	SyncStatusMarkersForPath(dirPath string, triggerEvents bool)
}

func ZipDirPayloadCount(entries []ZipEntryInfo) int {
	total := 0
	for _, e := range entries {
		if e.IsDir || e.IsSymlink || strings.HasPrefix(strings.TrimSpace(e.Name), ".") || !IsZipPayloadName(e.Name) {
			continue
		}
		total++
	}
	return total
}

func CheckUploadedZipIntegrity(bridge ZipRuntimeBridge, cfg Config, dirPath, filePath, fileName string) (bool, error) {
	if bridge == nil || !CheckZipIntegrityForDir(cfg, dirPath) {
		return false, nil
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(fileName)), ".zip") {
		return false, nil
	}
	ok, err := bridge.CheckZipIntegrity(filePath)
	if err != nil {
		return false, err
	}
	if ok {
		return false, nil
	}
	bridge.DeleteFile(filePath)
	log.Printf("[MASTER-ZS] Zip integrity failed for %s - deleted", filePath)
	return true, nil
}

func RefreshZipDIZFromArchive(bridge ZipRuntimeBridge, dirPath, archivePath, fileName string) error {
	if bridge == nil || !IsZipRecoverableArchiveName(fileName) {
		return nil
	}
	dizPath := path.Join(dirPath, "file_id.diz")
	if bridge.GetFileSize(dizPath) >= 0 {
		return nil
	}
	content, err := bridge.ReadZipEntry(archivePath, "file_id.diz")
	if err != nil || len(content) == 0 {
		return err
	}
	return bridge.WriteFile(dizPath, content)
}

func ZipExpectedPartsFromDIZ(bridge ZipRuntimeBridge, dirPath string, allowRecover bool) int {
	if bridge == nil {
		return 0
	}
	dizPath := path.Join(dirPath, "file_id.diz")
	if expected, ok := bridge.GetZipExpectedParts(dirPath); ok {
		return expected
	}
	content, err := bridge.ReadFile(dizPath)
	if (err != nil || len(content) == 0) && allowRecover {
		recovered, recoverErr := RecoverZipDIZFromDirectory(bridge, dirPath)
		if recoverErr != nil || len(recovered) == 0 {
			bridge.CacheZipExpectedParts(dirPath, 0, 0)
			return 0
		}
		content = recovered
	} else if err != nil || len(content) == 0 {
		bridge.CacheZipExpectedParts(dirPath, 0, 0)
		return 0
	}
	expected := ParseZipExpectedPartsFromDIZ(content)
	dizChecksum, ok := bridge.GetKnownChecksum(dizPath)
	if !ok && len(content) > 0 {
		dizChecksum = crc32.ChecksumIEEE(content)
	}
	bridge.CacheZipExpectedParts(dirPath, expected, dizChecksum)
	return expected
}

func RecoverZipDIZFromDirectory(bridge ZipRuntimeBridge, dirPath string) ([]byte, error) {
	if bridge == nil {
		return nil, fmt.Errorf("bridge unavailable")
	}
	entries := bridge.ListZipDirEntries(dirPath)
	var archives []string
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if entry.Size <= 0 || entry.XferTime <= 0 {
			continue
		}
		if IsZipRecoverableArchiveName(entry.Name) {
			archivePath := path.Join(dirPath, entry.Name)
			archives = append(archives, archivePath)
		}
	}
	sort.Strings(archives)
	for _, archivePath := range archives {
		content, err := bridge.ReadZipEntry(archivePath, "file_id.diz")
		if err != nil || len(content) == 0 {
			continue
		}
		if writeErr := bridge.WriteFile(path.Join(dirPath, "file_id.diz"), content); writeErr != nil {
			return nil, writeErr
		}
		return content, nil
	}
	return nil, fmt.Errorf("file_id.diz not found in any archive")
}

func ZipDirComplete(entries []ZipEntryInfo, expected int) bool {
	if expected <= 0 {
		return false
	}
	return ZipDirPayloadCount(entries) >= expected
}

func CacheZipReleaseProgress(bridge ZipRuntimeBridge, dirPath string, present, total int) {
	if bridge == nil {
		return
	}
	bridge.SyncStatusMarkersForPath(dirPath, true)
}
