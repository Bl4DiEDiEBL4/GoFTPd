# Daemon Overview

## Architecture

GoFTPd is built around a master/slave model.

### Master

The master owns:

- FTP command handling
- authentication
- ACL checks
- VFS state
- slave routing
- SITE commands
- plugin dispatch
- FIFO event output to the sitebot

### Slaves

Slaves own:

- real files on disk
- upload and download I/O
- local roots
- free space reporting

## Routing

Uploads are routed to writable slaves using:

- `slaves[].sections`
- `slaves[].paths`
- `weight`
- available space

Read-only slaves are used for scans and downloads, but not for new uploads.

## VFS

The master maintains a virtual filesystem built from what slaves report.

Important behavior:

- configured root/section directories are protected
- stale random top-level roots are no longer auto-protected
- restart/remerge should preserve real folder mtimes instead of stamping them
  with daemon-restart time

## Users and Groups

GoFTPd uses:

- `etc/passwd`
- `etc/group`
- `etc/users/<username>`

User files carry:

- flags
- ratio
- credits
- day/week/month stats
- groups
- IP masks

Flag `6` is now treated as a real disabled-account flag during login.

## Transfers

The daemon tracks:

- uploads
- downloads
- race progress
- credits
- zipscript release state

Speedtest transfers are counted as traffic but do not affect credits.
