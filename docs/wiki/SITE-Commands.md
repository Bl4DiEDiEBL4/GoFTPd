# SITE Commands

GoFTPd exposes a broad set of scene-style `SITE` commands.

## Examples

Common commands include:

- `SITE HELP`
- `SITE RULES`
- `SITE WHO`
- `SITE USERS`
- `SITE USER`
- `SITE SEEN`
- `SITE LASTLOGIN`
- `SITE GROUPS`
- `SITE GINFO`
- `SITE TRAFFIC`
- `SITE ADDUSER`
- `SITE GADDUSER`
- `SITE DELUSER`
- `SITE CHPASS`
- `SITE ADDIP`
- `SITE DELIP`
- `SITE FLAGS`
- `SITE CHGRP`
- `SITE CHPGRP`
- `SITE GADMIN`
- `SITE INVITE`
- `SITE NUKE`
- `SITE UNNUKE`
- `SITE WIPE`
- `SITE KICK`
- `SITE REHASH`
- `SITE REMERGE`
- `SITE RACE`
- `SITE SEARCH`
- `SITE RESCAN`
- `SITE CHMOD`
- `SITE PRE`
- `SITE ADDAFFIL`
- `SITE DELAFFIL`
- `SITE AFFILS`
- `SITE REQUEST`
- `SITE REQUESTS`
- `SITE REQFILL`
- `SITE REQFILLED`
- `SITE REQDEL`
- `SITE REQWIPE`
- `SITE BW`
- `SITE SELFIP`

## Invite

`SITE INVITE <nick>` asks the sitebot to invite an IRC nick into the channels
allowed by the sitebot invite rules.

The bot handles the IRC invite directly, and sitebot plugins can observe the
event too.

## Slave Security Commands

There is also slave-auth ban visibility and management:

- `SITE SLAVEBANS`
- `SITE SLAVEBAN <ip|cidr>`
- `SITE SLAVEUNBAN <ip|cidr>`

## Command Permissions

Access is controlled through `sitecmd` ACL rules in:

- `etc/permissions.yml`

That is where you decide who can use which SITE commands.
