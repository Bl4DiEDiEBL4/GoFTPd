package zipscript

import "path"

type SFVRuntimeBridge interface {
	GetSFVData(dirPath string) map[string]uint32
	GetKnownChecksum(filePath string) (uint32, bool)
	DeleteFile(filePath string) error
	MarkFileMissing(filePath string) error
	GetFileSize(filePath string) int64
	WriteFile(filePath string, data []byte) error
}

func ShouldTreatDownloadAsMissing(cfg Config, bridge SFVRuntimeBridge, filePath string, debugLog func(string, ...any)) bool {
	if bridge == nil {
		return false
	}
	dirPath := path.Dir(filePath)
	expectedCRC, exists := CachedExpectedCRC(bridge.GetSFVData(dirPath), path.Base(filePath))
	if !exists || expectedCRC == 0 {
		return false
	}

	if knownCRC, ok := bridge.GetKnownChecksum(filePath); ok {
		if knownCRC == expectedCRC {
			return false
		}
		if ShouldDeleteBadCRCForDir(cfg, dirPath) {
			if err := bridge.DeleteFile(filePath); err != nil && debugLog != nil {
				debugLog("[MASTER-ZS] cached bad CRC delete failed for %s: %v", filePath, err)
			}
			_ = bridge.MarkFileMissing(filePath)
			missingPath := filePath + "-MISSING"
			if bridge.GetFileSize(missingPath) < 0 {
				_ = bridge.WriteFile(missingPath, []byte{})
			}
		}
		return true
	}

	return false
}
