package plugin

import "goftpd/internal/user"

type Plugin interface {
	Name() string
	Init(config map[string]interface{}) error
	OnUpload(user *user.User, path string, filename string, size int64, speed float64) error
	OnDownload(user *user.User, path string, filename string, size int64) error
	OnDirList(user *user.User, path string) (string, error)
	Stop() error
}
