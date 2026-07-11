//go:build windows

package core

import "os"

// fileOwnerUsername reports the FTP username that owns a file on disk.
// On unix this maps the file's uid through the passwd config. Windows has no
// uid, and NTFS owner SIDs all resolve to the daemon's service account rather
// than an FTP user, so attribution is impossible here. Report "unknown" so
// callers (SITE NUKE credit attribution) skip the file instead of penalizing
// a bogus user for every upload.
func fileOwnerUsername(info os.FileInfo, cfg *Config) (string, bool) {
	return "", false
}
