# Sitebot Plugins

Sitebot plugins either:

- react to daemon events and post to IRC
- handle IRC `!commands`

## Built-in Plugins

### Announce

Formats event-driven announces such as:

- NEW
- RACE
- HALFWAY
- COMPLETE
- STATS
- PRE
- NUKE
- PRETiME

### TVMaze / IMDB

Async metadata output for TV and movie releases.

### News

IRC-side news storage and display:

- `!news`
- `!addnews`
- `!delnews`

### Free

Free space command surface:

- `!free`
- `!df`

### Affils

Shows configured affils:

- `!affils`

### Request

IRC wrapper around request SITE commands:

- `!request`
- `!requests`
- `!reqfill`
- `!reqdel`
- `!reqwipe`

### BNC

IRC-side FTP login/health checks:

- `!bnc`

### BW

Bandwidth summary:

- `!bw`

### Banned

Shows banned rules:

- `!banned`

### SelfIP

Self-service IP management:

- `!ip`
- `!ips`
- `!addip`
- `!delip`
- `!chgip`

### Quota

Trial/quota tracking plugin:

- `!quota`
- `!quotactl ...`

See:

- [Quota Plugin](Quota-Plugin)

### Top

Daily uploader leaderboard:

- `!top`

### Rules

Rules output:

- `!rules`

### Topic

Staff-only topic editing:

- `!topic #channel new topic text`

Supports FiSH-encrypted topics when a channel key exists.

### Control

Built-in staff bot control:

- `!refresh`
- `!restart`

### AdminCommander

IRC gateway for selected SITE commands:

- `!site ...`
- `!nuke ...`
- `!unnuke ...`

## Plugin Development

See:

- [C:\Users\nelis\Documents\GoFTPd\sitebot\plugins\README.md](C:\Users\nelis\Documents\GoFTPd\sitebot\plugins\README.md)

for the handler interface, event model, output routing, and async plugin
pattern.
