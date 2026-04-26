# GoFTPd Plugins

Plugins extend the daemon by reacting to events (dir created, file uploaded,
release nuked, etc.) and doing work with the master's VFS bridge.

## Built-in plugins

- **autonuke** - periodically scans release paths and auto-nukes empty, incomplete, half-empty, or banned releases using the normal nuke pipeline
- **tvmaze** - async TV show lookup on MKD, writes `.tvmaze` into the release dir
- **imdb** - async movie lookup on MKD (via imdbapi.dev), writes `.imdb`
- **speedtest** - creates fixed-size test files and emits SPEEDTEST events
- **slowupkick** - monitors live uploads and downloads, aborts/kicks slow transfers, and can briefly tempban the FTP user after a kick
- **spacekeeper** - combines free-space cleanup and archive-style moves using virtual FTP path rules, with separate switches for each side
- **releaseguard** - blocks bad release dir names before MKD creates them

TVMaze and IMDb metadata files are shown on `CWD` via the daemon's
`show_diz` mechanism.

## Writing a new plugin

1. Create a new package under `/plugins/<yourname>/handler.go`
2. Implement the `plugin.Plugin` interface
3. Register it in `cmd/goftpd/main.go`
4. Add a config block to `etc/config.yml`

### Minimal example

```go
package mything

import (
    "log"
    "goftpd/internal/plugin"
)

type Handler struct{ svc *plugin.Services }

func New() *Handler { return &Handler{} }

func (h *Handler) Name() string { return "mything" }

func (h *Handler) Init(svc *plugin.Services, cfg map[string]interface{}) error {
    h.svc = svc
    return nil
}

func (h *Handler) OnEvent(evt *plugin.Event) error {
    if evt.Type == plugin.EventMKDir {
        log.Printf("[mything] new dir: %s by %s", evt.Path, evt.User.Name)
    }
    return nil
}

func (h *Handler) Stop() error { return nil }
```

Then in `cmd/goftpd/main.go`, add a case:

```go
case "mything":
    p = mything.New()
```

And the config block:

```yaml
plugins:
  mything:
    enabled: true
    # any plugin-specific keys...
```

## Interface

```go
type Plugin interface {
    Name() string
    Init(svc *Services, config map[string]interface{}) error
    OnEvent(evt *Event) error
    Stop() error
}
```

Some plugins can also implement optional extra hooks. For example,
`releaseguard` implements `plugin.MKDirValidator` so it can deny a bad release
name before `MKD` creates the directory.

### Event types

| Constant            | Fires when                                     |
|---------------------|------------------------------------------------|
| `EventMKDir`        | A directory is created                         |
| `EventRMDir`        | A directory is removed                         |
| `EventUpload`       | A file is successfully uploaded (CRC verified) |
| `EventDownload`     | A file is downloaded                           |
| `EventDelete`       | A file is deleted                              |
| `EventRename`       | A file or directory is renamed                 |
| `EventCWD`          | A user changes directory                       |
| `EventLogin`        | A user logs in                                 |
| `EventLogout`       | A user logs out                                |
| `EventNuke`         | A release is nuked                             |
| `EventUnnuke`       | A nuke is reversed                             |
| `EventComplete`     | A release race finishes (all SFV files present)|

### Event struct

```go
type Event struct {
    Type     string       // one of the Event* constants above
    User     *user.User   // acting user (may be nil)
    Path     string       // full virtual path
    Filename string       // base name
    Size     int64        // bytes (UPLOAD/DOWNLOAD)
    Speed    float64      // bytes/sec (UPLOAD)
    Section  string       // first path component (auto-populated)
    From     string       // source path (RENAME)
    Extra    map[string]interface{}  // event-specific extras
}
```

### Services

```go
type Services struct {
    Bridge MasterBridge   // WriteFile, ReadFile, FileExists, GetFileSize
    Debug  bool
    Logger *log.Logger
}

type MasterBridge interface {
    PluginListDir(path string) []FileEntry
    WriteFile(path string, content []byte) error
    CreateSparseFile(path string, size int64, owner, group string) error
    ReadFile(path string) ([]byte, error)
    FileExists(path string) bool
    GetFileSize(path string) int64
    PluginGetVFSRaceStats(path string) (users []RaceUser, groups []RaceGroup, totalBytes int64, present int, total int)
}
```

## Rules

1. **OnEvent MUST return fast.** Don't block. Network I/O, API calls, or
   heavy work should be pushed to a goroutine or job queue.
2. **Plugins have no state between daemon restarts.** Persist anything you
   need via the bridge (write a file in the slave storage) or your own DB.
3. **Section matching** is up to you. Use `plugin.Event.Section` (first path
   component) and do case-insensitive substring matches - "TV" should match
   both `TV-1080P` and `TV-720P`.
4. **Release-name detection** - scene releases always have dots, a dash
   (group suffix), and a year or `SxxEyy` tag. Anything else is a subfolder
   (`Sample`, `Proof`, `Subs`, `CD1`...) and should be ignored.
5. **Slave mode.** If `svc.Bridge == nil` your plugin is running on a slave
   and should skip any work that requires VFS access.

## Example: log all uploads

```go
func (h *Handler) OnEvent(evt *plugin.Event) error {
    if evt.Type != plugin.EventUpload {
        return nil
    }
    log.Printf("[audit] %s uploaded %s (%d bytes @ %.1f MB/s)",
        evt.User.Name, evt.Path, evt.Size, evt.Speed/(1024*1024))
    return nil
}
```

## Example: write a tag file on release complete

```go
func (h *Handler) OnEvent(evt *plugin.Event) error {
    if evt.Type != plugin.EventComplete {
        return nil
    }
    tag := fmt.Sprintf("Completed at %s by %s\n",
        time.Now().Format(time.RFC3339), evt.User.Name)
    return h.svc.Bridge.WriteFile(evt.Path+"/.completed", []byte(tag))
}
```
