# Gotify VPS Agent

Monitor a Debian 13 VPS and receive health alerts in Gotify. The agent runs through a systemd timer, checks the host approximately every five minutes by default, and groups detected problems into notifications.

Unofficial community project. Not affiliated with or endorsed by Gotify.

## Features

- Failed systemd units and supported services: SSH, Nginx, PHP-FPM, MariaDB/MySQL, PostgreSQL and Tor.
- Disk space, inodes and read-only filesystems.
- Available memory, swap, CPU load and new OOM events.
- Kernel and block-device errors.
- APT, DPKG, unattended-upgrades, reboot requirements and running kernel checks.
- Time synchronization, agent scheduling and notification delivery health.
- Deduplicated alerts, optional recovery notifications and configurable reminders.
- Direct Gotify delivery or a local SOCKS5/SOCKS5H proxy, including Tor onion services.

The agent monitors and reports. Package updates, service restarts and scheduled reboots remain the responsibility of your system configuration. Service checks inspect systemd status; use an external uptime monitor to detect a completely unavailable VPS.

## Install

Requirements: Debian 13 with systemd, an AMD64 or ARM64 CPU, root access and a Gotify **application token**.

Install the recommended packages:

```bash
sudo apt update
sudo apt install curl needrestart unattended-upgrades
```

Download and review the installer, then run it:

```bash
curl -fL -o install.sh https://github.com/h0ek/gotify-vps-agent/releases/latest/download/install.sh
less install.sh
sudo bash install.sh
```

Setup verifies the downloaded binary's checksum, asks for your Gotify URL and token, detects supported services, and offers to enable monitoring. It sends a test notification and offers to save an initial baseline without health alerts.

Check the installation:

```bash
sudo gotify-vps-agent doctor
sudo gotify-vps-agent status
```

## Commands

| Command | Purpose |
| --- | --- |
| `sudo gotify-vps-agent configure` | Open interactive configuration. |
| `sudo gotify-vps-agent status` | Show saved check results and pending notifications. |
| `sudo gotify-vps-agent doctor` | Diagnose configuration, dependencies and connectivity. |
| `sudo gotify-vps-agent check` | Run checks now and send notifications when needed. |
| `sudo gotify-vps-agent check --dry-run` | Show results without sending notifications or changing state. |
| `sudo gotify-vps-agent test-notification` | Send one test message to Gotify. |
| `sudo gotify-vps-agent services` | List enabled service checks. |
| `sudo gotify-vps-agent services detect` | Show supported services detected on this host. |
| `sudo gotify-vps-agent services enable nginx` | Enable a service check; replace `nginx` with a detected service ID. |
| `sudo gotify-vps-agent services disable nginx` | Disable a service check. |
| `sudo gotify-vps-agent proxy status` | Show the Gotify URL and proxy mode. |
| `sudo gotify-vps-agent proxy enable` | Configure and test delivery through a local SOCKS5 proxy. |
| `sudo gotify-vps-agent proxy disable` | Configure and test direct delivery. |
| `sudo gotify-vps-agent timer sync` | Apply the configured check interval to systemd. |
| `gotify-vps-agent version` | Show the installed version. |
| `gotify-vps-agent help` | Show command syntax and additional options. |

View recent agent logs with:

```bash
sudo journalctl -u gotify-vps-agent.service -n 50 --no-pager
```

## Configuration and notifications

| Location | Contents |
| --- | --- |
| `/etc/gotify-vps-agent/config.toml` | Gotify URL, thresholds, enabled checks and notification settings. |
| `/etc/gotify-vps-agent/gotify.token` | Application token; keep this file private. |
| `/var/lib/gotify-vps-agent/` | Incident state and pending notifications; keep it when upgrading. |

By default, an incident sends one problem alert, at most one escalation from WARNING to CRITICAL, and one recovery notification. Periodic reminders are disabled. Recovery requires two successful checks; a later failure is a new incident. New OOM and kernel I/O events can notify separately.

For problem and escalation alerts only, set these values in the **existing** `[agent]` section of `config.toml`:

```toml
warning_reminder_hours = 0
critical_reminder_hours = 0
notify_recovery = false
```

A positive reminder interval enables reminders for that severity. Upgrades preserve existing settings. Changes are read on the next check; after editing `interval` manually, also run `sudo gotify-vps-agent timer sync`.

Deleting a message in Gotify does not reset the agent's incident state. Successful routine checks, package upgrades and system startup do not send informational messages.

If Gotify is unavailable, queued messages are retried. The queue holds up to 100 events and 1 MiB, discarding the oldest events when full. Emergency alerts about state storage failures may repeat until storage works again.

## SOCKS5 and Tor

Run `sudo gotify-vps-agent proxy enable` and enter your Gotify URL and local proxy, usually `socks5h://127.0.0.1:9050` for Tor. Onion URLs require proxy delivery. Only loopback proxies are supported.

Proxy changes send a test message before saving; a failed test preserves the previous configuration. Proxy delivery never falls back to a direct connection. Use an HTTPS URL when switching back to direct delivery.

## Upgrade

Download a fresh installer and upgrade the existing installation:

```bash
curl -fL -o install.sh https://github.com/h0ek/gotify-vps-agent/releases/latest/download/install.sh &&
sudo bash install.sh --upgrade &&
sudo gotify-vps-agent doctor
```

Configuration, token, incident state and pending notifications are preserved. The installer restarts the timer if it was running before the upgrade.

## Uninstall

Remove the agent and its systemd units while keeping configuration and state:

```bash
sudo /usr/local/lib/gotify-vps-agent/uninstall.sh
```

To also delete configuration, the token, state and timer overrides:

```bash
sudo /usr/local/lib/gotify-vps-agent/uninstall.sh --purge
```

Licensed under [MIT](LICENSE). See [SECURITY.md](SECURITY.md) for vulnerability reporting.
