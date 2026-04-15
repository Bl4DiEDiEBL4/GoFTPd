# GoFTPd - Modular FTP Daemon

Lightweight FTP daemon 

## SITE Commands

- `SITE HELP` - Show commands
- `SITE ADDUSER username password ip` - Add user
- `SITE DELUSER username` - Delete user
- `SITE CHANGE username field value` - Modify user
- `SITE CHPASS username password` - Change password
- `SITE NUKE dir multiplier reason` - Nuke directory
- `SITE UNNUKE dir` - Unnuke
- `SITE TAGLINE tag` - Change tagline
- `SITE STAT` - Your stats
- `SITE USER [user]` - User info
- `SITE TIME` - Server time

## Features

✅ glftpd-compatible users/groups
✅ Password hashing (bcrypt)
✅ TLS/SSL support
✅ File transfers with ratio/credits
✅ SITE commands
✅ Plugin system

## Flags (glftpd-style)

- 1 = Admin/SiteOp
- 2 = Upload
- 3 = Download
- 4 = Nuke
- 5 = Unnuke
- 6 = Delete
- 7 = MKD
- 8 = Rename
- A-Z = Custom


To install: 
```
git clone and run build.sh
chmod +x build.sh
./build.sh
```

ps. its far from finished!

Master: 
```
./goftpd
2026/04/14 12:30:39 [PLUGINS] Initializing plugin system...
2026/04/14 12:30:39 [PLUGIN-MANAGER] Registered plugin: zipscript
2026/04/14 12:30:39 [PLUGINS] Registered zipscript plugin
2026/04/14 12:30:39 [ZS-INIT] Zipscript initialized: storage=./site sitename=HVA
2026/04/14 12:30:39 [PLUGIN-MANAGER] Initialized 1 plugins
2026/04/14 12:30:39 [PLUGINS] All plugins initialized
2026/04/14 12:30:39 [DUPE] Enabled and initialized at ./logs/xdupe.db
2026/04/14 12:30:39 [VFS] Loaded 24 entries from userdata/vfs.dat
2026/04/14 12:30:39 [SlaveManager] Listening for slaves on 0.0.0.0:1099
2026/04/14 12:30:39 [MASTER] SlaveManager listening on port 1099, waiting for slaves...
2026/04/14 12:30:39 GoFTPd online at :21212 [Mode=master] [Plugins=1]
2026/04/14 12:30:45 [SlaveManager] Accepted connection from 127.0.0.1:57588
2026/04/14 12:30:45 [SlaveManager] Slave 'SLAVE1' connected from 127.0.0.1:57588
2026/04/14 12:30:45 [SlaveManager] Slave SLAVE1 disk: 69018MB free / 7560161MB total
2026/04/14 12:30:45 [SlaveManager] Starting remerge for slave SLAVE1 (instant online)
2026/04/14 12:30:45 [SlaveManager] Slave SLAVE1 is now AVAILABLE (remerge running in background)
2026/04/14 12:30:45 [SlaveManager] Remerge from SLAVE1: dir=/ files=1
2026/04/14 12:30:45 [SlaveManager] Remerge from SLAVE1: dir=/TV-1080P files=1
2026/04/14 12:30:45 [SlaveManager] Remerge from SLAVE1: dir=/TV-1080P/DMV.2025.S01E16.1080p.WEB.h264-ETHEL files=1
2026/04/14 12:30:45 [SlaveManager] Remerge from SLAVE1: dir=/TV-1080P/DMV.2025.S01E16.1080p.WEB.h264-ETHEL/Sample files=1
2026/04/14 12:30:45 [SlaveManager] Remerge from SLAVE1: dir=/TV-1080P/DMV.2025.S01E16.1080p.WEB.h264-ETHEL files=18
2026/04/14 12:30:45 [SlaveManager] Remerge complete for slave SLAVE1

Slave:

 ./goftpd
2026/04/14 12:30:33 [SLAVE] Name 'slave', connecting to master
2026/04/14 12:30:33 [SLAVE] Name=SLAVE1 Master=127.0.0.1:1099 Roots=[/slave1/site]
2026/04/14 12:30:33 [Slave] SLAVE1 connecting to master at 127.0.0.1:1099
2026/04/14 12:30:33 [Slave] Connected to master
2026/04/14 12:30:33 [Slave] Registered as 'SLAVE1', entering command loop
2026/04/14 12:30:33 [Slave] Received command: AsyncCommand[index=00, name=remerge, args=[/ false 0 1776162633436 false]]
2026/04/14 12:30:33 [Slave] Starting remerge from / across 1 roots
2026/04/14 12:30:33 [Slave] Remerge: scanning root /slave1/site
2026/04/14 12:30:33 [Slave] Remerge root /slave1/site done: sent 5 directories
2026/04/14 12:30:33 [Slave] Remerge complete: 19 files, 3 dirs across 5 sent directories
2026/04/14 12:30:35 [Slave] Error reading from master: EOF
2026/04/14 12:30:35 [Slave] Shutting down
2026/04/14 12:30:35 [Slave] Disconnected: lost connection to master: EOF
2026/04/14 12:30:35 [Slave] Reconnecting to master in 10 seconds...
2026/04/14 12:30:45 [Slave] SLAVE1 connecting to master at 127.0.0.1:1099
2026/04/14 12:30:45 [Slave] Connected to master
2026/04/14 12:30:45 [Slave] Registered as 'SLAVE1', entering command loop
2026/04/14 12:30:45 [Slave] Received command: AsyncCommand[index=00, name=remerge, args=[/ false 0 1776162645684 false]]
2026/04/14 12:30:45 [Slave] Starting remerge from / across 1 roots
2026/04/14 12:30:45 [Slave] Remerge: scanning root /slave1/site
2026/04/14 12:30:45 [Slave] Remerge root /slave1/site done: sent 5 directories
2026/04/14 12:30:45 [Slave] Remerge complete: 19 files, 3 dirs across 5 sent directories
2026/04/14 12:31:15 [Slave] Received command: AsyncCommand[index=01, name=ping, args=[]]
2026/04/14 12:31:45 [Slave] Received command: AsyncCommand[index=02, name=ping, args=[]]
```


