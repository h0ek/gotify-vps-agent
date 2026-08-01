#!/usr/bin/env bash
set -Eeuo pipefail

PROGRAM="gotify-vps-agent"
VERSION="${1:-}"
DIST="${DIST_DIR:-dist}"
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}"

if [[ -z "${VERSION}" || "${VERSION}" == "dev" ]]; then
  echo "Usage: package-release.sh VERSION" >&2
  exit 2
fi
VERSION="${VERSION#v}"
if [[ ! "${VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Invalid release version: ${VERSION}" >&2
  exit 2
fi
if [[ ! "${SOURCE_DATE_EPOCH}" =~ ^[0-9]+$ ]]; then
  echo "Invalid SOURCE_DATE_EPOCH: ${SOURCE_DATE_EPOCH}" >&2
  exit 2
fi

for arch in amd64 arm64; do
  binary="${DIST}/${PROGRAM}_linux_${arch}"
  [[ -x "${binary}" ]] || {
    echo "Missing ${binary}" >&2
    exit 1
  }
  stage="${DIST}/package-${arch}"
  archive="${DIST}/${PROGRAM}_${VERSION}_linux_${arch}.tar.gz"
  rm -rf -- "${stage}"
  mkdir -p "${stage}"
  install -m 0755 "${binary}" "${stage}/${PROGRAM}"
  install -m 0644 README.md LICENSE "${stage}/"
  tar --sort=name --mtime="@${SOURCE_DATE_EPOCH}" --owner=0 --group=0 --numeric-owner -C "${stage}" -cf - . | gzip -n >"${archive}"
  rm -rf -- "${stage}"
done

cp scripts/install.sh "${DIST}/install.sh"
chmod 0755 "${DIST}/install.sh"
(
  cd "${DIST}"
  sha256sum \
    "${PROGRAM}_${VERSION}_linux_amd64.tar.gz" \
    "${PROGRAM}_${VERSION}_linux_arm64.tar.gz" \
    install.sh >checksums.txt
)
