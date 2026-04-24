# GoFTPd

GoFTPd is a distributed FTP daemon written in Go. It uses a master/slave
architecture with a VFS, CRC/SFV checking, race stats, TLS data transfers,
SITE commands, plugin hooks, and an IRC sitebot.

## Architecture

The master owns FTP control, authentication, ACL checks, VFS state, SITE
commands, race state, plugin dispatch, and sitebot event output. Slaves own
the actual disk I/O.

Passthrough mode is the default. In this mode the client connects directly to
the selected slave for data transfers, while the master keeps control-plane
state:

```text
FTP client <--TLS data--> slave storage
    |
    +-- FTP control --> master
```

Proxy mode is available when slaves are not reachable by clients:

```text
FTP client <--TLS data--> master <--TCP--> slave storage
```

| Mode | Data path | Use when |
|------|-----------|----------|
| Passthrough | client <-> slave | slaves have reachable passive ports |
| Proxy | client <-> master <-> slave | only the master is reachable |

FXP is supported in passthrough mode, including secure data channels using
PASV/CPSV/PORT, SSCN, and PROT P.

## Quick Start

```bash
./build.sh
./generate_certs.sh "My FTPd"
cp etc/config-example.yml etc/config.yml
./goftpd

cd sitebot
./build.sh
cp etc/config.yml.example etc/config.yml
./sitebot -config etc/config.yml
```

For a first-time guided setup, use:

```bash
./setup-interactive.sh
```

It asks for master/slave mode, ports, PASV/proxy style, certificate name,
channel names, Blowfish keys, and per-plugin enable flags only when the real
config files do not exist yet. It also saves the answers in
`etc/setup-interactive.env` so a reinstall or move can reuse them as defaults.

Edit `etc/config.yml` before running it for real. The same config file is used
for master and slave mode; `mode: master` or `mode: slave` decides which blocks
are active.

Edit `sitebot/etc/config.yml` before starting the sitebot. The daemon and
sitebot must use the same `event_fifo` path.

The example user is `goftpd` / `goftpd`. Change that before exposing the
daemon.

## Configuration

Main files:

| File | Purpose |
|------|---------|
| `etc/config.yml` | Active daemon config |
| `etc/config-example.yml` | Annotated daemon example |
| `etc/permissions.yml` | ACL rules |
| `etc/passwd` | Password hashes |
| `etc/users/` | User records |
| `etc/groups/` | Group records |
| `etc/groups/default.group` | Template for newly created groups |
| `etc/affils.yml` | Shared affil PRE config |
| `etc/msgs/` | Text shown by SITE RULES and login/logout messages |
| `sitebot/etc/config.yml` | Active sitebot config |
| `sitebot/etc/config.yml.example` | Annotated sitebot example |
| `sitebot/etc/templates/pzsng.theme` | Sitebot theme templates |

Plugin defaults are shipped as `config.yml.dist` files inside each plugin
directory. Copy them to `config.yml` and point the main config at those files:

```yaml
plugins:
  pre:
    enabled: true
    config_file: "plugins/pre/config.yml"
```

This keeps site-local plugin settings out of Git-tracked example files.

## Slaves

The master can route uploads by section, path, and weight:

```yaml
slaves:
  - name: "SLAVE1"
    sections: ["TV-1080P", "TV-720P"]
    weight: 2
  - name: "ARCHIVE"
    readonly: true
    weight: 1
```

Selection order is section/path match, then lowest `activeTransfers / weight`,
then most free disk space. If writable policies exclude every slave, the master
falls back to available writable slaves. Read-only slaves are used for scanning
and downloads, but are not selected for uploads.

For an archive server, point a slave root at the existing archive tree and set
`readonly: true`. Use `sections` or `paths` only if you want to limit which
paths that archive slave serves.

## ACL Rules

ACLs live in `etc/permissions.yml`. Rules are checked top to bottom inside each
rule type. The first matching rule decides access. If no action rule matches,
matching `privpath` rules are checked. If nothing matches, flag `1` users are
allowed by default.

Supported rule subjects:

| Syntax | Meaning |
|--------|---------|
| `*` | everyone |
| `!*` | nobody |
| `1`, `A` | required user flag |
| `!4` | user must not have flag `4` |
| `=Admin` | member of group `Admin` |
| `!=Trial` | not a member of group `Trial` |
| `@Nick` | FTP username `Nick` |
| `!@Nick` | not FTP username `Nick` |
| `1 =NUKERS` | flag `1` and member of `NUKERS` |
| `=GRP =SiteOP` | member of `GRP` or `SiteOP` |

Rule types currently used by the example config:

`sitecmd`, `privpath`, `upload`, `resume`, `download`, `makedir`, `dirlog`,
`rename`, `renameown`, `nuke`, `unnuke`, `delete`, `deleteown`, `filemove`,
and `nodupecheck`.

Owner-only rules are handled by `renameown` and `deleteown`; the ACL grants the
policy and the daemon checks ownership before allowing the action.

## Transfers And Credits

- TLS control/data support: `AUTH TLS`, `PBSZ`, `PROT`, `SSCN`, `CPSV`.
- Transfer commands: `PASV`, `PORT`, `PRET`, `STOR`, `RETR`, `REST`, `ABOR`.
- Listings: `LIST`, `MLSD`, `MLST`.
- CRC/SFV verification is done during upload.
- Existing-file overwrite protection still applies, including paths marked
  `nodupecheck`.
- User records support flags, groups, IP masks, credits, ratios, and password
  hashes.
- Password hashing supports bcrypt and Apache MD5 (`apr1`). Unknown `$...`
  formats are rejected.
- Speedtest traffic is free: it does not cost credits and does not apply credit
  gain/loss.

## Daemon Plugins

Daemon plugins are enabled under `plugins:` in `etc/config.yml`. Plugin-specific
defaults can live in separate files referenced with `config_file`, and only
plugins with `enabled: true` are loaded.

Built-in daemon plugins:

| Plugin | What it does |
|--------|--------------|
| `dateddirs` | Creates date-based section folders and today symlinks |
| `tvmaze` | Writes `.tvmaze` metadata for configured TV sections |
| `imdb` | Writes `.imdb` metadata for configured movie sections |
| `mediainfo` | Emits audio/video metadata events after uploads |
| `pre` | Provides SITE PRE and affil management commands |
| `request` | Provides SITE REQUEST/REQUESTS/REQFILL/REQDEL/REQWIPE |
| `speedtest` | Creates speedtest files and emits SPEEDTEST events |

### Speedtest

When enabled, the speedtest plugin creates the configured directory and sparse
files, by default:

```text
/SPEEDTEST/100MB
/SPEEDTEST/500MB
/SPEEDTEST/1000MB
```

Uploads and downloads in that directory emit `SPEEDTEST` events for the
sitebot. Credits are not charged or awarded for speedtest transfers.

### Requests

When enabled, the request plugin creates the configured base directory on
startup, by default `/REQUESTS`.

SITE commands:

| Command | Usage |
|---------|-------|
| `REQUEST` | `<release> [-for:<user>]` |
| `REQUESTS` | show current requests |
| `REQFILL` / `REQFILLED` | `<number|release>` |
| `REQDEL` | `<number|release>` |
| `REQWIPE` | `<number|release>` |

`REQDEL` is owner-safe. A user can delete their own request. Privileged users
can wipe requests with `REQWIPE`. The sitebot request plugin can pass the IRC
nick through using `-by:<nick>` only when the FTP login is listed in
`request.proxy_users`.

### PRE And Affils

The PRE plugin reads `etc/affils.yml` and can manage affil entries through SITE
commands. It can update `etc/permissions.yml` and group data when adding or
removing affils.

SITE commands:

| Command | Usage |
|---------|-------|
| `PRE` | `<release> <section>` |
| `ADDAFFIL` | `<group>` |
| `DELAFFIL` | `<group>` |
| `AFFILS` | list configured affils |

## SITE Commands

Implemented daemon SITE commands include:

| Area | Commands |
|------|----------|
| Info | `HELP`, `RULES`, `WHO`, `SWHO`, `USERS`, `USER`, `SEEN`, `LASTLOGIN`, `GROUPS`, `GROUP`, `GINFO`, `GRPNFO`, `TRAFFIC` |
| Users/groups | `ADDUSER`, `GADDUSER`, `DELUSER`, `READD`, `RENUSER`, `CHPASS`, `ADDIP`, `DELIP`, `FLAGS`, `CHGRP`, `CHPGRP`, `GADMIN`, `GRPADD`, `GRPDEL`, `GRP` |
| Release/admin | `NUKE`, `UNNUKE`, `UNDUPE`, `WIPE`, `KICK`, `REHASH`, `REMERGE`, `CHMOD` |
| Search/rescan | `SEARCH`, `RACE`, `RESCAN`, `XDUPE` |
| IRC/sitebot | `INVITE` |
| Plugins | `PRE`, `ADDAFFIL`, `DELAFFIL`, `AFFILS`, `REQUEST`, `REQUESTS`, `REQFILL`, `REQFILLED`, `REQDEL`, `REQWIPE` |

Command access is controlled through `sitecmd` ACL rules in
`etc/permissions.yml`.

Account command notes:

- `GADDUSER <user> <pass> <group> [ident@ip ...]` creates a user and places it
  in the given group immediately.
- `DELUSER <user>` now moves the user into `etc/users/.deleted/` instead of
  permanently removing the account file.
- `READD <user> [newpass]` restores a user deleted with `DELUSER`. If the old
  password hash was preserved, `newpass` is optional.
- `RENUSER <olduser> <newuser>` renames both the user file and the passwd
  entry.

## Sitebot

The sitebot reads daemon events from the configured FIFO and posts to IRC. It
supports channel routing, per-channel Blowfish keys, themed output, command
plugins, and SIGHUP reload.

Built-in sitebot plugins:

| Plugin | Commands/events |
|--------|-----------------|
| `Announce` | race, stats, PRE, nuke, speedtest, metadata events |
| `TVMaze` | TV lookup output |
| `IMDB` | movie lookup output |
| `News` | `!news`, `!addnews`, `!delnews` |
| `Free` | `!free`, `!df` |
| `Affils` | `!affils` |
| `Request` | `!request`, `!requests`, `!reqfill`, `!reqdel`, staff `!reqwipe` |
| `AdminCommander` | staff-only IRC gateway for configured SITE commands |

The example sitebot config uses YAML anchors for channel sets, so a channel can
be changed once at the top and reused across sections and plugin config.

Sitebot command plugins can use the same split config layout:

```yaml
plugins:
  config:
    request:
      config_file: "plugins/request/config.yml"
    admin_commander:
      config_file: "plugins/admincommander/config.yml"
```

The repo ships matching `sitebot/plugins/<name>/config.yml.dist` files.

### Theme Output

The theme file supports `%b{}` bold, `%cNN{}` mIRC colors, `%u{}` underline,
and `%{var}` or `%var` variables. `\n` inside a template becomes multiple IRC
lines.

Request command output has theme keys:

```text
REQUESTCMD_OK
REQUESTCMD_LINE
REQUESTCMD_ERROR
REQUESTCMD_DENIED
REQUESTCMD_USAGE
```

Admin command output has theme keys:

```text
ADMINCMD_OK
ADMINCMD_LINE
ADMINCMD_ERROR
ADMINCMD_DENIED
ADMINCMD_BLOCKED
ADMINCMD_USAGE
```

### Channel Routing

Event output routes in this order:

1. `announce.type_routes`
2. matching `sections[*].channels`
3. `announce.default_channel`
4. `irc.channels`

Command plugins can reply directly to the channel, by notice, or to a fixed
channel depending on their `reply_target`.

## Runtime Reload

`SITE REHASH` reloads daemon config pieces that are safe to update at runtime,
including affils, PRE settings, slave policies, lookup toggles, TLS enforcement
flags, IP restrictions, limits, show_diz map, nuke style, and debug.

`SITE RESCAN <path|path/*>` checks release files against SFV data. `SITE
REMERGE <slave|*>` asks connected slave(s) to rescan their filesystem roots and
refresh the master's VFS index.

The sitebot also supports SIGHUP reload for channels, encryption keys, theme,
sections, plugin config, and announce routing without dropping the IRC
connection.

Some low-level settings still need a restart, such as listen ports, passive
port ranges, TLS cert/key paths, storage path, mode, master control port, and
event FIFO path.

## Project Layout

```text
cmd/goftpd/          daemon entry point
internal/            core FTP, VFS, slave, ACL, user, event, and protocol code
plugins/             daemon plugins
sitebot/             IRC sitebot
sitebot/plugins/     sitebot plugins
etc/                 daemon example config, ACLs, users, groups, messages
```

## Development Notes

Daemon plugin notes live in `plugins/README.md`.
Sitebot plugin notes live in `sitebot/plugins/README.md`.

Current plugin development still requires registration in the relevant switch:

- daemon plugins: `cmd/goftpd/main.go`
- sitebot plugins: `sitebot/internal/bot/bot.go`

## License

MIT

## Credits

Inspired by [drftpd](https://github.com/drftpd-ng/drftpd), [glftpd](https://glftpd.io), and [pzs-ng](https://github.com/pzs-ng/pzs-ng).
