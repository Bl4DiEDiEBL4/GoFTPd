# GoSitebot Plugins

Plugins extend the sitebot by reacting to events from weaveftpd (uploads, race
finishes, nukes, IRC commands, etc.) and producing IRC output (PRIVMSG /
NOTICE) that the bot sends to channels.

## Built-in plugins

- **announce** - formats race events into IRC announces (NEW / RACE / HALFWAY / COMPLETE / STATS)
- **tvmaze** - async TV-show lookup on MKD, posts TV-INFO to channels
- **imdb** - async movie lookup on MKD, posts MOVIE-INFO to channels
- **news** - handles `!news`, `!addnews`, `!delnews` IRC commands, persists to JSONL
- **free** - handles `!free` (disk space) IRC command
- **affils** - handles `!affils`, showing configured affil groups from the shared affils file
- **request** - handles `!request`, `!requests`, `!reqfill`, `!reqdel`, `!reqwipe`
- **bnc** - handles `!bnc` FTP login health checks across one or more configured targets
- **bw** - handles `!bw`, querying SITE BW through the daemon
- **admincommander** - staff bridge for `!site`, `!nuke`, `!unnuke` and other allowed SITE commands
- **banned** - handles `!banned` by querying `SITE BANNED`
- **selfip** - handles `!ip`, `!ips`, `!addip`, `!delip`, `!chgip` through `SITE SELFIP`
- **quota** - tracks trial/quota users from WeaveFTPd user files and handles `!quota` plus staff `!quotactl ...`
- **top** - handles `!top`, reading daily upload stats from weaveftpd user files with optional user/group excludes and timed auto-announce
- **rules** - handles `!rules`, reading a configured rules file or falling back to `SITE RULES`
- **topic** - handles staff-only `!topic #channel topic text`, using FiSH topic encryption when a channel key exists
- **control** - built-in staff control surface for `!refresh` and `!restart`

## Writing a new plugin

1. Create `sitebot/plugins/<yourname>/handler.go` in package `<yourname>`
2. Implement the `plugin.Handler` interface (4 methods below)
3. Register it in `sitebot/internal/bot/bot.go` (`initializePlugins`)
4. Add a config entry under `plugins:` in `sitebot/etc/config.yml`

### Minimal example

```go
package myplugin

import (
    "log"

    "weaveftpd/sitebot/internal/event"
    "weaveftpd/sitebot/internal/plugin"
)

type MyPlugin struct {
    debug bool
}

func New() *MyPlugin { return &MyPlugin{} }

func (p *MyPlugin) Name() string { return "MyPlugin" }

func (p *MyPlugin) Initialize(config map[string]interface{}) error {
    if v, ok := config["debug"].(bool); ok {
        p.debug = v
    }
    return nil
}

func (p *MyPlugin) OnEvent(evt *event.Event) ([]plugin.Output, error) {
    if evt.Type != event.EventUpload {
        return nil, nil
    }
    if p.debug {
        log.Printf("[MyPlugin] upload by %s: %s", evt.User, evt.Filename)
    }
    return []plugin.Output{
        {Type: "MYTHING", Text: "uploaded " + evt.Filename + " by " + evt.User},
    }, nil
}

func (p *MyPlugin) Close() error { return nil }
```

Then in `sitebot/internal/bot/bot.go` `initializePlugins()`:

```go
import myplugin "weaveftpd/sitebot/plugins/myplugin"

if enabled, ok := b.Config.Plugins.Enabled["MyPlugin"]; !ok || enabled {
    p := myplugin.New()
    cfg := map[string]interface{}{"debug": b.Debug}
    for k, v := range b.Config.Plugins.Config {
        cfg[k] = v
    }
    if err := p.Initialize(cfg); err != nil {
        return err
    }
    if err := b.Plugins.Register(p); err != nil {
        return err
    }
}
```

And in `sitebot/etc/config.yml`:

```yaml
plugins:
  enabled:
    MyPlugin: true
  config:
    myplugin:
      some_key: "some value"
```

## Interface

```go
type Handler interface {
    Name() string
    Initialize(config map[string]interface{}) error
    OnEvent(evt *event.Event) ([]Output, error)
    Close() error
}

type Output struct {
    Type   string  // tag like "RACE", "STATS", "NEW", "TV-INFO" - used by routeChannels
    Text   string  // line(s) to send; \n splits into multiple PRIVMSGs
    Target string  // explicit channel (overrides routing); leave "" to use routeChannels
    Notice bool    // true -> NOTICE instead of PRIVMSG
}
```

### Output routing

Two ways your plugin's output gets to a channel:

**1. Explicit Target** - used by command-driven plugins (news, free). When `Output.Target` is set, the bot sends straight there. Example: `!news` typed in `#weaveftpd-chat` -> the news plugin returns `Output{Target: "#weaveftpd-chat", Text: "..."}` -> reply lands in `#weaveftpd-chat`.

**2. Empty Target -> routeChannels** - used by event-driven plugins (announce, tvmaze, imdb). The bot looks up channels via:
  - `announce.type_routes[Output.Type]` first (per-type override, e.g. NUKE -> `#weaveftpd-nuke`)
  - then `sections[*].channels` matching `evt.Section` or path glob
  - then `announce.default_channel`
  - then `irc.channels`

So setting `Type: "RACE"` and the right `evt.Section` is usually enough - config decides where it goes.

### Async output (lookups, slow APIs)

If your plugin needs to do HTTP/network work, **don't** block in `OnEvent`. The pattern used by tvmaze and imdb:

1. `OnEvent` enqueues a job and returns immediately
2. A worker goroutine drains the queue and does the HTTP call
3. When the call returns, the worker uses an "async emitter" callback (set by the bot in `initializePlugins`) to push lines to IRC

See `tvmaze/handler.go` `SetAsyncEmitter` for the exact pattern. To use it, your plugin exposes:

```go
func (p *MyPlugin) SetAsyncEmitter(fn func(outType, text, section, relpath string)) {
    p.asyncEmit = fn
}
```

...and the bot wires it in `initializePlugins` like the existing tvmaze block.

## Event types (from weaveftpd FIFO)

| Constant              | Fires when                                              |
|-----------------------|---------------------------------------------------------|
| `EventMKDir`          | New directory created                                   |
| `EventRMDir`          | Directory removed                                       |
| `EventUpload`         | File uploaded successfully (CRC verified)               |
| `EventDownload`       | File downloaded                                         |
| `EventDelete`         | File deleted                                            |
| `EventRename`         | File or directory renamed                               |
| `EventNuke`           | Release nuked                                           |
| `EventUnnuke`         | Nuke reversed                                           |
| `EventRaceEnd`        | Race complete (COMPLETE line)                           |
| `EventRaceStats`      | STATS_HOF + STATS_SPEEDS                                |
| `EventRaceUser`       | One per racer in HOF                                    |
| `EventRaceFooter`     | STATS_END line                                          |
| `EventNewUser`        | New user added via SITE ADDUSER                         |
| `EventLoginFail`      | Login denied (unknown user, deleted user, bad password, IP issue, etc.) |
| `EventInvite`         | SITE INVITE - bot sends the IRC invite and plugins can observe it too |
| `EventCommand`        | IRC `!cmd` from a user (news, free, etc.)               |
| `EventDiskStatus`     | Slave disk status report                                |
| `EventNewDay`         | Dated dir rollover announcement                         |
| `EventAudioInfo`      | Audio metadata announce                                 |
| `EventMediaInfo`      | Video/sample metadata announce                          |
| `EventCustom`         | Custom JSON FIFO event from an external script          |
| `EventSpeedtest`      | Speedtest upload/download result                        |
| `EventPre`            | SITE PRE                                                |
| `EventPreBW`          | Bandwidth summary at end of PRE                         |
| `EventPreBWUser`      | Per-user bandwidth in PRE                               |
| `EventPreBWInterval`  | Interval bandwidth samples during PRE                   |

## Event struct

```go
type Event struct {
    Type      EventType
    Timestamp time.Time
    User      string
    Group     string
    Section   string             // e.g. "TV-1080P"
    Filename  string             // base name (file or dir)
    Size      int64              // bytes (UPLOAD/DOWNLOAD)
    Speed     float64            // MB/s (UPLOAD)
    Path      string             // full virtual path
    Data      map[string]string  // free-form extras (template vars, etc.)
}
```

### Custom FIFO events from external scripts

The sitebot also accepts direct JSON-line events written to the configured
`event_fifo`. This lets external scripts announce to IRC directly.

Use event type `CUSTOM` and put your final IRC line in `data.message`.
Optionally set:

- `data.announce_type` for routing via `announce.type_routes`
- `data.template` to force a specific announce theme key

If `data.template` is empty, the announce plugin first tries a theme key with
the same name as `data.announce_type`, then falls back to `CUSTOM`.

For archive-style helpers, a common pattern is:

- `SPACEARCHIVE`
- `SPACEOFFSITE`
- `SPACEDELETE`

Those can be routed in `sitebot/plugins/announce/config.yml` like:

```yaml
type_routes:
  SPACEARCHIVE: ["#weaveftpd-archive"]
  SPACEOFFSITE: ["#weaveftpd-archive"]
  SPACEDELETE: ["#weaveftpd-archive"]
```

Example JSON line:

```json
{
  "type": "CUSTOM",
  "timestamp": "2026-05-13T18:00:00Z",
  "user": "external-script",
  "group": "automation",
  "section": "ARCHIVE",
  "filename": "Example.Release-GRP",
  "path": "/ARCHiVE/TV-1080P/Example.Release-GRP",
  "size": 0,
  "speed": 0,
  "data": {
    "message": "[SPACE]: [ARCHiViNG] :: Example.Release-GRP :: [1432 MB] to [ARCHIVE-TV-1080P]",
    "announce_type": "SPACEARCHIVE",
    "template": "SPACEARCHIVE",
    "orig_section": "TV-1080P",
    "destination_label": "ARCHIVE-TV-1080P",
    "size_mb": "1432"
  }
}
```

Example Python:

```python
import json
from datetime import datetime, timezone

fifo_path = "/WeaveFTPd_master/etc/weaveftpd.sitebot.fifo"

evt = {
    "type": "CUSTOM",
    "timestamp": datetime.now(timezone.utc).isoformat(),
    "user": "external-script",
    "group": "automation",
    "section": "ARCHIVE",
    "filename": "Example.Release-GRP",
    "path": "/ARCHiVE/TV-1080P/Example.Release-GRP",
    "size": 0,
    "speed": 0,
    "data": {
        "message": "[SPACE]: [ARCHiViNG] :: Example.Release-GRP :: [1432 MB] to [ARCHIVE-TV-1080P]",
        "announce_type": "SPACEARCHIVE",
        "template": "SPACEARCHIVE",
        "orig_section": "TV-1080P",
        "destination_label": "ARCHIVE-TV-1080P",
        "size_mb": "1432"
    }
}

with open(fifo_path, "w") as fifo:
    fifo.write(json.dumps(evt) + "\n")
```

Matching theme keys can live in `sitebot/etc/templates/*.theme`, for example:

```text
announce.SPACEARCHIVE = "%c14{[SPACE]:} %c09{[ARCHiViNG]} %c14{::} %c14{%relname} %c14{::} %c06{[}%c14{%size_mb} MB%c06{]} %c14{to} %c07{[}%c14{%destination_label}%c07{]}"
announce.SPACEOFFSITE = "%c14{[SPACE]:} %c13{[OFFSiTE]} %c14{::} %c14{%relname} %c14{::} %c06{[}%c14{%size_mb} MB%c06{]} %c14{to} %c07{[}%c14{%destination_label}%c07{]}"
announce.SPACEDELETE = "%c14{[SPACE]:} %c04{[DELETiNG]} %c14{::} %c14{%relname} %c14{::} %c06{[}%c14{%size_mb} MB%c06{]} %c14{from} %c07{[}%c14{%destination_label}%c07{]}"
```

## Rules

1. **OnEvent must return fast.** No HTTP calls, no SQL, no file I/O - push that to a goroutine.
2. **Plugins are called serially** by the manager. A slow plugin blocks every other plugin for that event.
3. **Section gating is your job.** Use `evt.Section` (case-insensitive substring) to decide which sections your plugin cares about.
4. **Don't write to disk casually.** If you need persistence (like news), use a JSONL or SQLite file under `sitebot/data/`.
5. **Panics are caught.** The plugin manager recovers from panics in `OnEvent` - your bug won't crash the bot. But it will log an error and skip your output.

## Theme files

The announce plugin reads templates from `sitebot/etc/templates/pzsng.theme` by default (or whatever `announce.theme_file` points to). A bundled `sitebot/etc/templates/drftpd.theme` preset is also available for a DrFTPD-like announce style. Format is one key per block:

```
NEWDIR
NEW : [%section] %relname by %u_name

UPDATE_RAR
RACE: [%section] %relname got its first rar from %u_name at %u_speed
```

Variables come from `vars()` in the announce plugin - anything in `evt.Data` is also exposed as `%key`. Templates are pure substitution, no logic.

For easier theme translation, the announce plugin also exposes a small DrFTPD-style alias set in addition to WeaveFTPd's native names: `%user`, `%group`, `%file`, `%path`, `%speed`, `%size`, `%files`, `%racers`, `%leaduser`, `%leadsize`, `%leadfiles`, `%leadpercent`, `%leadspeed`, `%filesleft`, and `%sectioncolor`.

## Examples to study

- **announce** - pure event consumer, no I/O, returns formatted strings. Good template for new event-driven plugins.
- **news** - command-driven (replies to `!news`), uses `Output.Target`, persists to JSONL.
- **tvmaze / imdb** - event-driven + async HTTP via `SetAsyncEmitter` pattern.
- **free** - minimal command-driven plugin, talks to weaveftpd's disk-status feed.
