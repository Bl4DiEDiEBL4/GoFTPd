# GoFTPd

> Still under active development. Things may change or break.

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
./setup.sh build
./setup.sh certs "My FTPd"
cp etc/config-example.yml etc/config.yml
./goftpd

cd sitebot
cp etc/config.yml.example etc/config.yml
./sitebot -config etc/config.yml
```

For a first-time guided setup, use:

```bash
./setup.sh install
```

If you only want to compile both binaries without touching config setup, use:

```bash
./setup.sh build
```

If you only want to generate fresh TLS certificates, use:

```bash
./setup.sh certs "My FTPd"
```

It asks for master/slave mode, ports, PASV/proxy style, certificate name,
channel names, a Blowfish key per channel, sitebot IRC settings, sitebot FTP
plugin settings, an optional `!rules` file path, and per-plugin enable flags
only when the real config files do not exist yet. It also saves the answers in
`etc/setup-interactive.env` so a reinstall or move can reuse them as defaults.
In slave mode it asks only the slave-specific daemon questions and skips the
sitebot flow unless you explicitly choose to configure a sitebot there too.
If the saved defaults file exists, the installer asks whether to load it and
use those values as the prompt defaults for the new run.
If your main daemon or sitebot config already exists, rerunning `./setup.sh install`
still creates any newly added plugin `config.yml` files from their `.dist`
templates and asks whether to enable just those new plugins.

To back up generated interactive configs and start over cleanly, use:

```bash
./setup.sh clean
```

Cleanup mode:

- backs up generated daemon and sitebot configs
- backs up generated plugin `config.yml` files
- backs up and removes the shared FIFO
- backs up and removes generated TLS certs in `etc/certs`
- keeps `etc/setup-interactive.env` so your saved answers survive the reset

Account import helpers are also built into `setup.sh`:

```bash
./setup.sh import-glftpd /glftpd
./setup.sh import-drftpd3 /drftpd
./setup.sh import-drftpd4 /drftpd
```

`import-drftpd3` derives groups from DrFTPD v3 user JSON and preserves
plaintext passwords only when they are actually present, rehashing them to
bcrypt during import. Masked values such as `*` still require a siteop reset.
`import-drftpd4` reads both user and group javabeans and preserves only
supported password formats safely.

Edit `etc/config.yml` before running it for real. The same config file is used
for master and slave mode; `mode: master` or `mode: slave` decides which blocks
are active.

You can also point the daemon at an explicit config file:

```bash
./goftpd --config etc/config-slave-example.yml
```

In `mode: slave`, the slave-specific runtime comes from the `slave:` block.
`slave.roots` is required, and slave mode no longer falls back to
`storage_path` for roots. Shared top-level runtime settings such as `tls_*`,
`log_*`, `debug`, and `timezone` still apply to both roles. If the master
requires encrypted data transfers, the slave config also needs matching
`tls_enabled`, `tls_cert`, and `tls_key` values because the slave terminates
the client data TLS session.

For a slave-only box, start from the minimal sample instead of trimming down
the full master config:

```bash
cp etc/config-slave-example.yml etc/config-slave.yml
vim etc/config-slave.yml
./goftpd --config etc/config-slave.yml
```

Edit `sitebot/etc/config.yml` before starting the sitebot. The daemon and
sitebot must use the same `event_fifo` path.
External scripts can also write JSON-line `CUSTOM` events to that FIFO for
direct IRC announces; see `sitebot/plugins/README.md` for examples.

The example user is `goftpd` / `goftpd`. Change that before exposing the
daemon.

## Configuration

Main files:

| File | Purpose |
|------|---------|
| `etc/config.yml` | Active daemon config |
| `etc/config-example.yml` | Annotated daemon example |
| `etc/config-slave-example.yml` | Minimal slave-only daemon example |
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

For a slave-only install, copy the minimal sample and run it from the same
folder as the built binary:

```bash
cp etc/config-slave-example.yml etc/config-slave.yml
./goftpd --config etc/config-slave.yml
```

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

Slave roots can also be split into two clear buckets:

- `roots`: normal site roots mounted at `/`
- `mounted_roots`: extra physical roots mounted under a virtual prefix such as
  `/ARCHiVE`

This is useful when one slave should expose normal site paths from one local
tree and archive content from other disks under a shared virtual prefix without
putting mergerfs in front of GoFTPd itself. This is especially useful when your
archive disks are already mounted individually over NFS and you want GoFTPd to
talk to those real paths directly instead of scanning one big merged mount.
Example:

```yml
slave:
  roots:
    - "/goftpd/site"
  mounted_roots:
    - path: "/goftpd/DISK1"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK2"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK3"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK4"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK5"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK6"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK7"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK8"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK9"
      mount_path: "/ARCHiVE"
    - path: "/goftpd/DISK10"
      mount_path: "/ARCHiVE"
```

With that setup, `/X265/...` resolves under `/goftpd/site`, while
`/ARCHiVE/EBOOKS/...` resolves directly against `/goftpd/DISK1`,
`/goftpd/DISK2`, `/goftpd/DISK3`, and so on. That keeps archive access on the
real per-disk mounts, which is usually much faster and more predictable than a
large mergerfs pool layered over NFS.

On a full remerge, GoFTPd scans normal `roots` first and only scans
`mounted_roots` after that. That keeps the live site tree responsive while the
larger archive trees catch up in the background, which makes `mounted_roots`
especially suitable for archive-style storage.

For slower local disks, NFS mounts, or archive disks, throttle slave remerge
instead of letting a full scan pin one disk at 100% iowait:

```yml
slave:
  remerge_delay_ms: 10
  remerge_pause_on_active_transfers: 1
```

`remerge_delay_ms` sleeps after each streamed directory. Keep it at `0` on fast
local disks; try `5-25` on slower disks. `remerge_pause_on_active_transfers`
pauses remerge while the slave is serving uploads/downloads, so live traffic
gets priority over background scans.

On a slave host, the role-specific settings you normally care about are:

- `mode: slave`
- the `slave:` block
- shared top-level runtime settings like `tls_*`, `log_*`, `debug`, and `timezone`

Master-only site layout, ACL storage, plugin routing, and sitebot settings are
ignored in slave mode.

The master control socket can also be hardened with:

- `master.slave_allowlist` - optional exact IP / CIDR allowlist for slave connections
- `master.slave_denylist_file` - persistent exact IP / CIDR denylist for the slave control socket
- `master.slave_auth_fail_limit`
- `master.slave_auth_fail_window_seconds`
- `master.slave_auth_ban_seconds`

Those guard only the slave control port, not normal FTP client sessions.

Siteops can manage the persistent denylist from FTP with:

- `SITE SLAVEBANS`
- `SITE SLAVEBAN <ip|cidr>`
- `SITE SLAVEUNBAN <ip|cidr>`

## Restart And Remerge

The master persists VFS state to `userdata/vfs.dat`.

On a normal master restart, the first reconnect from a slave with cached VFS
entries now reuses that cached tree instead of immediately forcing a full
startup remerge. Later reconnects in the same runtime still use normal remerge
behavior.

`master.remerge_mode` controls startup reconnect behavior:

- `off`: keep the slave unavailable until startup remerge finishes
- `instant`: keep the slave online while the VFS fills in

`master.manual_remerge_mode` controls `SITE REMERGE` separately and defaults to
`instant`, so large hand-triggered remerges do not take the site offline.

If `master.remerge_checksums: true`, remerge only asks slaves for CRCs when the
current VFS entry does not already have a stored checksum. Unchanged files with
preserved checksum state should not be re-CRC'd just because the master was
restarted.

For sites where files can disappear outside GoFTPd, for example external archive
or cleanup scripts, enable the master's slow background VFS patrol:

```yml
master:
  background_remerge_interval_seconds: 21600 # every 6 hours, 0 disables
  background_remerge_start_delay_seconds: 300
  background_remerge_stagger_seconds: 60
  background_remerge_path: "/"
  background_remerge_roots_only: false # keep false when the goal is stale cleanup
  background_remerge_skip_busy_slaves: true

slave:
  remerge_delay_ms: 10
  remerge_pause_on_active_transfers: 1
```

The master only schedules the patrol; the slave-side knobs make it gentle. As
each scanned directory reports back, stale direct VFS children are pruned right
away, so old directories do not need to wait for the whole tree to finish.

## ACL Rules

ACLs live in `etc/permissions.yml`. Rules are checked top to bottom inside each
rule type. The first matching rule decides access. If no action rule matches,
matching `privpath` rules are checked. If nothing matches, flag `1` users are
allowed by default.

`acl_base_path` controls which virtual tree the ACL engine matches against.
The shipped default is now `acl_base_path: "/"`, so the permissions file
mirrors the visible FTP tree directly. A visible FTP path like `/MP3/Release`
is checked against ACL rules as `/MP3/Release`.

The example config now uses the structured ACL format with reusable `roles:`
and grouped rule blocks such as `sitecmd:`, `list:`, and `nuke:`.

`required:` supports readable fields such as:

| Key | Meaning |
|-----|---------|
| `anyone: true` | everyone |
| `nobody: true` | nobody |
| `users: ["goftpd"]` | exact FTP user match |
| `all_flags: ["1"]` | user must have all listed flags |
| `any_flags: ["1", "A"]` | user must have at least one listed flag |
| `all_groups: ["NUKERS"]` | user must be in all listed groups |
| `any_groups: ["STAFF", "SiteOP"]` | user must be in at least one listed group |
| `not_users`, `not_flags`, `not_groups` | explicit deny filters |
| `any_of:` | OR across nested requirement blocks |

Example:

```yml
roles:
  siteop:
    any_of:
      - all_groups: ["SiteOP"]
      - all_flags: ["1"]

rules:
  sitecmd:
    - allow: [HELP, RULES, AFFILS]
      required:
        anyone: true

    - allow: [REHASH, ADDUSER]
      required: $siteop
```

The legacy flat syntax is still supported for compatibility, so older entries
such as `required: "1 =NUKERS"` continue to load.

Rule types currently used by the example config:

`sitecmd`, `privpath`, `upload`, `resume`, `download`, `makedir`, `list`,
`dirlog`, `rename`, `renameown`, `nuke`, `unnuke`, `delete`, `deleteown`,
`filemove`, and `nodupecheck`.

Owner-only rules are handled by `renameown` and `deleteown`; the ACL grants the
policy and the daemon checks ownership before allowing the action.

## Transfers And Credits

- TLS control/data support: `AUTH TLS`, `PBSZ`, `PROT`, `SSCN`, `CPSV`.
- Transfer commands: `PASV`, `PORT`, `PRET`, `STOR`, `RETR`, `REST`, `ABOR`.
- Listings: `LIST`, `MLSD`, `MLST`.
- CRC/SFV verification is done during upload.
- Successful transfer-checksum chatter is suppressed on the control channel;
  only mismatch warnings are still emitted there.
- Existing-file overwrite protection still applies, including paths marked
  `nodupecheck`.
- User records support flags, groups, IP masks, credits, ratios, and password
  hashes.
- Password hashing supports bcrypt and Apache MD5 (`apr1`). Unknown `$...`
  formats are rejected.
- Speedtest traffic is free: it does not cost credits and does not apply credit
  gain/loss.

## Daemon Plugins

Daemon plugins are enabled under `plugins:` in `etc/config.yml`. That main
`plugins:` block is the only on/off switch. Plugin-specific files referenced
with `config_file` hold settings only.

Built-in daemon plugins:

| Plugin | What it does |
|--------|--------------|
| `autonuke` | Periodically scans configured release paths and uses the normal nuke pipeline for empty, incomplete, half-empty, or banned releases |
| `dateddirs` | Creates date-based section folders and today symlinks |
| `tvmaze` | Writes `.tvmaze` metadata for configured TV sections |
| `imdb` | Writes `.imdb` metadata for configured movie sections |
| `pretime` | Looks up known pre times for new release dirs and emits appended `NEWPRETIME` / `OLDPRETIME` announces |
| `pre` | Provides SITE PRE and affil management commands |
| `releaseguard` | Blocks bad release dir names before MKD creates them and provides `SITE BANNED` |
| `request` | Provides SITE REQUEST/REQUESTS/REQFILL/REQTOP/REQDEL/REQWIPE |
| `speedtest` | Creates speedtest files and emits SPEEDTEST events |
| `slowkick` | Monitors live uploads and downloads and aborts/kicks users whose speed stays below a configured floor long enough to block slots |
| `spacekeeper` | Watches slave free space and deletes the oldest eligible releases from configured virtual paths when a slave drops below its threshold |

Audio and sample/video metadata are not a separate daemon plugin anymore. They
are built into the zipscript upload flow under `zipscript.audio` and
`zipscript.media`, and are announced through the sitebot announce plugin as
`AUDIOINFO` / `MEDIAINFO` events.

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
| `REQTOP` | `[limit]` show request fill leaderboard |
| `REQDEL` | `<number|release>` |
| `REQWIPE` | `<number|release>` |

`REQDEL` is owner-safe. A user can delete their own request. Privileged users
can wipe requests with `REQWIPE`. The sitebot request plugin can pass the IRC
nick through using `-by:<nick>` only when the FTP login is listed in
`request.proxy_users`. Filled requests are tracked in the request plugin's
stored metadata on the request root, and `REQTOP` ranks users by filled
request count without relying on `.reqfills` text files.

### Slowkick

The slowkick plugin watches live uploads and downloads and only acts after a
transfer stays below the configured speed floor for the full verify window.
When it fires, the master tells the slave to abort the transfer first and then
disconnects the FTP session, so partial uploads are cleaned up on the slave
side instead of being left behind as junk. Upload and download thresholds can
be tuned independently, and the plugin can temporarily ban the FTP user after
a slow kick so they cannot immediately reconnect and grab the slot again.

### Pretime

The pretime plugin watches release-root `MKDIR` events and looks up the
release in one or more configurable backends:

- SQLite
- MySQL
- PostgreSQL
- HTTP/JSON API providers such as `predb.club` or `predb.net`

When a unix pre timestamp is found, it emits either `NEWPRETIME` or
`OLDPRETIME` with a formatted release age string for the sitebot. This is
append-only on purpose: the normal `NEW` line is already emitted by the daemon
before plugins run, so trying to replace it here would be racy.

### Spacekeeper

The spacekeeper plugin is a master-side free-space cleaner. It watches
reported free space per slave and, when a configured slave falls below its
trigger threshold, deletes the oldest eligible releases from the configured
virtual FTP paths until the target threshold is reached or nothing else is
safe to touch. It also supports archive-style relocation into a destination
path through `archive_oldest` rules. Delete rules do not use `destination` at
all. The two sides can be switched independently with
`enable_freespace_actions` and `enable_archive_actions`, and both skip active
transfers and incomplete releases by default. Dated bucket directories such as
`0426`, `20260426`, and `2026-04-26` are skipped as cleanup/archive targets.
Archive rules can also create dated destination buckets under the configured
destination by enabling `destination_dated` and setting
`destination_date_format` (same tokens as the `dateddirs` plugin, such as
`MMDD`, `YYYYMMDD`, or `YYYY-WW`).
Archive jobs run in the background. If the destination path routes to another
slave, spacekeeper performs a real cross-slave copy and only removes the
source after the destination copy completes successfully. If the destination
stays on the same slave, it uses the slave-local relocate path instead. If you
do not want archive behavior tied to normal upload routing, set
`target_slaves` on the rule and spacekeeper will pick the healthiest available
slave from that list explicitly. If no listed archive slave currently has
enough room for the incoming release, spacekeeper will try to free room on one
of those archive slaves by deleting the oldest archived releases already under
that rule's destination path until the new release fits.

Example delete rule:

```yml
- name: "0day-delete"
  slave: "SLAVE1"
  action: "delete_oldest"
  paths:
    - "/0DAY/*/*"
  trigger_free_gb: 30
  target_free_gb: 50
```

Example archive rule:

```yml
- name: "0day-archive"
  slave: "SLAVE1"
  action: "archive_oldest"
  destination: "/ARCHiVE/0DAY"
  destination_dated: true
  destination_date_format: "MMDD"
  target_slaves: ["ARCHIVE1", "ARCHIVE2"]
  paths:
    - "/0DAY/*/*"
  trigger_free_gb: 30
  target_free_gb: 50
```

### PRE And Affils

The PRE plugin uses `etc/affils.yml` as the source of truth for affil groups
and predirs. It can manage affil entries through SITE commands and sync the
derived PRE visibility rules into `etc/permissions.yml` when affils are added
or removed.

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
| Info | `HELP`, `RULES`, `WHO`, `SWHO`, `BW`, `USERS`, `USER`, `SEEN`, `LASTLOGIN`, `GROUPS`, `GROUP`, `GINFO`, `GRPNFO`, `TRAFFIC` |
| Users/groups | `ADDUSER`, `GADDUSER`, `DELUSER`, `READD`, `RENUSER`, `CHANGE`, `CHPASS`, `CHRATIO`, `CHNUMLOGINS`, `CHMAXSIM`, `CHWKLYALLOTMENT`, `CHUPLOADSLOTS`, `CHDOWNLOADSLOTS`, `GROUPSLOTS`, `GROUPSIMULT`, `ADDIP`, `DELIP`, `SELFIP`, `BAN`, `UNBAN`, `FLAGS`, `CHGRP`, `CHPGRP`, `TAGLINE`, `GADMIN`, `GRPADD`, `GRPDEL`, `GRP` |
| Release/admin | `NUKE`, `UNNUKE`, `NUKES`, `UNDUPE`, `WIPE`, `KICK`, `REHASH`, `REMERGE`, `CHMOD` |
| Search/rescan | `SEARCH`, `RACE`, `RESCAN`, `XDUPE` |
| Stats/traffic | `ALLUP`, `ALLDN`, `WKUP`, `WKDN`, `DAYUP`, `DAYDN`, `MONTHUP`, `MONTHDN` |
| IRC/sitebot | `INVITE`, `BLOWFISH`, `IRC` |
| Plugins | `PRE`, `ADDAFFIL`, `DELAFFIL`, `AFFILS`, `REQUEST`, `REQUESTS`, `REQFILL`, `REQFILLED`, `REQTOP`, `REQDEL`, `REQWIPE`, `BANNED`, `SELFIP` |

Command access is controlled through `sitecmd` ACL rules in
`etc/permissions.yml`.

Account command notes:

- `GADDUSER <user> <pass> <group> [ident@ip ...]` creates a user and places it
  in the given group immediately.
- `GADDUSER` is the only built-in gadmin-capable account-management command by
  default. A group admin can only add users for groups they manage.
- `DELUSER <user>` now moves the user into `etc/users/.deleted/` instead of
  permanently removing the account file.
- `READD <user> [newpass]` restores a user deleted with `DELUSER`. If the old
  password hash was preserved, `newpass` is optional.
- `RENUSER <olduser> <newuser>` renames both the user file and the passwd
  entry.
- `CHANGE` is a convenience wrapper for built-in user/group limit edits such as
  ratio, login limits, slot counts, weekly allotment, and group simult.
- Use `SITE CHANGE HELP` to list the canonical field names.
- Canonical `SITE CHANGE` fields are `RATIO`, `NUM_LOGINS`, `MAX_SIM`,
  `WKLY_ALLOTMENT`, `UPLOADSLOTS`, `DOWNLOADSLOTS`, `GROUP_SLOTS`,
  `GROUP_SIMULT`, and `TAGLINE`.
- `GROUPSLOTS <gadmin-user> <slots> [leech_slots]` stores how many users that
  gadmin may create and how many of those may be leech users.
- `CHWKLYALLOTMENT <user> <credits>` sets the weekly credit replacement amount.
  GoFTPd persists the applied week and replaces credits once per week instead
  of adding to the current credit total.
- `CHRATIO` stays siteop-controlled in the shipped ACL defaults.
- `BLOWFISH` and `IRC` are informational sitebot helpers. `SITE IRC` reads the
  sitebot connection info from `sitebot_config`, and `SITE BLOWFISH` falls back
  to `sitebot_config` when no daemon-side Blowfish mirror is configured.

## Sitebot

The sitebot reads daemon events from the configured FIFO and posts to IRC. It
supports channel routing, per-channel Blowfish keys, themed output, command
plugins, and SIGHUP reload.

External scripts can also write JSON-line events directly to the same
`event_fifo`. The built-in announce plugin supports a generic `CUSTOM` event:

- set `type` to `CUSTOM`
- put the final IRC text in `data.message`
- optionally set `data.announce_type` for `announce.type_routes`
- set `path` and `section` if you want normal section/path-based channel routing

See `sitebot/plugins/README.md` for a full JSON and Python example.

Built-in sitebot plugins:

| Plugin | Commands/events |
|--------|-----------------|
| `Announce` | race, stats, PRE, nuke, speedtest, metadata events |
| `TVMaze` | TV lookup output |
| `IMDB` | movie lookup output |
| `News` | `!news`, `!addnews`, `!delnews` |
| `Free` | `!free`, `!df` |
| `BNC` | `!bnc` FTP login checks across configured targets |
| `BW` | `!bw` bandwidth summary |
| `Affils` | `!affils` |
| `Quota` | `!quota` plus staff `!quotactl trial|quota|extend|delete`, tracking trial/quota users from GoFTPd user files |
| `Request` | `!request`, `!requests`, `!reqfill`, `!reqdel`, staff `!reqwipe` |
| `Banned` | `!banned`, `!banned <filter>`, `!banned allow [filter]` |
| `SelfIP` | `/msg BotNick !ip`, `!ips`, `!addip`, `!delip`, `!chgip` for PM-only self-service IP management |
| `Top` | `!top`, `!top 5`, `!top 10`, `!top 25`, optional timed TOP announce |
| `Rules` | `!rules` (reads `rules_file` when configured, otherwise uses `SITE RULES`) |
| `Topic` | staff-only `!topic #channel topic text` with FiSH topic encryption when a key exists |
| `Control` | staff-only `!refresh` and `!restart` for live config/plugin reload and bot re-exec |
| `AdminCommander` | staff-only IRC gateway for configured SITE commands |

The example sitebot config uses YAML anchors for channel sets, so a channel can
be changed once at the top and reused across sections and plugin config. By
default, race and section announces stay in `#goftpd`, while user `!` commands
are meant for `#goftpd-chat`.

For FiSH keys, the sitebot accepts either a plain key or `cbc:<key>`. Plain
keys default to CBC. `ecb:` is intentionally rejected. The interactive
`setup.sh` prompt still expects raw keys and may write the `cbc:` prefix for
clarity. `encryption.keys` are used per channel, while
`encryption.private_key` is used for encrypted PM/NOTICE replies and encrypted
commands sent directly to the bot nick. If `encryption.auto_exchange` is
enabled, the sitebot will also do DH1080 auto key exchange for private
PM/NOTICE traffic with users. That auto-exchange is peer-to-peer only; channel
and topic FiSH still use the static per-channel keys from `encryption.keys`.

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
The `quota` sitebot plugin keeps its own small YAML state file, so it can
remember trial starts and IRC nick mappings without needing SQLite just for
bot-side bookkeeping.

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

Staff/security notices such as failed logins, self-service IP changes, and bad
slave-control handshakes can be routed to `#goftpd-staff` through
`announce.type_routes`.

## Runtime Reload

`SITE REHASH` reloads daemon config pieces that are safe to update at runtime,
including affils, PRE settings, slave policies, lookup toggles, TLS enforcement
flags, IP restrictions, limits, show_diz map, nuke style, and debug.

`SITE RESCAN <path|path/*>` checks release files against SFV data. `SITE
REMERGE <slave|*>` asks connected slave(s) to rescan their filesystem roots and
refresh the master's VFS index. Its online/offline behavior is controlled by
`master.manual_remerge_mode`.

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

