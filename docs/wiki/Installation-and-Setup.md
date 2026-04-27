# Installation and Setup

## Quick Start

Build and configure the daemon:

```bash
go build -o goftpd ./cmd/goftpd
./setup.sh install
./goftpd -config ./etc/config.yml
```

Build and configure the sitebot:

```bash
cd sitebot
go build -o sitebot ./cmd
cp etc/config.yml.example etc/config.yml
./sitebot -config etc/config.yml
```

## Using `setup.sh`

The installer is the normal way to get a working config without editing every
file by hand.

It can:

- create daemon and sitebot configs
- copy missing plugin `config.yml` files from `.dist` templates
- ask whether to enable daemon and sitebot plugins
- generate TLS certificates
- repair missing plugin blocks when you rerun it later
- update renamed plugin paths such as `slowupkick` -> `slowkick`

## Rerunning Setup

Rerunning:

```bash
./setup.sh install
```

does **not** only help with first install. It also:

- creates newly added plugin configs
- inserts missing plugin blocks into existing configs
- asks enable/disable questions for those missing blocks

## TLS

When TLS is enabled in the daemon config, use:

```yml
tls_enabled: true
require_tls_control: true
require_tls_data: true
tls_exempt_users: []
```

`tls_enabled` only makes TLS available.

`require_tls_control` and `require_tls_data` are what actually force secure
logins and data transfers.

## Cleanup / Fresh Start

To back up generated configs and start over:

```bash
./setup.sh clean
```

This backs up:

- daemon config
- sitebot config
- generated plugin configs
- FIFO path
- generated TLS certificates

## Important Paths

- daemon config: `etc/config.yml`
- sitebot config: `sitebot/etc/config.yml`
- daemon plugin defaults: `plugins/<name>/config.yml.dist`
- sitebot plugin defaults: `sitebot/plugins/<name>/config.yml.dist`
