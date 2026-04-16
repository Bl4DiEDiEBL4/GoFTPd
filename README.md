# GoFTPd v1.0.1b

A modular, drftpd-inspired FTP daemon written in Go. Distributed master/slave architecture with pzs-ng-style zipscript, CRC verification, race stats, and TLS 1.3.

## Architecture

- **Master** — handles FTP clients, VFS in memory, routes transfers to slaves
- **Slave** — storage daemon, auto-reconnects to master, handles file I/O
- **Bridge** — master copies data between FTP client and slave, with CRC32 verification

Run a slave on the same machine as the master if you want local data serving.

## Features

- Distributed master/slave architecture
- TLS 1.3 with ECDSA P-384 certs (`TLS_AES_256_GCM_SHA384`)
- VFS-based zipscript (no disk writes during upload — everything live)
- CRC32 verification on upload (drftpd-style)
- 0-byte file rejection
- Virtual LIST entries (status bars, missing files, complete tags)
- Live race stats on CWD (250- response from VFS)
- pzs-ng-style SFV parsing
- XDUPE protocol support
- IMDb/TVMaze plugins
- Dupe checker
- Nuke/unnuke
- glftpd-compatible user/group files
- bcrypt + Apache MD5 (apr1) password hashing

## Build

```bash
./build.sh
```

Installs Go 1.26.2 if missing, downloads deps, builds `./goftpd` binary.

## TLS Certificates

```bash
./generate_certs.sh
```

Generates ECDSA P-384 CA, server, and client certs (10-year validity).

## Configuration

Three config files in `etc/`:

- `config.yml` — active config
- `config-master.yml` — master example
- `config-slave.yml` — slave example

Copy whichever one applies to `config.yml` before starting.

## Default Login

- **User:** `goftpd`
- **Password:** `goftpd`
- **Flags:** `1` (siteop)

Change the password immediately with `SITE CHPASS goftpd <newpass>`.

## SITE Commands

### Informational
- `SITE HELP` — show available commands
- `SITE RULES` — site rules
- `SITE WHO` — online users
- `SITE STAT` — your stats
- `SITE USER [user]` — user info
- `SITE TIME` — server time

### User Management (siteop only)
- `SITE ADDUSER <user> <pass> [ident@ip ...]` — create user
- `SITE DELUSER <user>` — delete user
- `SITE CHPASS <user> <newpass>` — change password
- `SITE ADDIP <user> <ident@ip> [...]` — add IP(s) to user
- `SITE DELIP <user> <ident@ip> [...]` — remove IP(s)
- `SITE FLAGS <user> <+|-|=><flags>` — modify user flags
  - `+1` add flag, `-1` remove flag, `=13` replace all flags
- `SITE CHGRP <user> <group> [...]` — toggle group membership
- `SITE CHPGRP <user> <group>` — set primary group
- `SITE GADMIN <user> <group>` — make user admin of group

### Group Management (siteop only)
- `SITE GRPADD <groupname> [description]` — create group
- `SITE GRPDEL <groupname>` — delete group
- `SITE GRP` — list groups

### Release Management
- `SITE NUKE <dir> <multiplier> <reason>` — nuke a release
- `SITE UNNUKE <dir>` — undo nuke

### Miscellaneous
- `SITE CHMOD <mode> <file>` — change permissions
- `SITE XDUPE <0|1|2|3|4>` — toggle XDUPE mode
- `SITE INVITE <nick>` — invite to sitebot channels

## User Flags (glftpd-compatible)

- `1` — Siteop (full admin)
- `2` — Group admin (gadmin)
- `3` — Regular user
- `4` — Exempt from stats
- `5` — Exempt from credits
- `6` — Can kick users
- `7` — Can see hidden directories
- `8` — Elite uploader

Flags can be combined: `FLAGS 13` = siteop + user.

## Race Stats

Race stats are generated **live** from VFS metadata on every CWD (250- response). No `.message` files written to disk. Templates in `etc/msgs/racestats_*.msg`:

- `racestats_header.msg`
- `racestats_usertop.msg`
- `racestats_groups.msg`
- `racestats_grouptop.msg`
- `racestats_footer.msg`

Template variables: `%n` (position), `%u` (user), `%g` (group), `%f` (files), `%m` (size MB), `%s` (speed MB/s), `%p` (percent), `%e` (expected total files).

## Zipscript (master-side, drftpd-style)

On every STOR:
1. Bridge data from client to slave (with CRC32 via io.TeeReader)
2. If 0 bytes → delete
3. If in SFV and CRC mismatch → delete
4. If in SFV and CRC matches → allowed
5. If NOT in SFV → allowed (NFO, sample, tags pass through)
6. Cache SFV data if `.sfv` file

LIST injects virtual entries:
- Progress bar during upload: `[####::::::::] - 40% Complete - [XXX]`
- Complete tag: `[XXX] - ( 1564M 17F - COMPLETE ) - [XXX]`
- Missing files: `filename.r05-MISSING`

## Directory Structure

```
/GoFTPd/
├── cmd/goftpd/          # main.go entry point
├── etc/
│   ├── config.yml       # active config
│   ├── config-master.yml
│   ├── config-slave.yml
│   ├── passwd           # password hashes
│   ├── users/           # user files
│   ├── groups/          # group files
│   ├── msgs/            # templates
│   └── certs/           # TLS certs
├── internal/
│   ├── config/          # config parsing
│   ├── core/            # FTP protocol, SITE commands
│   ├── master/          # master mode (bridge, VFS, slave manager)
│   ├── slave/           # slave mode
│   ├── protocol/        # master/slave wire protocol (gob)
│   ├── user/            # user loading/saving
│   ├── acl/             # ACL engine
│   └── plugin/          # plugin interface
├── plugins/
│   ├── imdb/
│   └── tvmaze/
├── userdata/            # VFS persistence
├── site/                # file storage (slave roots)
└── logs/
```

## Running

**Master:**
```bash
cp etc/config-master.yml etc/config.yml
./goftpd
```

**Slave:**
```bash
cp etc/config-slave.yml etc/config.yml
./goftpd
```

Edit `etc/config.yml` before starting (set sitename, IPs, roots, etc).

## Changelog

### v1.0.1b (2026-04-16)
- Added SITE FLAGS command (glftpd-style `+1`/`-1`/`=13`)
- SITE CHGRP now toggles group membership (drftpd-style)
- SITE ADDUSER now fails if user exists (suggests CHPASS/ADDIP)
- Added SITE DELUSER, SITE CHPASS, SITE ADDIP, SITE DELIP
- Fixed password verification: unknown `$`-prefixed formats now REJECTED
- Added Apache MD5 (apr1) password hash support
- Passwords now bcrypt-hashed on SITE ADDUSER/CHPASS
- Removed `.message` file writes (race stats now live on CWD)
- Added thread-safe write mutex on master/slave gob streams
- Increased slave response timeouts (60s) to prevent ACK timeouts
- Drftpd-style CRC verification: delete on mismatch, delete 0-byte files
- Pure ASCII race stats templates (renders correctly in all FTP clients)

### v1.0.0
- Initial master/slave architecture
- VFS-based zipscript replaces standalone plugin
- TLS 1.3 with ECDSA P-384

## License

MIT

## Credits

Inspired by drftpd, glftpd, and pzs-ng.
