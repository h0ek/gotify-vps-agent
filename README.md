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
- optional local SOCKS5 or SOCKS5H delivery, including Gotify v3 onion services

The agent does not open network ports, accept inbound connections, load plugins or perform automatic remediation. It only sends outbound requests to the configured Gotify server, directly or through an explicitly configured loopback SOCKS5 proxy.

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

A local SOCKS5 proxy is optional. A standard local Tor daemon commonly exposes SOCKS on:

```text
socks5h://127.0.0.1:9050
```

## Install

Download and review the installer:

```bash
curl -fLO https://github.com/h0ek/gotify-vps-agent/releases/latest/download/install.sh
less install.sh
sudo bash install.sh
```

The installer downloads the correct binary, verifies its SHA-256 checksum, configures Gotify, detects supported services, creates an initial baseline and installs the systemd service and timer.

During a new installation, SOCKS5 delivery is disabled by default. When enabled, the suggested proxy is `socks5h://127.0.0.1:9050`.

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
gotify-vps-agent proxy status
gotify-vps-agent proxy enable
gotify-vps-agent proxy disable
gotify-vps-agent timer sync
gotify-vps-agent reset-state --yes
gotify-vps-agent version
```

Commands that read or modify protected state require root privileges.

## SOCKS5 and onion services

Enable SOCKS5 interactively on an existing installation:

```bash
sudo gotify-vps-agent proxy enable
```

The command asks for the Gotify server URL and the SOCKS5 proxy URL. It sends one test notification before saving. If the test fails, the existing configuration remains unchanged.

Enable it non-interactively:

```bash
sudo gotify-vps-agent proxy enable \
  --server http://exampleexampleexampleexampleexampleexampleexample.onion \
  --proxy socks5h://127.0.0.1:9050
```

Check the current mode:

```bash
sudo gotify-vps-agent proxy status
```

Disable the proxy and optionally switch back to a direct Gotify URL:

```bash
sudo gotify-vps-agent proxy disable \
  --server https://gotify.example.com
```

Only loopback SOCKS5 proxies are accepted. Proxy authentication, remote proxy hosts, paths, query strings and unsupported schemes are rejected. A `.onion` Gotify URL requires SOCKS5 and never falls back to a direct connection.

Plain HTTP remains blocked for non-loopback clearnet targets unless `configure --allow-insecure-http` is used. Plain HTTP is accepted for a valid v3 onion service only when SOCKS5 is enabled.

## Upgrade

Download the current installer and run:

```bash
sudo bash install.sh --upgrade
```

The upgrade preserves the configuration, application token, state, journal cursor, notification queue and timer state.

After upgrading an existing installation to a release with SOCKS5 support:

```bash
sudo gotify-vps-agent proxy enable
sudo gotify-vps-agent proxy status
sudo gotify-vps-agent doctor
```

## Build from source

```bash
make fmt-check
make shellcheck
make vet
make test
make test-race
make security
make build VERSION=0.2.0
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

The optional proxy setting is stored in `/etc/gotify-vps-agent/config.toml`:

```toml
[gotify]
url = "http://exampleexampleexampleexampleexampleexampleexample.onion"
token_file = "/etc/gotify-vps-agent/gotify.token"
timeout = "10s"
allow_insecure_http = false
proxy_url = "socks5h://127.0.0.1:9050"
```

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

SOCKS5 delivery is fail closed. If the local proxy or onion service is unavailable, notifications remain in the bounded queue and are retried later. The agent does not retry the same destination directly.

See [SECURITY.md](SECURITY.md) for vulnerability reporting.

## License

MIT
