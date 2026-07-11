//go:build windows

package slave

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func createSymlink(targetPath, fullLink string) error {
	err := os.Symlink(targetPath, fullLink)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.Errno(1314)) {
		return fmt.Errorf("Windows symlink creation requires Developer Mode or administrator privileges: %w", err)
	}
	return err
}
