package zipscript

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func LocalCheckUploadedZipIntegrity(cfg Config, dirPath, localPath, fileName string) (bool, error) {
	if !CheckZipIntegrityForDir(cfg, dirPath) {
		return false, nil
	}
	if !strings.HasSuffix(strings.ToLower(strings.TrimSpace(fileName)), ".zip") {
		return false, nil
	}

	r, err := zip.OpenReader(localPath)
	if err != nil {
		_ = os.Remove(localPath)
		return true, nil
	}
	defer r.Close()
	return false, nil
}

func LocalRefreshZipDIZFromArchive(dirPath, archivePath, fileName string) error {
	if !IsZipRecoverableArchiveName(fileName) {
		return nil
	}
	dizPath := filepath.Join(dirPath, "file_id.diz")
	if _, err := os.Stat(dizPath); err == nil {
		return nil
	}

	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if !strings.EqualFold(filepath.Base(f.Name), "file_id.diz") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || len(data) == 0 {
			return err
		}
		return os.WriteFile(dizPath, data, 0644)
	}

	return os.ErrNotExist
}
