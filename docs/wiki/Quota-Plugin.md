# Quota Plugin

The `Quota` sitebot plugin tracks trial and quota users from GoFTPd user files
and lets staff manage them from IRC.

It is inspired by the old fin-bot workflow, but without the old `!df` / free
space command set.

## What It Does

- Reads GoFTPd user files from `etc/users`
- Tracks users in one of three states:
  - `trial`
  - `quota`
  - `disabled`
- Checks weekly upload totals from the `WKUP` line in each user file
- Moves trial users to quota when they pass
- Disables users who fail trial/quota, using flag `6`
- Writes a `.bye` file when a user is disabled
- Can kick the linked IRC nick from configured channels after disable

The plugin keeps its own small YAML state file so it can remember:

- when a trial started
- how many days are left
- the last IRC nick seen for a user
- whether a user is currently tracked as trial/quota/disabled

## Commands

### Public

`!quota`

Shows two blocks:

- weekly quota users
- current trial users

Each line includes:

- username
- group
- current weekly uploaded amount
- `PASSING` or `FAILING`
- days left for trial users

### Staff

`!quotactl trial <user> [days]`

- sets the user to trial mode
- clears the disabled flag if present
- resets the trial start time

`!quotactl quota <user>`

- sets the user to quota mode
- clears the disabled flag if present

`!quotactl extend <user> <days>`

- resets the user's trial timer to the given number of days

`!quotactl delete <user> [reason]`

- disables the user with flag `6`
- writes a `.bye` file
- optionally kicks the linked IRC nick from configured channels

## IRC Nick Mapping

The plugin learns FTP user to IRC nick mappings from `SITE INVITE`.

That means if a user uses `SITE INVITE <nick>`, the sitebot can remember:

- FTP login: `SomeUser`
- IRC nick: `SomeNick`

Later, if that FTP user fails trial/quota and gets disabled, the plugin can
kick `SomeNick` from configured IRC channels.

## Config

The shipped example lives at:

- [C:\Users\nelis\Documents\GoFTPd\sitebot\plugins\quota\config.yml.dist](C:\Users\nelis\Documents\GoFTPd\sitebot\plugins\quota\config.yml.dist)

Example:

```yml
users_dir: "../etc/users"
bye_dir: "../etc/byefiles"
state_file: "plugins/quota/state.yml"

status_commands: ["quota"]
staff_command: "quotactl"

reply_target: "channel"
channels: ["#goftpd-chat"]
staff_channels: ["#goftpd-staff"]
staff_hosts: []
staff_users: []

included_groups: []
skip_users: ["default.user", "glftpd", "goftpd", "NUKEBOT"]

scan_interval_seconds: 300

trial_enabled: true
trial_days_default: 7
trial_quota_gb: 150

quota_enabled: true
quota_gb: 250
quota_fail_back_to_trial: true

disabled_flag: "6"

kick_on_disable: true
kick_channels: []
```

## Main Sitebot Config

Enable it in:

- [C:\Users\nelis\Documents\GoFTPd\sitebot\etc\config.yml.example](C:\Users\nelis\Documents\GoFTPd\sitebot\etc\config.yml.example)

Example:

```yml
plugins:
  enabled:
    Quota: true
  config:
    quota:
      config_file: "plugins/quota/config.yml"
```

## Disabled User Behavior

The plugin uses flag `6` as the disabled marker.

GoFTPd now treats that as a real disabled account and rejects login with:

- `530 Account disabled.`

So the quota plugin and the daemon now agree on what "disabled" means.

## Notes

- `!quota` is intentionally separate from the existing `!top` plugin
- If you want old-style `!top` behavior from this plugin, you can set:

```yml
status_commands: ["top"]
```

but then you should disable the built-in `Top` plugin to avoid double replies.

- `!quotactl delete` disables the user; it does not remove the user file
- The plugin is sitebot-side only right now; there are no FTP `SITE QUOTA`
  commands yet

## Good First Test

1. `!quota`
2. `!quotactl trial testuser 7`
3. `!quotactl extend testuser 10`
4. `!quotactl quota testuser`
5. `!quotactl delete testuser test reason`

That is enough to verify:

- reply formatting
- state persistence
- flag `6` handling
- `.bye` file creation
- IRC-side management flow
