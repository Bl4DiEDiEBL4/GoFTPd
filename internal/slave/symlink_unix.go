//go:build !windows

package slave

import "os"

func createSymlink(targetPath, fullLink string) error {
	return os.Symlink(targetPath, fullLink)
}
