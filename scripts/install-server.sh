#!/usr/bin/env bash
set -euo pipefail

# Install or safely upgrade the WindRex headless Linux server (and optionally
# windrex-relay). Always verifies SHA-256 checksums before extracting release
# archives and never kills a running session server: if a server is running,
# the script hands sessions over in place with `windrex-server server upgrade`.

REPO="windzu/wind-rex"
VERSION="latest"
MODE=""
WITH_RELAY=0
INSTALL_SYSTEMD=0
NO_UPGRADE=0
DRY_RUN=0
LOCAL_ARCHIVE=""
LOCAL_RELAY_ARCHIVE=""
LOCAL_CHECKSUMS=""
PREFIX=""

usage() {
  cat <<'EOF'
Usage: install-server.sh [options]

Options:
  --user                     install under ~/.local (default for non-root)
  --system                   install under /usr/local (default for root)
  --prefix DIR               override install prefix (lib/windrex and bin/)
  --version TAG              release tag to install (default: latest)
  --repo OWNER/REPO          GitHub repository (default: windzu/wind-rex)
  --with-relay               also install windrex-relay
  --systemd                  install systemd unit file(s) and reload systemd
  --no-upgrade               skip automatic in-place server upgrade
  --archive FILE             install windrex-server from a local .tar.gz archive
  --relay-archive FILE       install windrex-relay from a local .tar.gz archive
  --checksums FILE           local SHA256SUMS file for --archive verification
  --dry-run                  print planned actions without changing the system
  -h, --help                 show this help text
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --user) MODE="user" ;;
    --system) MODE="system" ;;
    --prefix) PREFIX="${2:?missing value for --prefix}"; shift ;;
    --version) VERSION="${2:?missing value for --version}"; shift ;;
    --repo) REPO="${2:?missing value for --repo}"; shift ;;
    --with-relay) WITH_RELAY=1 ;;
    --systemd) INSTALL_SYSTEMD=1 ;;
    --no-upgrade) NO_UPGRADE=1 ;;
    --archive) LOCAL_ARCHIVE="${2:?missing value for --archive}"; shift ;;
    --relay-archive) LOCAL_RELAY_ARCHIVE="${2:?missing value for --relay-archive}"; WITH_RELAY=1; shift ;;
    --checksums) LOCAL_CHECKSUMS="${2:?missing value for --checksums}"; shift ;;
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install-server.sh: unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

if [ -z "$MODE" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    MODE="system"
  else
    MODE="user"
  fi
fi

if [ -z "$PREFIX" ]; then
  if [ "$MODE" = "system" ]; then
    PREFIX="/usr/local"
  else
    PREFIX="${HOME}/.local"
  fi
fi

LIB_DIR="${PREFIX}/lib/windrex"
BIN_DIR="${PREFIX}/bin"
if [ "$MODE" = "system" ]; then
  UNIT_DIR="/etc/systemd/system"
else
  UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
fi

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) echo "install-server.sh: unsupported Linux architecture: $machine" >&2; exit 1 ;;
  esac
}

detect_os() {
  local os
  os="$(uname -s)"
  if [ "$os" != "Linux" ] && [ "$DRY_RUN" -eq 0 ] && [ -z "$LOCAL_ARCHIVE" ]; then
    echo "install-server.sh: headless server archives are published for Linux (detected: $os)" >&2
    exit 1
  fi
  echo "linux"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "install-server.sh: sha256sum or shasum is required to verify archives" >&2
    exit 1
  fi
}

verify_checksum() {
  local file="$1"
  local sums="$2"
  local base expected actual
  base="$(basename "$file")"
  expected="$(awk -v name="$base" '$2 == name || $2 == ("*" name) { print $1 }' "$sums")"
  if [ -z "$expected" ]; then
    echo "install-server.sh: checksum for $base not found in $sums" >&2
    exit 1
  fi
  actual="$(sha256_file "$file")"
  if [ "$actual" != "$expected" ]; then
    echo "install-server.sh: SHA-256 mismatch for $base (expected $expected, got $actual)" >&2
    exit 1
  fi
}

fetch_file() {
  local url="$1"
  local dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --output "$dest" "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    echo "install-server.sh: curl or wget is required to download releases" >&2
    exit 1
  fi
}

resolve_version() {
  if [ "$VERSION" != "latest" ]; then
    echo "$VERSION"
    return
  fi
  local api_url="https://api.github.com/repos/${REPO}/releases/latest"
  local json tag
  json="$(mktemp)"
  fetch_file "$api_url" "$json"
  tag="$(awk -F'"' '/"tag_name"[[:space:]]*:/ { if (!found) { print $4; found=1 } }' "$json")"
  rm -f "$json"
  if [ -z "$tag" ]; then
    echo "install-server.sh: could not determine latest release tag from $api_url; pass --version TAG" >&2
    exit 1
  fi
  echo "$tag"
}

atomic_install_file() {
  local src="$1"
  local dest="$2"
  local perm="$3"
  local tmp="${dest}.tmp.$$"
  cp "$src" "$tmp"
  chmod "$perm" "$tmp"
  mv -f "$tmp" "$dest"
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "dry-run:"
  echo "  mode: $MODE"
  echo "  target: ${OS}/${ARCH}"
  echo "  repo: $REPO"
  echo "  version: $VERSION"
  echo "  lib dir: $LIB_DIR"
  echo "  bin dir: $BIN_DIR"
  echo "  with relay: $WITH_RELAY"
  echo "  install systemd: $INSTALL_SYSTEMD (unit dir: $UNIT_DIR)"
  echo "  auto in-place upgrade: $((1 - NO_UPGRADE))"
  exit 0
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

SERVER_TAR="$LOCAL_ARCHIVE"
RELAY_TAR="$LOCAL_RELAY_ARCHIVE"
CHECKSUMS_FILE="$LOCAL_CHECKSUMS"

if [ -z "$SERVER_TAR" ]; then
  TAG="$(resolve_version)"
  BASE_URL="https://github.com/${REPO}/releases/download/${TAG}"
  CHECKSUMS_FILE="${TMP_DIR}/SHA256SUMS"
  echo "Downloading SHA256SUMS for ${TAG}..."
  fetch_file "${BASE_URL}/SHA256SUMS" "$CHECKSUMS_FILE"

  SERVER_TAR="${TMP_DIR}/windrex-server_${TAG}_${OS}_${ARCH}.tar.gz"
  echo "Downloading $(basename "$SERVER_TAR")..."
  fetch_file "${BASE_URL}/$(basename "$SERVER_TAR")" "$SERVER_TAR"
  verify_checksum "$SERVER_TAR" "$CHECKSUMS_FILE"

  if [ "$WITH_RELAY" -eq 1 ] && [ -z "$RELAY_TAR" ]; then
    RELAY_TAR="${TMP_DIR}/windrex-relay_${TAG}_${OS}_${ARCH}.tar.gz"
    echo "Downloading $(basename "$RELAY_TAR")..."
    fetch_file "${BASE_URL}/$(basename "$RELAY_TAR")" "$RELAY_TAR"
    verify_checksum "$RELAY_TAR" "$CHECKSUMS_FILE"
  fi
elif [ -n "$CHECKSUMS_FILE" ]; then
  verify_checksum "$SERVER_TAR" "$CHECKSUMS_FILE"
  if [ -n "$RELAY_TAR" ]; then
    verify_checksum "$RELAY_TAR" "$CHECKSUMS_FILE"
  fi
fi

mkdir -p "$LIB_DIR" "$BIN_DIR"

SERVER_STAGE="${TMP_DIR}/server"
mkdir -p "$SERVER_STAGE"
tar -xzf "$SERVER_TAR" -C "$SERVER_STAGE"

if [ ! -f "${SERVER_STAGE}/windrex-server" ]; then
  echo "install-server.sh: archive missing windrex-server binary" >&2
  exit 1
fi

atomic_install_file "${SERVER_STAGE}/windrex-server" "${LIB_DIR}/windrex-server" 0755
if [ -f "${SERVER_STAGE}/libghostty-vt.so" ]; then
  atomic_install_file "${SERVER_STAGE}/libghostty-vt.so" "${LIB_DIR}/libghostty-vt.so" 0644
fi
ln -sfn "${LIB_DIR}/windrex-server" "${BIN_DIR}/windrex-server"
echo "Installed ${BIN_DIR}/windrex-server -> ${LIB_DIR}/windrex-server"

RELAY_STAGE="${TMP_DIR}/relay"
if [ "$WITH_RELAY" -eq 1 ]; then
  if [ -z "$RELAY_TAR" ]; then
    echo "install-server.sh: --with-relay with --archive requires --relay-archive" >&2
    exit 1
  fi
  mkdir -p "$RELAY_STAGE"
  tar -xzf "$RELAY_TAR" -C "$RELAY_STAGE"
  if [ ! -f "${RELAY_STAGE}/windrex-relay" ]; then
    echo "install-server.sh: relay archive missing windrex-relay binary" >&2
    exit 1
  fi
  atomic_install_file "${RELAY_STAGE}/windrex-relay" "${LIB_DIR}/windrex-relay" 0755
  ln -sfn "${LIB_DIR}/windrex-relay" "${BIN_DIR}/windrex-relay"
  echo "Installed ${BIN_DIR}/windrex-relay -> ${LIB_DIR}/windrex-relay"
fi

if [ "$INSTALL_SYSTEMD" -eq 1 ]; then
  mkdir -p "$UNIT_DIR"
  if [ "$MODE" = "system" ]; then
    SERVER_UNIT_SRC="${SERVER_STAGE}/packaging/linux/windrex-server.service"
    RELAY_UNIT_SRC="${RELAY_STAGE}/packaging/linux/windrex-relay.service"
  else
    SERVER_UNIT_SRC="${SERVER_STAGE}/packaging/linux/windrex-server.user.service"
    RELAY_UNIT_SRC="${RELAY_STAGE}/packaging/linux/windrex-relay.user.service"
  fi
  if [ -f "$SERVER_UNIT_SRC" ]; then
    atomic_install_file "$SERVER_UNIT_SRC" "${UNIT_DIR}/windrex-server.service" 0644
    echo "Installed ${UNIT_DIR}/windrex-server.service"
  fi
  if [ "$WITH_RELAY" -eq 1 ] && [ -f "$RELAY_UNIT_SRC" ]; then
    atomic_install_file "$RELAY_UNIT_SRC" "${UNIT_DIR}/windrex-relay.service" 0644
    echo "Installed ${UNIT_DIR}/windrex-relay.service"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    if [ "$MODE" = "system" ]; then
      systemctl daemon-reload || true
    else
      systemctl --user daemon-reload || true
    fi
  fi
fi

# Never kill a running server: upgrade in place when one is active.
if [ "$NO_UPGRADE" -eq 0 ]; then
  if "${BIN_DIR}/windrex-server" server status >/dev/null 2>&1; then
    echo "Running session server detected; upgrading in place without interrupting shells..."
    "${BIN_DIR}/windrex-server" server upgrade
  fi
fi

cat <<EOF

Next steps:
  1. Ensure ${BIN_DIR} is in your PATH.
  2. Start the server:
     windrex-server server start
     (or with systemd: systemctl $( [ "$MODE" = "user" ] && echo "--user " )enable --now windrex-server)
  3. Pair iPhone or desktop clients:
     windrex-server rexapi direct on --listen :7443 --public https://<host>:7443
     (or behind NAT: windrex-server rexapi direct relay --url https://relay.example.com:7443 --pin <pin>)
     windrex-server remote enable && windrex-server remote pair start
EOF
