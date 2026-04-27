# Sitebot

The sitebot reads events from the daemon FIFO and posts them to IRC.

It handles:

- announce formatting
- channel routing
- command plugins
- FiSH encryption
- DH1080 auto key exchange for PM traffic

## Core Behavior

The bot reads the FIFO configured by:

- daemon: `event_fifo`
- sitebot: `event_fifo`

Both sides must point to the same path.

## IRC Features

The sitebot supports:

- SSL IRC connections
- auto-oper
- auto-join
- per-channel FiSH keys
- encrypted PM replies
- DH1080 auto-exchange for private bot traffic

## Routing

Output is routed by:

1. explicit `type_routes`
2. matching section routes
3. `announce.default_channel`
4. fallback IRC channels

## Pretime Rendering

Pretime rendering is sitebot-side.

Modes:

- `newline`
  - normal `NEW`
  - then `PRETiME`

- `inline`
  - briefly holds the visible `NEW`
  - replaces it with a pretime-decorated `NEW` if the lookup arrives in time

## Reload

The sitebot supports live reload of:

- channels
- encryption keys
- themes
- section routing
- plugin config

without dropping the IRC connection.
