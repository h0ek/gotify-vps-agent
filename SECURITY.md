# Security Policy

## Supported versions

Only the latest published release is supported.

## Reporting a vulnerability

Use GitHub private vulnerability reporting. Do not open a public issue for a suspected vulnerability.

Include the affected version, reproduction steps, expected impact and suggested remediation when available. Never include real Gotify tokens, private hostnames, IP addresses or production logs.

## Security scope

The agent opens no listening socket and accepts no inbound network requests. It executes only fixed local commands with fixed argument structures and sends outbound requests only to the administrator-configured Gotify URL.

The application token grants message-push access to the configured Gotify application. Treat it as a secret and rotate it after suspected disclosure.
