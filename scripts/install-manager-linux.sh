#!/usr/bin/env sh
set -eu

CMESH_VERSION="${CMESH_VERSION:-v0.1.0-alpha.7}"
CMESH_ADDR="${CMESH_ADDR:-127.0.0.1:8080}"
CMESH_JOIN_TOKEN="${CMESH_JOIN_TOKEN:-}"
CMESH_OPERATOR_TOKEN="${CMESH_OPERATOR_TOKEN:-}"
CMESH_PUBLIC_URL="${CMESH_PUBLIC_URL:-}"
CMESH_STATE_PATH="${CMESH_STATE_PATH:-/var/lib/cmesh/cmesh-state.json}"
DATABASE_URL="${DATABASE_URL:-}"
CMESH_BIN_DIR="${CMESH_BIN_DIR:-/usr/local/bin}"

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
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "unsupported CPU architecture: $arch" >&2; exit 1 ;;
  esac
  printf "cmesh-linux-%s" "$arch"
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

if [ "$(uname -s)" != "Linux" ]; then
  echo "manager installer currently supports Linux only" >&2
  exit 1
fi
if [ "$(id -u)" -ne 0 ]; then
  echo "manager install requires root; rerun with sudo" >&2
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd is required" >&2
  exit 1
fi

if [ -z "$CMESH_JOIN_TOKEN" ]; then
  if command -v openssl >/dev/null 2>&1; then
    CMESH_JOIN_TOKEN="$(openssl rand -hex 32)"
  else
    prompt_if_empty CMESH_JOIN_TOKEN "Join token"
  fi
fi
if [ -z "$CMESH_OPERATOR_TOKEN" ] && command -v openssl >/dev/null 2>&1; then
  CMESH_OPERATOR_TOKEN="$(openssl rand -hex 32)"
fi

asset="$(detect_asset)"
url="https://github.com/NythralHome/cmesh/releases/download/$CMESH_VERSION/$asset"
tmp="${TMPDIR:-/tmp}/$asset.$$"

download "$url" "$tmp"
install -m 0755 -o root -g root "$tmp" "$CMESH_BIN_DIR/cmesh"
rm -f "$tmp"

id cmesh >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/cmesh --shell /usr/sbin/nologin cmesh 2>/dev/null || useradd --system --create-home --home-dir /var/lib/cmesh --shell /sbin/nologin cmesh
install -d -m 0755 /etc/cmesh /var/lib/cmesh

cat > /etc/cmesh/manager.env <<EOF
CMESH_JOIN_TOKEN="$CMESH_JOIN_TOKEN"
CMESH_OPERATOR_TOKEN="$CMESH_OPERATOR_TOKEN"
CMESH_PUBLIC_URL="$CMESH_PUBLIC_URL"
CMESH_STATE_PATH="$CMESH_STATE_PATH"
DATABASE_URL="$DATABASE_URL"
EOF
chmod 600 /etc/cmesh/manager.env

cat > /etc/systemd/system/cmesh.service <<EOF
[Unit]
Description=CMesh Manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cmesh
Group=cmesh
WorkingDirectory=/var/lib/cmesh
EnvironmentFile=/etc/cmesh/manager.env
ExecStart=$CMESH_BIN_DIR/cmesh manager start --addr $CMESH_ADDR
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cmesh.service

echo "installed $($CMESH_BIN_DIR/cmesh version)"
echo "CMesh manager service is active: $(systemctl is-active cmesh.service)"
echo "manager tokens are stored in /etc/cmesh/manager.env"
echo "manager state is stored in $CMESH_STATE_PATH"
