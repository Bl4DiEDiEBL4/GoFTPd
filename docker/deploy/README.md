# Docker Deploy Pack

This directory is the easy path for running WeaveFTPd with Docker on real Linux
servers. Build the images locally on each machine, then run the deploy compose
files from those local images.

There are two normal layouts:

- master machine: master daemon + sitebot, and optionally a local slave
- slave machine: one remote slave daemon

The sitebot should run on the master machine. It reads daemon events from a
local FIFO. Separate containers are fine, but both containers must mount the
same host directory for `/app/etc`.

Remote slaves do not need the FIFO and do not need the sitebot.

## Master Machine

Initialize runtime files:

```bash
cd docker/deploy
./init.sh master
```

Edit:

```text
runtime/master/etc/config.yml
runtime/master/sitebot/etc/config.yml
master.env
```

Start master + sitebot:

```bash
docker build --target daemon -t weaveftpd:latest ../..
docker build --target sitebot -t weaveftpd-sitebot:latest ../..
docker compose --env-file master.env -f master-sitebot.compose.yml up -d master sitebot
docker compose --env-file master.env -f master-sitebot.compose.yml logs -f master sitebot
```

If the same machine should also be a storage slave:

```bash
docker compose --env-file master.env -f master-sitebot.compose.yml --profile local-slave up -d
docker compose --env-file master.env -f master-sitebot.compose.yml logs -f master sitebot local-slave
```

The local slave config is:

```text
runtime/master/etc/config-slave-local.yml
```

It connects to `127.0.0.1:1099` because all containers use host networking.

## Remote Slave Machine

Initialize runtime files:

```bash
cd docker/deploy
MASTER_HOST="203.0.113.10" SLAVE_NAME="SLAVE1" ./init.sh slave
```

Edit:

```text
runtime/slave/etc/config.yml
slave.env
```

Start the slave:

```bash
docker build --target daemon -t weaveftpd:latest ../..
docker compose --env-file slave.env -f slave.compose.yml up -d slave
docker compose --env-file slave.env -f slave.compose.yml logs -f slave
```

## FIFO And Sitebot

The master daemon writes JSON events to:

```text
/app/etc/weaveftpd.sitebot.fifo
```

The sitebot reads the same path. This works because both services mount:

```text
runtime/master/etc:/app/etc
```

Do not put the sitebot on a different machine unless you also add a network
event transport. A POSIX FIFO is local filesystem IPC.

## Updating

Rebuild the local image and restart:

```bash
docker build --target daemon -t weaveftpd:latest ../..
docker build --target sitebot -t weaveftpd-sitebot:latest ../..
docker compose --env-file master.env -f master-sitebot.compose.yml up -d
```

For a remote slave:

```bash
docker build --target daemon -t weaveftpd:latest ../..
docker compose --env-file slave.env -f slave.compose.yml up -d
```

Runtime data stays in `runtime/`; images only contain binaries.
