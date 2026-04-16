# GoFTPd v1.0.1b

A distributed FTP daemon written in Go with a drftpd-inspired master/slave architecture. Features VFS-based zipscript with CRC32 verification, CP437 race stats, TLS 1.3, and glftpd-compatible user management.

## Architecture

GoFTPd uses a master/slave model where transfers are bridged through the master:

```
FTP Client <--TLS--> Master (VFS, zipscript, auth) <--gob--> Slave (disk I/O)
```

- **Master** — FTP server, VFS in memory, CRC32 verification, race stats, user auth
- **Slave** — storage daemon, auto-reconnects, handles file reads/writes
- **Bridge** — master relays data between client and slave via `io.TeeReader` (calculates CRC32 on the fly)

To serve data locally, run a slave on the same machine as the master.

## Quick Start

```bash
# Build
./build.sh

# Generate TLS certs (ECDSA P-384, TLS 1.3 AES-256-GCM)
./generate_certs.sh

# Configure
cp etc/config-master.yml etc/config.yml
# Edit etc/config.yml — set public_ip, ports, storage_path

# Run master
./goftpd

# Run slave (same or different machine)
cp etc/config-slave.yml etc/config.yml
# Edit — set slave name, master_host, roots
./goftpd
```

**Default login:** `goftpd` / `goftpd` (siteop). Change immediately with `SITE CHPASS`.

## Features

**Transfer & Security**
- TLS 1.3 with ECDSA P-384 certs (`TLS_AES_256_GCM_SHA384`)
- AUTH TLS, PBSZ, PROT, SSCN, CPSV (FXP)
- PRET, PASV, PORT modes
- XDUPE duplicate file detection
- Thread-safe gob streams (write mutex on master + slave)

**Zipscript (master-side, drftpd-style)**
- CRC32 verification on upload via `io.TeeReader` — zero overhead
- 0-byte file rejection (unconditional delete)
- CRC mismatch → file deleted, client notified
- Files not in SFV → allowed (NFO, sample, tags pass through)
- Virtual LIST entries: progress bars, complete tags, `-MISSING` files
- Live race stats on CWD (250- response, CP437 box-drawing, ASCII art logo)
- No disk writes during upload — all state in VFS memory

**User Management**
- glftpd-compatible user/group files
- bcrypt + Apache MD5 (apr1) password hashing
- Unknown hash formats rejected (fail-closed)
- Per-user flags, groups, IPs, credits, ratios
- ACL engine for path-based permissions

**Plugins**
- IMDb — movie info lookup
- TVMaze — TV show info
- Sitebot — IRC integration (infrastructure)

## FTP Protocol Support

`FEAT` `OPTS` `USER` `PASS` `SYST` `TYPE` `REST` `PWD` `CWD` `CDUP` `MKD` `RMD` `SIZE` `MDTM` `DELE` `RNFR` `RNTO` `PASV` `PORT` `LIST` `MLSD` `STOR` `RETR` `ABOR` `NOOP` `PRET` `PBSZ` `PROT` `SSCN` `CPSV` `AUTH TLS` `SITE` `XDUPE`

## SITE Commands

**User Management** (flag `1` = siteop)

| Command | Usage | Description |
|---------|-------|-------------|
| `ADDUSER` | `<user> <pass> [ident@ip ...]` | Create user (fails if exists) |
| `DELUSER` | `<user>` | Delete user (can't delete self) |
| `CHPASS` | `<user> <newpass>` | Change password (bcrypt) |
| `ADDIP` | `<user> <ident@ip> [...]` | Add IP(s), auto-prefixes `*@` |
| `DELIP` | `<user> <ident@ip> [...]` | Remove IP(s) |
| `FLAGS` | `<user> <+\|-\|=><flags>` | Modify flags: `+1` add, `-1` remove, `=13` set |
| `CHGRP` | `<user> <group> [...]` | Toggle group membership |
| `CHPGRP` | `<user> <group>` | Set primary group |
| `GADMIN` | `<user> <group>` | Grant group admin |

**Group Management**

| Command | Usage | Description |
|---------|-------|-------------|
| `GRPADD` | `<name> [desc]` | Create group |
| `GRPDEL` | `<name>` | Delete group |
| `GRP` | | List groups |

**Informational**

| Command | Description |
|---------|-------------|
| `HELP` | Available commands |
| `RULES` | Site rules |
| `WHO` | Online users |
| `INVITE` | IRC channel invite |

**Release Management**

| Command | Usage | Description |
|---------|-------|-------------|
| `NUKE` | `<dir> <mult> <reason>` | Nuke release |
| `UNNUKE` | `<dir>` | Undo nuke |

**Misc:** `CHMOD`, `XDUPE`

## User Flags

| Flag | Role |
|------|------|
| `1` | Siteop (full admin) |
| `2` | Group admin |
| `3` | Regular user |
| `4` | Exempt from stats |
| `5` | Exempt from credits |
| `6` | Can kick users |
| `7` | See hidden dirs |
| `8` | Elite uploader |

## Race Stats

Rendered live from VFS on every `CWD` into a release directory. Uses CP437 box-drawing characters for proper rendering in FTP clients (FlashFXP, etc). Includes per-user and per-group stats with file count, size, speed, and percentage.

## Configuration

| File | Purpose |
|------|---------|
| `etc/config.yml` | Active config |
| `etc/config-master.yml` | Master example |
| `etc/config-slave.yml` | Slave example |
| `etc/passwd` | Password hashes |
| `etc/users/` | User files (glftpd format) |
| `etc/groups/` | Group files |
| `etc/msgs/` | Message templates (welcome, goodbye, rules, help) |

## Project Structure

```
cmd/goftpd/           Entry point
internal/
  config/             YAML config parsing
  core/               FTP protocol, SITE commands, race renderer, ACL, auth
  master/             Bridge, VFS, slave manager, remote slave
  slave/              Slave daemon, transfer handler
  protocol/           Master/slave wire protocol (gob-encoded)
  user/               User loading/saving (glftpd format)
  acl/                Path-based ACL engine
  plugin/             Plugin interface
plugins/
  imdb/               IMDb lookup
  tvmaze/             TVMaze lookup
  sitebot/            IRC bot config
```

## Changelog

### v1.0.1b
- Race stats rendered in code with CP437 box-drawing and ASCII art logo
- SITE FLAGS command (glftpd-style `+`/`-`/`=`)
- SITE CHGRP toggle (drftpd-style: add if not in group, remove if in)
- SITE ADDUSER/DELUSER/CHPASS/ADDIP/DELIP
- ADDUSER: fails if user exists, accepts multiple IPs, bcrypt-hashes password
- Fixed password verification: unknown `$`-formats now rejected
- Apache MD5 (apr1) hash support
- Removed `.message` disk writes — race stats fully live from VFS
- Write mutex on master + slave gob streams (fixes concurrent upload crashes)
- CRC32 verification via io.TeeReader during bridge upload
- 0-byte file rejection

### v1.0.0
- Initial master/slave architecture
- VFS-based zipscript
- TLS 1.3 with ECDSA P-384

## License

WTF

## Credits

Inspired by [drftpd](https://github.com/drftpd-ng/drftpd), [glftpd](https://glftpd.io), and [pzs-ng](https://github.com/pzs-ng/pzs-ng).
