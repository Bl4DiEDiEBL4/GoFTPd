# Configuration

## Main Daemon Config

The daemon uses a single main config:

- `etc/config.yml`

Key sections:

- server identity
- mode (`master` or `slave`)
- master block
- slave block
- slave routing
- section directories
- TLS
- zipscript
- plugins

## Master and Slave

GoFTPd uses one config format for both roles.

The active role is selected with:

```yml
mode: master
```

or:

```yml
mode: slave
```

## Section Directories

`sections:` in the main config defines the root and nested virtual directories
that should exist in the VFS and be created on writable slaves.

Example:

```yml
sections:
  - "/0DAY"
  - "/TV-1080P"
  - "/FOREIGN/TV-NL"
  - "/ARCHiVE"
```

Some daemon plugins also contribute directory structure, notably:

- `pre`
- `dateddirs`
- `request`

So plugin config can also create or keep directories alive.

## Plugin Config Split

Daemon plugins are enabled in the main config:

```yml
plugins:
  slowkick:
    enabled: true
    config_file: "plugins/slowkick/config.yml"
```

The `enabled` switch lives in the main config.
The plugin-specific file holds settings only.

## Sitebot Config

The sitebot has its own config:

- `sitebot/etc/config.yml`

This covers:

- IRC connection
- channel routing
- FiSH / DH1080 encryption
- sitebot plugin enable list
- sitebot plugin config file references

## Config Validation

GoFTPd now validates the important configs more strictly:

- `etc/config.yml`
- `etc/permissions.yml`

Malformed YAML should fail cleanly instead of partially loading and producing
weird runtime behavior.

## Practical Advice

- keep plugin config in the plugin file
- keep on/off enable decisions in the main config
- rerun `./setup.sh install` after adding new built-in plugins to older installs
