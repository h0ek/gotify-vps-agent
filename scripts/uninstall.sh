#!/usr/bin/env bash
set -Eeuo pipefail

PROGRAM="gotify-vps-agent"
STATE_DIR="/var/lib/${PROGRAM}"
CONFIG_DIR="/etc/${PROGRAM}"
MANIFEST="${STATE_DIR}/install-manifest"
PURGE=0
ASSUME_YES=0

usage() {
  cat <<USAGE
Usage: uninstall.sh [--purge] [--yes]

  --purge  Remove configuration, token, state and timer override
  --yes    Do not ask for confirmation
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --purge) PURGE=1 ;;
  --yes) ASSUME_YES=1 ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "Unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
  esac
  shift
done

if [[ ${EUID} -ne 0 ]]; then
  echo "Run this script as root." >&2
  exit 1
fi

if [[ ${ASSUME_YES} -ne 1 ]]; then
  read -r -p "Remove Gotify VPS Agent? [y/N]: " answer
  case "${answer}" in
  y | Y | yes | YES) ;;
  *)
    echo "Cancelled."
    exit 0
    ;;
  esac
fi

systemctl disable --now "${PROGRAM}.timer" >/dev/null 2>&1 || true
systemctl stop "${PROGRAM}.service" >/dev/null 2>&1 || true

paths=()
if [[ -r "${MANIFEST}" ]]; then
  while IFS= read -r path; do
    [[ -n "${path}" ]] && paths+=("${path}")
  done <"${MANIFEST}"
else
  paths=(
    "/usr/local/bin/${PROGRAM}"
    "/usr/local/lib/${PROGRAM}/uninstall.sh"
    "/etc/systemd/system/${PROGRAM}.service"
    "/etc/systemd/system/${PROGRAM}.timer"
  )
fi

for path in "${paths[@]}"; do
  case "${path}" in
  /usr/local/bin/gotify-vps-agent | /usr/local/lib/gotify-vps-agent/uninstall.sh | /etc/systemd/system/gotify-vps-agent.service | /etc/systemd/system/gotify-vps-agent.timer)
    rm -f -- "${path}"
    ;;
  *)
    echo "Skipping unexpected manifest path: ${path}" >&2
    ;;
  esac
done

rm -rf -- "/usr/local/lib/${PROGRAM}"

if [[ ${PURGE} -eq 1 ]]; then
  rm -rf -- "/etc/systemd/system/${PROGRAM}.timer.d"
  rm -rf -- "${CONFIG_DIR}" "${STATE_DIR}"
  echo "Configuration, token and state removed."
else
  rm -f -- "${MANIFEST}"
  echo "Configuration and state were preserved in ${CONFIG_DIR} and ${STATE_DIR}."
fi

systemctl daemon-reload
systemctl reset-failed >/dev/null 2>&1 || true

echo "Gotify VPS Agent removed."
