#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="${REPO_OWNER:-bstncartwright}"
REPO_NAME="${REPO_NAME:-gopher}"
VERSION_TAG="${VERSION_TAG:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="${BINARY_NAME:-gopher}"
CONFIG_PATH="${CONFIG_PATH:-}"
CONFIG_PATH_SET="false"
ENV_PATH="${ENV_PATH:-}"
ENV_PATH_SET="false"
ROLE="${ROLE:-gateway}"
WITH_NATS="false"
INSTALL_SERVICE="${INSTALL_SERVICE:-}"
INIT_CONFIG="true"
GOOS=""

usage() {
  cat <<'EOF'
gopher installer

usage:
  ./scripts/install.sh [options]

options:
  --version <tag>          release tag (default: latest)
  --owner <owner>          github repo owner (default: bstncartwright)
  --repo <name>            github repo name (default: gopher)
  --install-dir <path>     binary install dir (default: /usr/local/bin)
  --binary-name <name>     installed binary name (default: gopher)
  --config-path <path>     config path (default: ~/.gopher/gopher.toml or ~/.gopher/node.toml)
  --env-path <path>        env file path (default: ~/.gopher/gopher.env)
  --role <gateway|node>    install role (default: gateway)
  --with-nats              install+enable local nats service (gateway role only; linux only)
  --no-service             skip service installation (default on macOS)
  --no-config-init         skip config file initialization
  -h, --help               show help

examples:
  ./scripts/install.sh --role gateway --with-nats
  ./scripts/install.sh --role node
  GOPHER_GITHUB_TOKEN=... ./scripts/install.sh --version v1.2.3
EOF
}

resolve_target_user() {
  if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "${SUDO_USER}"
    return 0
  fi
  id -un
}

resolve_home_for_user() {
  python3 - <<'PY' "$1"
import pwd
import sys

username = sys.argv[1]
try:
    print(pwd.getpwnam(username).pw_dir)
except KeyError:
    sys.exit(1)
PY
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      VERSION_TAG="${2:-}"
      shift 2
      ;;
    --owner)
      REPO_OWNER="${2:-}"
      shift 2
      ;;
    --repo)
      REPO_NAME="${2:-}"
      shift 2
      ;;
    --install-dir)
      INSTALL_DIR="${2:-}"
      shift 2
      ;;
    --binary-name)
      BINARY_NAME="${2:-}"
      shift 2
      ;;
    --config-path)
      CONFIG_PATH="${2:-}"
      CONFIG_PATH_SET="true"
      shift 2
      ;;
    --env-path)
      ENV_PATH="${2:-}"
      ENV_PATH_SET="true"
      shift 2
      ;;
    --role)
      ROLE="${2:-}"
      shift 2
      ;;
    --with-nats)
      WITH_NATS="true"
      shift
      ;;
    --no-service)
      INSTALL_SERVICE="false"
      shift
      ;;
    --no-config-init)
      INIT_CONFIG="false"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

ROLE="$(echo "$ROLE" | tr '[:upper:]' '[:lower:]')"
case "$ROLE" in
  gateway|node) ;;
  *)
    echo "invalid --role value: $ROLE (expected gateway or node)" >&2
    exit 1
    ;;
esac
if [[ "$ROLE" == "node" ]]; then
  if [[ "$WITH_NATS" == "true" ]]; then
    echo "--with-nats is only valid for --role gateway" >&2
    exit 1
  fi
fi

case "$(uname -s)" in
  Linux)
    GOOS="linux"
    if [[ -z "$INSTALL_SERVICE" ]]; then
      INSTALL_SERVICE="true"
    fi
    ;;
  Darwin)
    GOOS="darwin"
    if [[ -z "$INSTALL_SERVICE" ]]; then
      INSTALL_SERVICE="false"
    fi
    ;;
  *)
    echo "unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

if [[ "$INSTALL_SERVICE" == "true" && "$GOOS" != "linux" ]]; then
  echo "service installation is currently supported on linux only; re-run with --no-service on macOS" >&2
  exit 1
fi
if [[ "$WITH_NATS" == "true" && "$GOOS" != "linux" ]]; then
  echo "--with-nats is currently supported on linux only" >&2
  exit 1
fi

resolve_target_home() {
  local target_user
  target_user="$(resolve_target_user)"
  if [[ -n "$target_user" ]]; then
    local home
    home="$(resolve_home_for_user "$target_user" 2>/dev/null || true)"
    if [[ -n "$home" ]]; then
      printf '%s\n' "$home"
      return 0
    fi
  fi
  if [[ -n "${HOME:-}" ]]; then
    printf '%s\n' "${HOME}"
    return 0
  fi
  if [[ "$GOOS" == "darwin" ]]; then
    printf '%s\n' "/var/root"
    return 0
  fi
  printf '%s\n' "/root"
}

STATE_DIR="$(resolve_target_home)/.gopher"
TARGET_USER="$(resolve_target_user)"
if [[ "$CONFIG_PATH_SET" == "false" ]]; then
  if [[ "$ROLE" == "node" ]]; then
    CONFIG_PATH="${STATE_DIR}/node.toml"
  else
    CONFIG_PATH="${STATE_DIR}/gopher.toml"
  fi
fi
if [[ "$ENV_PATH_SET" == "false" ]]; then
  ENV_PATH="${STATE_DIR}/gopher.env"
fi

for cmd in curl python3 mktemp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing required command: $cmd" >&2
    exit 1
  fi
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo "missing required command: sha256sum or shasum" >&2
  exit 1
fi

if [[ -z "${GOPHER_GITHUB_TOKEN:-}" && -n "${GOPHER_GITHUB_UPDATE_TOKEN:-}" ]]; then
  GOPHER_GITHUB_TOKEN="${GOPHER_GITHUB_UPDATE_TOKEN}"
fi

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *)
    echo "unsupported architecture: $ARCH_RAW" >&2
    exit 1
    ;;
esac

ASSET_NAME="gopher-${GOOS}-${GOARCH}"
API_BASE="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases"
if [[ "$VERSION_TAG" == "latest" ]]; then
  RELEASE_URL="${API_BASE}/latest"
else
  RELEASE_URL="${API_BASE}/tags/${VERSION_TAG}"
fi

curl_auth_args=()
if [[ -n "${GOPHER_GITHUB_TOKEN:-}" ]]; then
  curl_auth_args=(-H "Authorization: Bearer ${GOPHER_GITHUB_TOKEN}")
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

RELEASE_JSON="$TMP_DIR/release.json"
curl -fsSL \
  "${curl_auth_args[@]}" \
  -H "Accept: application/vnd.github+json" \
  "$RELEASE_URL" \
  -o "$RELEASE_JSON"

ASSET_URL="$(python3 - <<'PY' "$RELEASE_JSON" "$ASSET_NAME"
import json, sys
release_path = sys.argv[1]
asset_name = sys.argv[2]
with open(release_path, "r", encoding="utf-8") as fh:
    release = json.load(fh)
for asset in release.get("assets", []):
    if asset.get("name") == asset_name:
        print(asset.get("url", "") or asset.get("browser_download_url", ""))
        break
PY
)"

CHECKSUMS_URL="$(python3 - <<'PY' "$RELEASE_JSON"
import json, sys
release_path = sys.argv[1]
with open(release_path, "r", encoding="utf-8") as fh:
    release = json.load(fh)
for asset in release.get("assets", []):
    name = (asset.get("name") or "").lower()
    if name == "checksums.txt" or "checksum" in name:
        print(asset.get("url", "") or asset.get("browser_download_url", ""))
        break
PY
)"

if [[ -z "$ASSET_URL" ]]; then
  echo "release asset not found: $ASSET_NAME" >&2
  exit 1
fi
if [[ -z "$CHECKSUMS_URL" ]]; then
  echo "checksums asset not found in release" >&2
  exit 1
fi

ASSET_PATH="$TMP_DIR/$ASSET_NAME"
CHECKSUMS_PATH="$TMP_DIR/checksums.txt"

curl -fsSL \
  "${curl_auth_args[@]}" \
  -H "Accept: application/octet-stream" \
  "$ASSET_URL" \
  -o "$ASSET_PATH"

curl -fsSL \
  "${curl_auth_args[@]}" \
  -H "Accept: application/octet-stream" \
  "$CHECKSUMS_URL" \
  -o "$CHECKSUMS_PATH"

EXPECTED_SHA="$(awk -v file="$ASSET_NAME" '{
  candidate = $2
  gsub(/^.*\//, "", candidate)
  if ($2 == file || candidate == file) {
    print $1
    exit
  }
}' "$CHECKSUMS_PATH")"
if [[ -z "$EXPECTED_SHA" ]]; then
  echo "checksum entry not found for $ASSET_NAME" >&2
  exit 1
fi
sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return 0
  fi
  shasum -a 256 "$1" | awk '{print $1}'
}
ACTUAL_SHA="$(sha256_file "$ASSET_PATH")"
if [[ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]]; then
  echo "checksum mismatch for $ASSET_NAME" >&2
  exit 1
fi

run_as_root() {
  if [[ "${EUID}" -eq 0 ]]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    echo "root privileges required (install sudo or run as root)" >&2
    exit 1
  fi
}

run_as_target_user() {
  if [[ "${EUID}" -eq 0 && "$TARGET_USER" != "root" ]]; then
    sudo -u "$TARGET_USER" "$@"
    return 0
  fi
  "$@"
}

create_env_file() {
  local env_path="$1"
  run_as_target_user bash -c 'umask 077; printf "%s\n" "# optional: GOPHER_GITHUB_TOKEN=..." > "$1"' _ "$env_path"
}

install_nats_service() {
  if command -v nats-server >/dev/null 2>&1; then
    echo "nats-server already present, skipping package install"
  elif command -v apt-get >/dev/null 2>&1; then
    run_as_root apt-get update
    run_as_root apt-get install -y nats-server
  elif command -v dnf >/dev/null 2>&1; then
    run_as_root dnf install -y nats-server
  elif command -v yum >/dev/null 2>&1; then
    run_as_root yum install -y nats-server
  else
    echo "unable to auto-install nats-server (no supported package manager found)" >&2
    echo "install nats manually and re-run without --with-nats if needed" >&2
    exit 1
  fi
  run_as_root systemctl enable --now nats-server
  echo "nats-server enabled and started"
}

run_as_root mkdir -p "$INSTALL_DIR"
run_as_root install -m 0755 "$ASSET_PATH" "$INSTALL_DIR/$BINARY_NAME"
echo "installed $BINARY_NAME to $INSTALL_DIR/$BINARY_NAME"

if [[ "$ROLE" == "gateway" && "$WITH_NATS" == "true" ]]; then
  install_nats_service
fi

if [[ "$INIT_CONFIG" == "true" ]]; then
  if ! run_as_target_user test -f "$CONFIG_PATH"; then
    run_as_target_user mkdir -p "$(dirname "$CONFIG_PATH")"
    if [[ "$ROLE" == "node" ]]; then
      run_as_target_user "$INSTALL_DIR/$BINARY_NAME" node config init --path "$CONFIG_PATH"
    else
      run_as_target_user "$INSTALL_DIR/$BINARY_NAME" gateway config init --path "$CONFIG_PATH"
    fi
    echo "initialized config at $CONFIG_PATH"
  else
    echo "config exists, keeping: $CONFIG_PATH"
  fi
fi

if ! run_as_target_user test -f "$ENV_PATH"; then
  run_as_target_user mkdir -p "$(dirname "$ENV_PATH")"
  create_env_file "$ENV_PATH"
  echo "created env file at $ENV_PATH"
fi

if [[ "$INSTALL_SERVICE" == "true" ]]; then
  run_as_root "$INSTALL_DIR/$BINARY_NAME" service install \
    --role "$ROLE" \
    --config "$CONFIG_PATH" \
    --env-file "$ENV_PATH" \
    --binary-path "$INSTALL_DIR/$BINARY_NAME"
  if [[ "$ROLE" == "node" ]]; then
    echo "service installed. check logs with: $BINARY_NAME logs --unit gopher-node.service --lines 200"
  else
    echo "service installed. check status with: $BINARY_NAME status"
  fi
else
  echo "skipped service install"
fi
