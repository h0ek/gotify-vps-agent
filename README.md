# Gotify VPS Agent

Gotify VPS Agent monitors essential Debian 13 VPS health conditions and sends deduplicated alerts and recovery notifications to Gotify.

Unofficial community project. Not affiliated with or endorsed by the Gotify project.

## Features

- failed systemd units
- disk usage, inode usage and read-only filesystems
- available memory, swap usage, OOM events and CPU load
- kernel and block-device errors
- APT, DPKG and unattended-upgrades status
- reboot requirement and running kernel checks
- time synchronization
- agent timer and execution freshness
- bounded notification queue with deduplication and backoff
- service checks for SSH, Nginx, PHP-FPM, MariaDB/MySQL, PostgreSQL and Tor
- aggregated warnings, critical alerts, reminders and recovery notifications

The agent does not open network ports, accept inbound connections, load plugins or perform automatic remediation. It only sends outbound HTTP(S) requests to the configured Gotify server.

## Requirements

- Debian 13
- systemd
- root privileges
- Gotify application token

Recommended packages:

```bash
sudo apt update
sudo apt install curl needrestart unattended-upgrades
```

## Install

Download and review the installer:

```bash
curl -fLO https://github.com/h0ek/gotify-vps-agent/releases/latest/download/install.sh
less install.sh
sudo bash install.sh
```

The installer downloads the correct binary, verifies its SHA-256 checksum, configures Gotify, detects supported services, creates an initial baseline and installs the systemd service and timer.

## Commands

```text
gotify-vps-agent configure
gotify-vps-agent check
gotify-vps-agent check --dry-run
gotify-vps-agent status
gotify-vps-agent doctor
gotify-vps-agent test-notification
gotify-vps-agent services
gotify-vps-agent services detect
gotify-vps-agent services enable nginx
gotify-vps-agent services disable nginx
gotify-vps-agent timer sync
gotify-vps-agent reset-state --yes
gotify-vps-agent version
```

Commands that read or modify protected state require root privileges.

## Upgrade

Download the current installer and run:

```bash
sudo bash install.sh --upgrade
```

The upgrade preserves the configuration, application token, state, journal cursor, notification queue and timer state.

## Build from source

```bash
make fmt-check
make shellcheck
make vet
make test
make test-race
make security
make build VERSION=0.1.0
```

Install the local build:

```bash
sudo ./scripts/install.sh --local ./dist/gotify-vps-agent
```

## Files

```text
/usr/local/bin/gotify-vps-agent
/usr/local/lib/gotify-vps-agent/uninstall.sh
/etc/gotify-vps-agent/config.toml
/etc/gotify-vps-agent/gotify.token
/var/lib/gotify-vps-agent/state.json
/var/lib/gotify-vps-agent/queue.json
/var/lib/gotify-vps-agent/journal.cursor
```

The application token is stored separately with mode `0600`. It is not placed in command arguments, configuration output or journal messages.

## Uninstall

Keep configuration and state:

```bash
sudo /usr/local/lib/gotify-vps-agent/uninstall.sh
```

Remove the application, configuration, token and state:

```bash
sudo /usr/local/lib/gotify-vps-agent/uninstall.sh --purge
```

## Security

The agent uses fixed command paths and argument structures, command timeouts, bounded output, strict input parsing, atomically written root-owned files, TLS certificate verification and a hardened systemd service.

See [SECURITY.md](SECURITY.md) for vulnerability reporting.

## License

MIT
