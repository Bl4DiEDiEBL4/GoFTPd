// Package plugin defines the extensibility interface for goftpd.
//
// To write a plugin:
//
//  1. Create a new package under /plugins/<yourname>/
//  2. Implement the Plugin interface below
//  3. Register it in cmd/goftpd/main.go's plugin switch
//
// Plugins receive typed events via OnEvent. They have access to a Services
// handle (populated at Init) which exposes the master bridge, logger, and
// config. Plugins MUST NOT block in OnEvent — spawn a goroutine for any
// network I/O or long-running work.
//
// Example minimal plugin:
//
//	type MyPlugin struct{ svc *plugin.Services }
//	func New() *MyPlugin                                  { return &MyPlugin{} }
//	func (p *MyPlugin) Name() string                      { return "myplugin" }
//	func (p *MyPlugin) Init(s *plugin.Services, cfg map[string]interface{}) error {
//	    p.svc = s
//	    return nil
//	}
//	func (p *MyPlugin) OnEvent(evt *plugin.Event) error {
//	    if evt.Type == plugin.EventMKDir {
//	        go p.svc.Bridge.WriteFile(evt.Path+"/.mytag", []byte("hello"))
//	    }
//	    return nil
//	}
//	func (p *MyPlugin) Stop() error                       { return nil }
package plugin

import (
	"log"

	"goftpd/internal/user"
)

// Event types dispatched by the PluginManager. Plugins should check evt.Type
// and ignore events they don't care about.
const (
	EventMKDir    = "MKDIR"    // New directory created
	EventRMDir    = "RMDIR"    // Directory removed
	EventUpload   = "UPLOAD"   // File uploaded successfully
	EventDownload = "DOWNLOAD" // File downloaded
	EventDelete   = "DELETE"   // File deleted
	EventRename   = "RENAME"   // File/dir renamed
	EventCWD      = "CWD"      // User changed directory
	EventLogin    = "LOGIN"    // User logged in
	EventLogout   = "LOGOUT"   // User logged out
	EventNuke     = "NUKE"     // Release nuked
	EventUnnuke   = "UNNUKE"   // Release unnuked
	EventComplete = "COMPLETE" // Race complete (all SFV files present)
)

// Event carries information about a single occurrence. Not every field is
// populated for every event — e.g. EventMKDir leaves Size/Speed zero.
type Event struct {
	Type     string                 // One of the Event* constants above
	User     *user.User             // Acting user (may be nil for system-originated events)
	Path     string                 // Full virtual path (directory for MKDIR, file for UPLOAD)
	Filename string                 // Base name of file/dir
	Size     int64                  // Bytes transferred (UPLOAD/DOWNLOAD)
	Speed    float64                // Bytes/sec (UPLOAD)
	Section  string                 // First path component, e.g. "TV-1080P"
	From     string                 // Source path for RENAME
	Extra    map[string]interface{} // Free-form for event-specific data
}

// Services is the handle plugins use to interact with goftpd. Populated by
// the PluginManager when it calls Init(). Plugins should store the pointer
// and use it in OnEvent callbacks.
type Services struct {
	Bridge MasterBridge // VFS/slave access — WriteFile, ReadFile, PluginListDir, etc.
	Debug  bool         // Global debug flag
	Logger *log.Logger  // Optional logger (may be nil — fall back to log.Printf)
	EmitEvent func(eventType, path, filename, section string, size int64, speed float64, data map[string]string)
}

type SiteContext interface {
	Reply(format string, args ...interface{})
	UserName() string
	UserPrimaryGroup() string
	UserGroups() []string
}

type SiteCommandHandler interface {
	SiteCommands() []string
	HandleSiteCommand(ctx SiteContext, command string, args []string) bool
}

type RaceUser struct {
	Name    string
	Group   string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}

type RaceGroup struct {
	Name    string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}

type FileEntry struct {
	Name       string
	Size       int64
	IsDir      bool
	IsSymlink  bool
	LinkTarget string
	Mode       uint32
	ModTime    int64
	Owner      string
	Group      string
	Slave      string
}

// MasterBridge exposes the subset of the master's bridge that plugins need.
// This is a minimal surface — we can add methods as plugins require them.
type MasterBridge interface {
	PluginListDir(path string) []FileEntry
	MakeDir(path, owner, group string)
	Symlink(linkPath, targetPath string) error
	Chmod(path string, mode uint32) error
	RenameFile(from, toDir, toName string)
	WriteFile(path string, content []byte) error
	ReadFile(path string) ([]byte, error)
	ProbeMediaInfo(path, binary string, timeoutSeconds int) (map[string]string, error)
	FileExists(path string) bool
	GetFileSize(path string) int64
	PluginGetVFSRaceStats(dirPath string) (users []RaceUser, groups []RaceGroup, totalBytes int64, present int, total int)
}

// Plugin is the interface every goftpd plugin must implement.
type Plugin interface {
	// Name returns a short, unique identifier (e.g. "tvmaze", "imdb").
	// Used as the key in the plugin config block.
	Name() string

	// Init is called once at startup after RegisterPlugin. The plugin receives
	// service handles and its own config sub-map from the YAML. It should
	// store the svc pointer for later use.
	Init(svc *Services, config map[string]interface{}) error

	// OnEvent is called synchronously for every dispatched event. It MUST
	// return quickly — spawn a goroutine for network I/O or heavy work.
	// Errors are logged but don't stop dispatch to other plugins.
	OnEvent(evt *Event) error

	// Stop is called at shutdown. Plugins should flush state and close any
	// background workers.
	Stop() error
}
