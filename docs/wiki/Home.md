# GoFTPd Wiki

Welcome to the GoFTPd wiki.

This wiki is meant to be a cleaner, more navigable companion to the main
README. It is split into daemon, sitebot, plugin, and operator-facing pages so
you can find the part you actually need without digging through one long file.

## Start Here

- [Installation and Setup](Installation-and-Setup)
- [Configuration](Configuration)
- [Daemon Overview](Daemon-Overview)
- [Daemon Plugins](Daemon-Plugins)
- [SITE Commands](SITE-Commands)
- [Sitebot](Sitebot)
- [Sitebot Plugins](Sitebot-Plugins)

## Notable Plugin Pages

- [Quota Plugin](Quota-Plugin)

## What GoFTPd Is

GoFTPd is a scene-style FTP daemon with:

- master/slave architecture
- virtual filesystem routing
- user/group/ACL handling
- race tracking and zipscript logic
- plugin hooks in the daemon
- an IRC sitebot with command and announce plugins

The master owns policy, routing, VFS state, commands, and event output.
Slaves own the real disk I/O.

## Good Mental Model

- **Daemon**: the FTP server itself
- **Plugins**: extra daemon logic reacting to events
- **Sitebot**: IRC output and command surface
- **Sitebot plugins**: IRC commands and extra announce behavior

## Current Documentation Layout

These pages were generated from the current repo state and are meant to be
copied into the GitHub wiki as-is or with light editing.

Repo-local source copies live under:

- [C:\Users\nelis\Documents\GoFTPd\docs\wiki](C:\Users\nelis\Documents\GoFTPd\docs\wiki)
