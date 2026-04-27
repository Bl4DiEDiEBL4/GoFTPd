# Daemon Plugins

Daemon plugins react to master-side events such as:

- MKDIR
- UPLOAD
- DELETE
- NUKE
- COMPLETE

They are enabled in `etc/config.yml`.

## Built-in Plugins

### autonuke

Auto-nukes bad releases such as:

- empty
- incomplete
- banned-name
- half-empty

### dateddirs

Creates dated section directories and NEWDAY behavior.

### tvmaze

Looks up TV metadata on new release directories and writes `.tvmaze`.

### imdb

Looks up movie metadata and writes `.imdb`.

### mediainfo

Extracts media/sample metadata and emits announce events for the sitebot.

### speedtest

Creates fixed-size speedtest files and emits speedtest events.

### request

Implements FTP-side request handling:

- `SITE REQUEST`
- `SITE REQUESTS`
- `SITE REQFILL`
- `SITE REQDEL`
- `SITE REQWIPE`

### pre

Handles PRE behavior and affil-related directory logic.

### pretime

Looks up release pre times from:

- SQLite
- MySQL
- HTTP/JSON APIs

and emits:

- `NEWPRETIME`
- `OLDPRETIME`

### slowkick

Watches live uploads and downloads and can:

- warn on slow transfers
- kick slow users
- tempban them briefly so they do not immediately reclaim the slot

### spacekeeper

Master-side free-space and archive plugin.

Supports:

- `delete_oldest`
- `archive_oldest`

Archive can target:

- other roots on the same slave
- other archive slaves in a target pool

It can free space on archive targets by deleting older archived releases when
needed to make room.

### releaseguard

Blocks bad release names before the directory is created.

## Writing New Plugins

See:

- [C:\Users\nelis\Documents\GoFTPd\plugins\README.md](C:\Users\nelis\Documents\GoFTPd\plugins\README.md)

for the plugin interface, events, bridge helpers, and examples.
