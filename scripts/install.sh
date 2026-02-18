#!/usr/bin/env bash
set -euo pipefail

REPO_OWNER="${REPO_OWNER:-bstncartwright}"
REPO_NAME="${REPO_NAME:-gopher}"
VERSION_TAG="${VERSION_TAG:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
BINARY_NAME="${BINARY_NAME:-gopher}"
CONFIG_PATH="${CONFIG_PATH:-/etc/gopher/gopher.toml}"
CONFIG_PATH_SET="false"
ENV_PATH="${ENV_PATH:-/etc/gopher/gopher.env}"
ROLE="${ROLE:-gateway}"
WITH_NATS="false"
INSTALL_SERVICE="true"
INIT_CONFIG="true"

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
  --config-path <path>     gateway config path (default: /etc/gopher/gopher.toml)
  --env-path <path>        gateway env file path (default: /etc/gopher/gopher.env)
  --role <gateway|node>    install role (default: gateway)
  --with-nats              install+enable local nats service (gateway role only)
  --no-service             skip systemd service installation
  --no-config-init         skip config file initialization
  -h, --help               show help

required env:
  GOPHER_GITHUB_TOKEN      github token with access to private repo releases
  GOPHER_GITHUB_UPDATE_TOKEN
                           alternate token env var (also accepted)

examples:
  GOPHER_GITHUB_TOKEN=... ./scripts/install.sh --role gateway --with-nats
  GOPHER_GITHUB_TOKEN=... ./scripts/install.sh --role node
EOF
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
  if [[ "$CONFIG_PATH_SET" == "false" ]]; then
    CONFIG_PATH="/etc/gopher/node.toml"
  fi
fi

for cmd in curl sha256sum python3 mktemp; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "missing required command: $cmd" >&2
    exit 1
  fi
done

# Backward-compatible token resolution:
# - GOPHER_GITHUB_TOKEN (original installer var)
# - GOPHER_GITHUB_UPDATE_TOKEN (runtime updater var)
if [[ -z "${GOPHER_GITHUB_TOKEN:-}" && -n "${GOPHER_GITHUB_UPDATE_TOKEN:-}" ]]; then
  GOPHER_GITHUB_TOKEN="${GOPHER_GITHUB_UPDATE_TOKEN}"
fi
if [[ -z "${GOPHER_GITHUB_TOKEN:-}" ]]; then
  echo "GOPHER_GITHUB_TOKEN is required for private release download" >&2
  echo "you can also set GOPHER_GITHUB_UPDATE_TOKEN" >&2
  exit 1
fi

case "$(uname -s)" in
  Linux) ;;
  *)
    echo "this installer currently supports linux only" >&2
    exit 1
    ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  x86_64|amd64) GOARCH="amd64" ;;
  aarch64|arm64) GOARCH="arm64" ;;
  *)
    echo "unsupported architecture: $ARCH_RAW" >&2
    exit 1
    ;;
esac

ASSET_NAME="gopher-linux-${GOARCH}"
API_BASE="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases"
if [[ "$VERSION_TAG" == "latest" ]]; then
  RELEASE_URL="${API_BASE}/latest"
else
  RELEASE_URL="${API_BASE}/tags/${VERSION_TAG}"
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

RELEASE_JSON="$TMP_DIR/release.json"
curl -fsSL \
  -H "Authorization: Bearer ${GOPHER_GITHUB_TOKEN}" \
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
  -H "Authorization: Bearer ${GOPHER_GITHUB_TOKEN}" \
  -H "Accept: application/octet-stream" \
  "$ASSET_URL" \
  -o "$ASSET_PATH"

curl -fsSL \
  -H "Authorization: Bearer ${GOPHER_GITHUB_TOKEN}" \
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
ACTUAL_SHA="$(sha256sum "$ASSET_PATH" | awk '{print $1}')"
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
  if ! run_as_root test -f "$CONFIG_PATH"; then
    run_as_root mkdir -p "$(dirname "$CONFIG_PATH")"
    if [[ "$ROLE" == "node" ]]; then
      run_as_root "$INSTALL_DIR/$BINARY_NAME" node config init --path "$CONFIG_PATH"
    else
      run_as_root "$INSTALL_DIR/$BINARY_NAME" gateway config init --path "$CONFIG_PATH"
    fi
    echo "initialized config at $CONFIG_PATH"
  else
    echo "config exists, keeping: $CONFIG_PATH"
  fi
fi

if ! run_as_root test -f "$ENV_PATH"; then
  run_as_root mkdir -p "$(dirname "$ENV_PATH")"
  run_as_root sh -c "printf '%s\n' 'GOPHER_GITHUB_TOKEN=' > '$ENV_PATH'"
  run_as_root chmod 600 "$ENV_PATH"
  echo "created env file at $ENV_PATH (set GOPHER_GITHUB_TOKEN before updates)"
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
  if [[ "$ROLE" == "node" ]]; then
    echo "skipped service install for node role"
  else
    echo "skipped service install (--no-service)"
  fi
fi
