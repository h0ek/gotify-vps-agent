#!/usr/bin/env bash
set -Eeuo pipefail

PROGRAM="gotify-vps-agent"
REPOSITORY="${GVA_REPOSITORY:-h0ek/gotify-vps-agent}"
PREFIX="/usr/local"
BINARY_PATH="${PREFIX}/bin/${PROGRAM}"
LIB_DIR="${PREFIX}/lib/${PROGRAM}"
CONFIG_DIR="/etc/${PROGRAM}"
STATE_DIR="/var/lib/${PROGRAM}"
SYSTEMD_DIR="/etc/systemd/system"
SERVICE_PATH="${SYSTEMD_DIR}/${PROGRAM}.service"
TIMER_PATH="${SYSTEMD_DIR}/${PROGRAM}.timer"
TIMER_OVERRIDE_PATH="${SYSTEMD_DIR}/${PROGRAM}.timer.d/override.conf"
UNINSTALL_PATH="${LIB_DIR}/uninstall.sh"
MANIFEST_PATH="${STATE_DIR}/install-manifest"
VERSION=""
LOCAL_BINARY=""
UPGRADE=0
NO_CONFIG=0
TEMP_DIR=""
TIMER_WAS_ENABLED=0
INSTALL_COMMITTED=0
SCRIPT_DIR="$(cd -- "$(dirname -- "$(readlink -f -- "$0")")" && pwd -P)"
BACKUP_NAMES=(binary service timer timer-override uninstall manifest)
BACKUP_TARGETS=("${BINARY_PATH}" "${SERVICE_PATH}" "${TIMER_PATH}" "${TIMER_OVERRIDE_PATH}" "${UNINSTALL_PATH}" "${MANIFEST_PATH}")

atomic_install() {
  local source="$1"
  local target="$2"
  local mode="$3"
  local directory temporary
  directory="$(dirname -- "${target}")"
  if [[ ! -d "${directory}" ]]; then
    install -d -o root -g root -m 0755 "${directory}"
  fi
  temporary="$(mktemp "${directory}/.${PROGRAM}.install.XXXXXX")"
  install -o root -g root -m "${mode}" "${source}" "${temporary}"
  mv -fT -- "${temporary}" "${target}"
}

backup_target() {
  local name="$1"
  local target="$2"
  if [[ -L "${target}" ]]; then
    echo "Refusing to replace symbolic link: ${target}" >&2
    exit 1
  fi
  if [[ -e "${target}" ]]; then
    [[ -f "${target}" ]] || {
      echo "Refusing to replace non-regular file: ${target}" >&2
      exit 1
    }
    cp --preserve=mode,timestamps -- "${target}" "${TEMP_DIR}/backup-${name}"
  else
    : >"${TEMP_DIR}/no-original-${name}"
  fi
}

restore_target() {
  local name="$1"
  local target="$2"
  local backup="${TEMP_DIR}/backup-${name}"
  if [[ -f "${backup}" ]]; then
    atomic_install "${backup}" "${target}" "$(stat -c '%a' "${backup}")" || true
  elif [[ -f "${TEMP_DIR}/no-original-${name}" ]]; then
    rm -f -- "${target}" || true
  fi
}

cleanup() {
  local status=$?
  if [[ ${status} -ne 0 && ${INSTALL_COMMITTED} -eq 0 && -n "${TEMP_DIR}" && -d "${TEMP_DIR}" ]]; then
    local index
    for index in "${!BACKUP_NAMES[@]}"; do
      restore_target "${BACKUP_NAMES[${index}]}" "${BACKUP_TARGETS[${index}]}"
    done
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [[ ${TIMER_WAS_ENABLED} -eq 1 ]]; then
      systemctl start "${PROGRAM}.timer" >/dev/null 2>&1 || true
    fi
  fi
  if [[ -n "${TEMP_DIR}" && -d "${TEMP_DIR}" ]]; then
    rm -rf -- "${TEMP_DIR}"
  fi
  exit "${status}"
}
trap cleanup EXIT

usage() {
  cat <<USAGE
Usage: install.sh [options]

Options:
  --local PATH     Install a locally built binary
  --version VER    Install a specific stable release
  --upgrade        Upgrade an existing installation in place
  --no-config      Install files without running configure
  -h, --help       Show this help

Environment:
  GVA_REPOSITORY   GitHub owner/repository, default: ${REPOSITORY}
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
  --local)
    [[ $# -ge 2 ]] || {
      echo "--local requires a path" >&2
      exit 2
    }
    LOCAL_BINARY="$2"
    shift
    ;;
  --version)
    [[ $# -ge 2 ]] || {
      echo "--version requires a value" >&2
      exit 2
    }
    VERSION="${2#v}"
    shift
    ;;
  --upgrade) UPGRADE=1 ;;
  --no-config) NO_CONFIG=1 ;;
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

[[ ${EUID} -eq 0 ]] || {
  echo "Run this script as root." >&2
  exit 1
}
[[ ${UPGRADE} -eq 0 || ${NO_CONFIG} -eq 0 ]] || {
  echo "--upgrade and --no-config cannot be combined." >&2
  exit 2
}
[[ -z "${LOCAL_BINARY}" || -z "${VERSION}" ]] || {
  echo "--local and --version cannot be combined." >&2
  exit 2
}
[[ -r /etc/os-release ]] || {
  echo "Unable to read /etc/os-release." >&2
  exit 1
}

OS_ID="$(sed -n 's/^ID=//p' /etc/os-release | head -n1 | tr -d '"')"
OS_VERSION_ID="$(sed -n 's/^VERSION_ID=//p' /etc/os-release | head -n1 | tr -d '"')"
if [[ "${OS_ID}" != "debian" || "${OS_VERSION_ID}" != "13" ]]; then
  echo "Debian 13 is required; detected ID=${OS_ID:-unknown} VERSION_ID=${OS_VERSION_ID:-unknown}." >&2
  exit 1
fi

if [[ ! -d /run/systemd/system ]] || ! command -v systemctl >/dev/null 2>&1; then
  echo "A running systemd installation is required." >&2
  exit 1
fi

for command in install mktemp realpath readlink sed head tr stat uname cp mv rm dirname; do
  command -v "${command}" >/dev/null 2>&1 || {
    echo "Required command not found: ${command}" >&2
    exit 1
  }
done

[[ "${REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || {
  echo "Invalid GVA_REPOSITORY value: ${REPOSITORY}" >&2
  exit 1
}

case "$(uname -m)" in
x86_64) ARCH="amd64" ;;
aarch64 | arm64) ARCH="arm64" ;;
*)
  echo "Unsupported architecture: $(uname -m). Supported: amd64, arm64." >&2
  exit 1
  ;;
esac

if [[ -x "${BINARY_PATH}" ]]; then
  [[ ${UPGRADE} -eq 1 ]] || {
    echo "An existing installation was detected. Use --upgrade, or run ${PROGRAM} configure to reconfigure it." >&2
    exit 1
  }
else
  [[ ${UPGRADE} -eq 0 ]] || {
    echo "--upgrade requires an existing installation at ${BINARY_PATH}." >&2
    exit 1
  }
fi

TEMP_DIR="$(mktemp -d)"
SOURCE_BINARY=""

if [[ -n "${LOCAL_BINARY}" ]]; then
  [[ -f "${LOCAL_BINARY}" && ! -L "${LOCAL_BINARY}" && -x "${LOCAL_BINARY}" ]] || {
    echo "Local binary must be a regular executable file: ${LOCAL_BINARY}" >&2
    exit 1
  }
  SOURCE_BINARY="$(realpath --canonicalize-existing "${LOCAL_BINARY}")"
else
  for command in sha256sum tar curl grep; do
    command -v "${command}" >/dev/null 2>&1 || {
      echo "Required command not found: ${command}" >&2
      exit 1
    }
  done
  CURL_OPTIONS=(--fail --location --silent --show-error --retry 3 --retry-all-errors --connect-timeout 10 --max-time 120 --max-redirs 5 --proto '=https' --proto-redir '=https' --tlsv1.2)
  if [[ -z "${VERSION}" ]]; then
    effective_url="$(curl "${CURL_OPTIONS[@]}" --head -o /dev/null -w '%{url_effective}' "https://github.com/${REPOSITORY}/releases/latest")"
    tag="${effective_url##*/}"
    [[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
      echo "Unable to determine the latest stable release tag." >&2
      exit 1
    }
    VERSION="${tag#v}"
  fi
  [[ "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    echo "Invalid release version: ${VERSION}" >&2
    exit 1
  }

  archive="${PROGRAM}_${VERSION}_linux_${ARCH}.tar.gz"
  base_url="https://github.com/${REPOSITORY}/releases/download/v${VERSION}"
  curl "${CURL_OPTIONS[@]}" -o "${TEMP_DIR}/${archive}" "${base_url}/${archive}"
  curl "${CURL_OPTIONS[@]}" -o "${TEMP_DIR}/checksums.txt" "${base_url}/checksums.txt"

  expected_line="$(grep -E "^[0-9a-fA-F]{64}[[:space:]]+\*?${archive//./\\.}$" "${TEMP_DIR}/checksums.txt" || true)"
  [[ -n "${expected_line}" ]] || {
    echo "Checksum for ${archive} is missing." >&2
    exit 1
  }
  printf '%s\n' "${expected_line}" >"${TEMP_DIR}/selected-checksum.txt"
  (cd "${TEMP_DIR}" && sha256sum -c selected-checksum.txt)

  mapfile -t archive_entries < <(tar -tzf "${TEMP_DIR}/${archive}")
  [[ ${#archive_entries[@]} -eq 4 ]] || {
    echo "Release archive contains an unexpected number of entries." >&2
    exit 1
  }
  binary_found=0
  readme_found=0
  license_found=0
  directory_found=0
  for entry in "${archive_entries[@]}"; do
    case "${entry}" in
    ./) directory_found=$((directory_found + 1)) ;;
    ./gotify-vps-agent) binary_found=$((binary_found + 1)) ;;
    ./README.md) readme_found=$((readme_found + 1)) ;;
    ./LICENSE) license_found=$((license_found + 1)) ;;
    *)
      echo "Release archive contains an unexpected path: ${entry}" >&2
      exit 1
      ;;
    esac
  done
  [[ ${directory_found} -eq 1 && ${binary_found} -eq 1 && ${readme_found} -eq 1 && ${license_found} -eq 1 ]] || {
    echo "Release archive layout is invalid." >&2
    exit 1
  }

  regular_entries=0
  directory_entries=0
  while IFS= read -r detail; do
    case "${detail:0:1}" in
    -) regular_entries=$((regular_entries + 1)) ;;
    d) directory_entries=$((directory_entries + 1)) ;;
    *)
      echo "Release archive contains a link or special file." >&2
      exit 1
      ;;
    esac
  done < <(tar -tvzf "${TEMP_DIR}/${archive}")
  [[ ${regular_entries} -eq 3 && ${directory_entries} -eq 1 ]] || {
    echo "Release archive file types are invalid." >&2
    exit 1
  }

  mkdir -p "${TEMP_DIR}/archive"
  tar -xzf "${TEMP_DIR}/${archive}" --no-same-owner --no-same-permissions --no-overwrite-dir --delay-directory-restore -C "${TEMP_DIR}/archive"
  SOURCE_BINARY="${TEMP_DIR}/archive/${PROGRAM}"
  [[ -f "${SOURCE_BINARY}" && ! -L "${SOURCE_BINARY}" ]] || {
    echo "Release archive contains an invalid binary." >&2
    exit 1
  }
  chmod 0755 "${SOURCE_BINARY}"
fi

source_version="$("${SOURCE_BINARY}" version 2>/dev/null || true)"
[[ "${source_version}" =~ ^gotify-vps-agent[[:space:]] ]] || {
  echo "The selected file is not a runnable ${PROGRAM} binary for this host." >&2
  exit 1
}
if [[ -z "${LOCAL_BINARY}" && "${source_version}" != gotify-vps-agent\ ${VERSION}\ * ]]; then
  echo "Downloaded binary version does not match release ${VERSION}: ${source_version}" >&2
  exit 1
fi

if systemctl is-enabled --quiet "${PROGRAM}.timer" 2>/dev/null; then
  TIMER_WAS_ENABLED=1
fi
systemctl stop "${PROGRAM}.timer" >/dev/null 2>&1 || true
systemctl stop "${PROGRAM}.service" >/dev/null 2>&1 || true

install -d -o root -g root -m 0755 "${PREFIX}/bin" "${LIB_DIR}" "${CONFIG_DIR}" "${SYSTEMD_DIR}"
install -d -o root -g root -m 0700 "${STATE_DIR}"

for index in "${!BACKUP_NAMES[@]}"; do
  backup_target "${BACKUP_NAMES[${index}]}" "${BACKUP_TARGETS[${index}]}"
done

atomic_install "${SOURCE_BINARY}" "${BINARY_PATH}" 0755

SERVICE_SOURCE="${SCRIPT_DIR}/../packaging/systemd/${PROGRAM}.service"
if [[ -f "${SERVICE_SOURCE}" ]]; then
  atomic_install "${SERVICE_SOURCE}" "${SERVICE_PATH}" 0644
else
  cat >"${TEMP_DIR}/${PROGRAM}.service" <<'UNIT'
[Unit]
Description=Gotify VPS Agent health check
Documentation=https://github.com/h0ek/gotify-vps-agent
Wants=network-online.target
After=network-online.target
ConditionPathExists=/etc/gotify-vps-agent/config.toml
ConditionPathExists=/etc/gotify-vps-agent/gotify.token

[Service]
Type=oneshot
ExecStart=/usr/local/bin/gotify-vps-agent check
User=root
Group=root
UMask=0077
TimeoutStartSec=2min
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/gotify-vps-agent
ProtectHostname=true
ProtectClock=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ProtectKernelLogs=true
KeyringMode=private
RemoveIPC=true
RestrictNamespaces=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RestrictRealtime=true
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT
  atomic_install "${TEMP_DIR}/${PROGRAM}.service" "${SERVICE_PATH}" 0644
fi

TIMER_SOURCE="${SCRIPT_DIR}/../packaging/systemd/${PROGRAM}.timer"
if [[ -f "${TIMER_SOURCE}" ]]; then
  atomic_install "${TIMER_SOURCE}" "${TIMER_PATH}" 0644
else
  cat >"${TEMP_DIR}/${PROGRAM}.timer" <<'UNIT'
[Unit]
Description=Run Gotify VPS Agent periodically
Documentation=https://github.com/h0ek/gotify-vps-agent

[Timer]
OnActiveSec=2m
OnUnitInactiveSec=5m
AccuracySec=30s
RandomizedDelaySec=30s
Unit=gotify-vps-agent.service

[Install]
WantedBy=timers.target
UNIT
  atomic_install "${TEMP_DIR}/${PROGRAM}.timer" "${TIMER_PATH}" 0644
fi

UNINSTALL_SOURCE="${SCRIPT_DIR}/uninstall.sh"
if [[ -f "${UNINSTALL_SOURCE}" ]]; then
  atomic_install "${UNINSTALL_SOURCE}" "${UNINSTALL_PATH}" 0750
else
  cat >"${TEMP_DIR}/uninstall.sh" <<'UNINSTALL'
#!/usr/bin/env bash
set -Eeuo pipefail
PROGRAM="gotify-vps-agent"
STATE_DIR="/var/lib/${PROGRAM}"
CONFIG_DIR="/etc/${PROGRAM}"
MANIFEST="${STATE_DIR}/install-manifest"
PURGE=0
ASSUME_YES=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --purge) PURGE=1 ;;
    --yes) ASSUME_YES=1 ;;
    -h|--help) echo "Usage: uninstall.sh [--purge] [--yes]"; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done
[[ ${EUID} -eq 0 ]] || { echo "Run this script as root." >&2; exit 1; }
if [[ ${ASSUME_YES} -ne 1 ]]; then
  read -r -p "Remove Gotify VPS Agent? [y/N]: " answer
  case "${answer}" in y|Y|yes|YES) ;; *) echo "Cancelled."; exit 0 ;; esac
fi
systemctl disable --now "${PROGRAM}.timer" >/dev/null 2>&1 || true
systemctl stop "${PROGRAM}.service" >/dev/null 2>&1 || true
paths=()
if [[ -r "${MANIFEST}" ]]; then
  while IFS= read -r path; do [[ -n "${path}" ]] && paths+=("${path}"); done < "${MANIFEST}"
else
  paths=("/usr/local/bin/${PROGRAM}" "/usr/local/lib/${PROGRAM}/uninstall.sh" "/etc/systemd/system/${PROGRAM}.service" "/etc/systemd/system/${PROGRAM}.timer")
fi
for path in "${paths[@]}"; do
  case "${path}" in
    /usr/local/bin/gotify-vps-agent|/usr/local/lib/gotify-vps-agent/uninstall.sh|/etc/systemd/system/gotify-vps-agent.service|/etc/systemd/system/gotify-vps-agent.timer) rm -f -- "${path}" ;;
    *) echo "Skipping unexpected manifest path: ${path}" >&2 ;;
  esac
done
rm -rf -- "/usr/local/lib/${PROGRAM}"
if [[ ${PURGE} -eq 1 ]]; then
  rm -rf -- "/etc/systemd/system/${PROGRAM}.timer.d" "${CONFIG_DIR}" "${STATE_DIR}"
  echo "Configuration, token and state removed."
else
  rm -f -- "${MANIFEST}"
  echo "Configuration and state were preserved in ${CONFIG_DIR} and ${STATE_DIR}."
fi
systemctl daemon-reload
systemctl reset-failed >/dev/null 2>&1 || true
echo "Gotify VPS Agent removed."
UNINSTALL
  atomic_install "${TEMP_DIR}/uninstall.sh" "${UNINSTALL_PATH}" 0750
fi

cat >"${TEMP_DIR}/install-manifest" <<MANIFEST
${BINARY_PATH}
${UNINSTALL_PATH}
${SERVICE_PATH}
${TIMER_PATH}
MANIFEST
atomic_install "${TEMP_DIR}/install-manifest" "${MANIFEST_PATH}" 0600

systemctl daemon-reload
if [[ -f "${CONFIG_DIR}/config.toml" ]]; then
  "${BINARY_PATH}" timer sync >/dev/null
fi
if command -v systemd-analyze >/dev/null 2>&1; then
  systemd-analyze verify "${SERVICE_PATH}" "${TIMER_PATH}" >/dev/null
fi

INSTALL_COMMITTED=1
installed_version="$("${BINARY_PATH}" version 2>/dev/null || true)"
echo "Installed ${installed_version:-${PROGRAM}}."

if [[ ${UPGRADE} -eq 1 ]]; then
  if [[ ${TIMER_WAS_ENABLED} -eq 1 && -f "${CONFIG_DIR}/config.toml" && -f "${CONFIG_DIR}/gotify.token" ]]; then
    systemctl restart "${PROGRAM}.timer"
  fi
  echo "Upgrade complete. Configuration, token, state, journal cursor and notification queue were preserved."
  exit 0
fi

if [[ ${NO_CONFIG} -eq 0 ]]; then
  "${BINARY_PATH}" configure
else
  echo "Installation complete without configuration."
fi
