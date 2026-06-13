#!/usr/bin/env sh
set -eu

CMESH_VERSION="${CMESH_VERSION:-v0.1.0-alpha.4}"
CMESH_MANAGER_URL="${CMESH_MANAGER_URL:-}"
CMESH_JOIN_TOKEN="${CMESH_JOIN_TOKEN:-}"
CMESH_NODE_NAME="${CMESH_NODE_NAME:-$(hostname 2>/dev/null || echo cmesh-worker)}"
CMESH_CPU="${CMESH_CPU:-}"
CMESH_MEMORY_GB="${CMESH_MEMORY_GB:-2}"
CMESH_DISK_GB="${CMESH_DISK_GB:-10}"
CMESH_VRAM_GB="${CMESH_VRAM_GB:-0}"
CMESH_GPU="${CMESH_GPU:-true}"
CMESH_BENCHMARK="${CMESH_BENCHMARK:-true}"
CMESH_CACHE_DIR="${CMESH_CACHE_DIR:-$HOME/.cache/cmesh}"
CMESH_INSTALL_SERVICE="${CMESH_INSTALL_SERVICE:-false}"
if [ -z "${CMESH_BIN_DIR:-}" ]; then
  if [ "$CMESH_INSTALL_SERVICE" = "true" ]; then
    CMESH_BIN_DIR="/usr/local/bin"
  else
    CMESH_BIN_DIR="$HOME/.local/bin"
  fi
fi

prompt_if_empty() {
  var_name="$1"
  prompt="$2"
  current_value="$(eval "printf '%s' \"\${$var_name}\"")"
  if [ -n "$current_value" ]; then
    return
  fi
  if [ ! -t 0 ]; then
    echo "missing required $var_name; pass it as an environment variable" >&2
    exit 1
  fi
  printf "%s: " "$prompt" >&2
  IFS= read -r value
  eval "$var_name=\$value"
}

detect_asset() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    darwin) os="darwin" ;;
    linux) os="linux" ;;
    *) echo "unsupported OS: $os" >&2; exit 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "unsupported CPU architecture: $arch" >&2; exit 1 ;;
  esac
  printf "cmesh-%s-%s" "$os" "$arch"
}

download() {
  url="$1"
  out="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$out"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$out"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

install_systemd_service() {
  if [ "$(uname -s)" != "Linux" ]; then
    echo "service install is only supported on Linux" >&2
    exit 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemd is required for service install" >&2
    exit 1
  fi
  if [ "$(id -u)" -ne 0 ]; then
    echo "service install requires root; rerun with sudo or use CMESH_INSTALL_SERVICE=false" >&2
    exit 1
  fi

  install -d -m 0755 /etc/cmesh /var/lib/cmesh
  cat > /etc/cmesh/worker.env <<EOF
CMESH_MANAGER_URL="$CMESH_MANAGER_URL"
CMESH_JOIN_TOKEN="$CMESH_JOIN_TOKEN"
CMESH_NODE_NAME="$CMESH_NODE_NAME"
CMESH_CPU="$CMESH_CPU"
CMESH_MEMORY_GB="$CMESH_MEMORY_GB"
CMESH_DISK_GB="$CMESH_DISK_GB"
CMESH_VRAM_GB="$CMESH_VRAM_GB"
CMESH_GPU="$CMESH_GPU"
CMESH_CACHE_DIR="/var/lib/cmesh/cache"
EOF
  chmod 600 /etc/cmesh/worker.env

  cat > /etc/systemd/system/cmesh-worker.service <<EOF
[Unit]
Description=CMesh Worker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/cmesh/worker.env
ExecStart=$CMESH_BIN_DIR/cmesh worker run --manager \${CMESH_MANAGER_URL} --token \${CMESH_JOIN_TOKEN} --name \${CMESH_NODE_NAME} --cpu \${CMESH_CPU} --memory-gb \${CMESH_MEMORY_GB} --disk-gb \${CMESH_DISK_GB} --vram-gb \${CMESH_VRAM_GB} --gpu=\${CMESH_GPU} --cache-dir \${CMESH_CACHE_DIR} --benchmark
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now cmesh-worker.service
  echo "CMesh worker service installed and started"
}

prompt_if_empty CMESH_MANAGER_URL "Manager URL, for example https://cmesh.nythral.com"
prompt_if_empty CMESH_JOIN_TOKEN "Join token"

if [ -z "$CMESH_CPU" ]; then
  if command -v nproc >/dev/null 2>&1; then
    CMESH_CPU="$(nproc)"
  elif command -v sysctl >/dev/null 2>&1; then
    CMESH_CPU="$(sysctl -n hw.ncpu)"
  else
    CMESH_CPU="2"
  fi
fi

asset="$(detect_asset)"
url="https://github.com/NythralHome/cmesh/releases/download/$CMESH_VERSION/$asset"
tmp="${TMPDIR:-/tmp}/$asset.$$"

mkdir -p "$CMESH_BIN_DIR"
download "$url" "$tmp"
chmod +x "$tmp"
mv "$tmp" "$CMESH_BIN_DIR/cmesh"

echo "installed $("$CMESH_BIN_DIR/cmesh" version)"

if [ "$CMESH_INSTALL_SERVICE" = "true" ]; then
  install_systemd_service
else
  benchmark_flag=""
  if [ "$CMESH_BENCHMARK" = "true" ]; then
    benchmark_flag="--benchmark"
  fi
  exec "$CMESH_BIN_DIR/cmesh" worker run \
    --manager "$CMESH_MANAGER_URL" \
    --token "$CMESH_JOIN_TOKEN" \
    --name "$CMESH_NODE_NAME" \
    --cpu "$CMESH_CPU" \
    --memory-gb "$CMESH_MEMORY_GB" \
    --disk-gb "$CMESH_DISK_GB" \
    --vram-gb "$CMESH_VRAM_GB" \
    --gpu="$CMESH_GPU" \
    --cache-dir "$CMESH_CACHE_DIR" \
    $benchmark_flag
fi
