#!/usr/bin/env sh
set -eu

ACTION="${1:-install}"
CMESH_VERSION="${CMESH_VERSION:-v0.1.0-alpha.9}"
CMESH_MANAGER_URL="${CMESH_MANAGER_URL:-}"
CMESH_JOIN_TOKEN="${CMESH_JOIN_TOKEN:-}"
CMESH_NODE_NAME="${CMESH_NODE_NAME:-$(hostname 2>/dev/null || echo cmesh-worker)}"
CMESH_CPU="${CMESH_CPU:-}"
CMESH_MEMORY_GB="${CMESH_MEMORY_GB:-2}"
CMESH_DISK_GB="${CMESH_DISK_GB:-10}"
CMESH_VRAM_GB="${CMESH_VRAM_GB:-0}"
CMESH_GPU="${CMESH_GPU:-true}"
CMESH_BENCHMARK="${CMESH_BENCHMARK:-true}"
CMESH_INSTALL_SERVICE="${CMESH_INSTALL_SERVICE:-false}"
CMESH_NONINTERACTIVE="${CMESH_NONINTERACTIVE:-false}"
CMESH_CACHE_DIR="${CMESH_CACHE_DIR:-$HOME/.cache/cmesh}"

CMESH_BIN_DIR_WAS_SET=false
if [ "${CMESH_BIN_DIR+x}" = "x" ]; then
  CMESH_BIN_DIR_WAS_SET=true
fi
if [ -z "${CMESH_BIN_DIR:-}" ]; then
  if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ]; then
    CMESH_BIN_DIR="/usr/local/bin"
  else
    CMESH_BIN_DIR="$HOME/.local/bin"
  fi
fi

can_prompt() {
  [ "$CMESH_NONINTERACTIVE" != "true" ] && [ -r /dev/tty ]
}

prompt_required() {
  var_name="$1"
  prompt="$2"
  current_value="$(eval "printf '%s' \"\${$var_name}\"")"
  if [ -n "$current_value" ]; then
    return
  fi
  if ! can_prompt; then
    echo "missing required $var_name; pass it as an environment variable" >&2
    exit 1
  fi
  printf "%s: " "$prompt" >&2
  IFS= read -r value < /dev/tty
  eval "$var_name=\$value"
}

prompt_default() {
  var_name="$1"
  prompt="$2"
  default_value="$3"
  current_value="$(eval "printf '%s' \"\${$var_name}\"")"
  if [ -n "$current_value" ]; then
    return
  fi
  if ! can_prompt; then
    eval "$var_name=\$default_value"
    return
  fi
  printf "%s [%s]: " "$prompt" "$default_value" >&2
  IFS= read -r value < /dev/tty
  if [ -z "$value" ]; then
    value="$default_value"
  fi
  eval "$var_name=\$value"
}

is_yes() {
  case "$1" in
    y|Y|yes|YES|true|TRUE|1) return 0 ;;
    *) return 1 ;;
  esac
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

install_binary() {
  asset="$(detect_asset)"
  url="https://github.com/NythralHome/cmesh/releases/download/$CMESH_VERSION/$asset"
  tmp="${TMPDIR:-/tmp}/$asset.$$"

  mkdir -p "$CMESH_BIN_DIR"
  download "$url" "$tmp"
  chmod +x "$tmp"
  mv "$tmp" "$CMESH_BIN_DIR/cmesh"

  echo "installed $("$CMESH_BIN_DIR/cmesh" version)"
}

default_cpu() {
  if command -v nproc >/dev/null 2>&1; then
    nproc
  elif command -v sysctl >/dev/null 2>&1; then
    sysctl -n hw.ncpu
  else
    echo "2"
  fi
}

collect_worker_config() {
  prompt_required CMESH_MANAGER_URL "Manager URL, for example https://cmesh.nythral.com"
  prompt_required CMESH_JOIN_TOKEN "Join token"
  prompt_default CMESH_CPU "CPU cores CMesh may use" "$(default_cpu)"
  prompt_default CMESH_MEMORY_GB "RAM in GB CMesh may use" "$CMESH_MEMORY_GB"
  prompt_default CMESH_DISK_GB "Disk in GB CMesh may use" "$CMESH_DISK_GB"
  prompt_default CMESH_GPU "Allow GPU discovery/use" "$CMESH_GPU"
  prompt_default CMESH_VRAM_GB "VRAM in GB CMesh may use, 0 means default" "$CMESH_VRAM_GB"
  prompt_default CMESH_BENCHMARK "Run benchmarks after joining" "$CMESH_BENCHMARK"
  prompt_default CMESH_INSTALL_SERVICE "Run in background and start on boot/login" "$CMESH_INSTALL_SERVICE"

  if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ] && [ "$CMESH_BIN_DIR_WAS_SET" != "true" ]; then
    CMESH_BIN_DIR="/usr/local/bin"
  fi
}

benchmark_arg() {
  if is_yes "$CMESH_BENCHMARK"; then
    printf "%s" "--benchmark"
  fi
}

run_foreground() {
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
    $(benchmark_arg)
}

install_systemd_service() {
  if [ "$(uname -s)" != "Linux" ]; then
    echo "systemd service install is only supported on Linux" >&2
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
CMESH_BENCHMARK="$CMESH_BENCHMARK"
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
ExecStart=/bin/sh -c 'benchmark=""; case "\$CMESH_BENCHMARK" in true|TRUE|1|yes|YES|y|Y) benchmark="--benchmark" ;; esac; exec $CMESH_BIN_DIR/cmesh worker run --manager "\$CMESH_MANAGER_URL" --token "\$CMESH_JOIN_TOKEN" --name "\$CMESH_NODE_NAME" --cpu "\$CMESH_CPU" --memory-gb "\$CMESH_MEMORY_GB" --disk-gb "\$CMESH_DISK_GB" --vram-gb "\$CMESH_VRAM_GB" --gpu="\$CMESH_GPU" --cache-dir "\$CMESH_CACHE_DIR" \$benchmark'
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now cmesh-worker.service
  echo "CMesh worker service installed and started"
  systemctl --no-pager status cmesh-worker.service || true
}

xml_escape() {
  printf "%s" "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'
}

install_launchd_service() {
  if [ "$(uname -s)" != "Darwin" ]; then
    echo "launchd service install is only supported on macOS" >&2
    exit 1
  fi

  plist="$HOME/Library/LaunchAgents/com.cmesh.worker.plist"
  log_dir="$HOME/Library/Logs/CMesh"
  mkdir -p "$HOME/Library/LaunchAgents" "$log_dir" "$CMESH_CACHE_DIR"

  bench=""
  if is_yes "$CMESH_BENCHMARK"; then
    bench="<string>--benchmark</string>"
  fi

  cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.cmesh.worker</string>
  <key>ProgramArguments</key>
  <array>
    <string>$(xml_escape "$CMESH_BIN_DIR/cmesh")</string>
    <string>worker</string>
    <string>run</string>
    <string>--manager</string><string>$(xml_escape "$CMESH_MANAGER_URL")</string>
    <string>--token</string><string>$(xml_escape "$CMESH_JOIN_TOKEN")</string>
    <string>--name</string><string>$(xml_escape "$CMESH_NODE_NAME")</string>
    <string>--cpu</string><string>$(xml_escape "$CMESH_CPU")</string>
    <string>--memory-gb</string><string>$(xml_escape "$CMESH_MEMORY_GB")</string>
    <string>--disk-gb</string><string>$(xml_escape "$CMESH_DISK_GB")</string>
    <string>--vram-gb</string><string>$(xml_escape "$CMESH_VRAM_GB")</string>
    <string>--gpu=$(xml_escape "$CMESH_GPU")</string>
    <string>--cache-dir</string><string>$(xml_escape "$CMESH_CACHE_DIR")</string>
    $bench
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>$(xml_escape "$log_dir/worker.log")</string>
  <key>StandardErrorPath</key><string>$(xml_escape "$log_dir/worker.err.log")</string>
</dict>
</plist>
EOF

  launchctl bootout "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
  launchctl bootstrap "gui/$(id -u)" "$plist"
  launchctl kickstart -k "gui/$(id -u)/com.cmesh.worker"
  echo "CMesh worker launchd service installed and started"
  launchctl print "gui/$(id -u)/com.cmesh.worker" | sed -n '1,24p' || true
}

linux_service_action() {
  action="$1"
  case "$action" in
    status) systemctl --no-pager status cmesh-worker.service || true ;;
    start) systemctl start cmesh-worker.service && systemctl --no-pager status cmesh-worker.service || true ;;
    stop) systemctl stop cmesh-worker.service && echo "CMesh worker stopped" ;;
    uninstall)
      systemctl disable --now cmesh-worker.service >/dev/null 2>&1 || true
      rm -f /etc/systemd/system/cmesh-worker.service
      systemctl daemon-reload
      echo "CMesh worker service removed"
      ;;
  esac
}

mac_service_action() {
  action="$1"
  plist="$HOME/Library/LaunchAgents/com.cmesh.worker.plist"
  case "$action" in
    status)
      if [ ! -f "$plist" ]; then
        echo "CMesh worker service is not installed"
        return
      fi
      launchctl print "gui/$(id -u)/com.cmesh.worker" || true
      ;;
    start)
      if [ -f "$plist" ]; then
        launchctl bootstrap "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
        launchctl kickstart -k "gui/$(id -u)/com.cmesh.worker"
      fi
      launchctl print "gui/$(id -u)/com.cmesh.worker" || true
      ;;
    stop)
      launchctl bootout "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
      echo "CMesh worker stopped"
      ;;
    uninstall)
      launchctl bootout "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
      rm -f "$plist"
      echo "CMesh worker launchd service removed"
      ;;
  esac
}

service_action() {
  os="$(uname -s)"
  case "$os" in
    Linux) linux_service_action "$1" ;;
    Darwin) mac_service_action "$1" ;;
    *) echo "unsupported OS: $os" >&2; exit 1 ;;
  esac
}

case "$ACTION" in
  install)
    collect_worker_config
    install_binary
    if [ "$CMESH_INSTALL_SERVICE" = "true" ]; then
      case "$(uname -s)" in
        Linux) install_systemd_service ;;
        Darwin) install_launchd_service ;;
      esac
    else
      run_foreground
    fi
    ;;
  status|start|stop|uninstall)
    service_action "$ACTION"
    ;;
  *)
    echo "usage: install-worker.sh [install|status|start|stop|uninstall]" >&2
    exit 1
    ;;
esac
