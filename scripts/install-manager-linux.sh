#!/usr/bin/env sh
set -eu

CMESH_VERSION="${CMESH_VERSION:-v0.1.0-alpha.8}"
CMESH_ADDR="${CMESH_ADDR:-127.0.0.1:8080}"
CMESH_DOMAIN="${CMESH_DOMAIN:-}"
CMESH_ADMIN_EMAIL="${CMESH_ADMIN_EMAIL:-}"
CMESH_INSTALL_CADDY="${CMESH_INSTALL_CADDY:-}"
CMESH_NONINTERACTIVE="${CMESH_NONINTERACTIVE:-false}"
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
  if ! can_prompt; then
    echo "missing required $var_name; pass it as an environment variable" >&2
    exit 1
  fi
  printf "%s: " "$prompt" >&2
  IFS= read -r value < /dev/tty
  eval "$var_name=\$value"
}

can_prompt() {
  [ "$CMESH_NONINTERACTIVE" != "true" ] && [ -r /dev/tty ]
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

install_caddy() {
  if command -v caddy >/dev/null 2>&1; then
    return
  fi

  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl gnupg
    curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/gpg.key" | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf "https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt" > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update
    apt-get install -y caddy
    return
  fi

  if command -v dnf >/dev/null 2>&1; then
    dnf install -y 'dnf-command(copr)' || true
    dnf copr enable -y @caddy/caddy
    dnf install -y caddy
    return
  fi

  if command -v yum >/dev/null 2>&1; then
    yum install -y yum-plugin-copr || true
    yum copr enable -y @caddy/caddy
    yum install -y caddy
    return
  fi

  echo "could not install Caddy automatically on this distribution" >&2
  exit 1
}

configure_caddy() {
  if [ -z "$CMESH_DOMAIN" ]; then
    return
  fi

  install_caddy
  install -d -m 0755 /etc/caddy

  caddyfile="/etc/caddy/Caddyfile"
  if [ -f "$caddyfile" ]; then
    cp "$caddyfile" "$caddyfile.cmesh-backup.$(date +%Y%m%d%H%M%S)"
  fi

  email_block=""
  if [ -n "$CMESH_ADMIN_EMAIL" ]; then
    email_block="{
  email $CMESH_ADMIN_EMAIL
}

"
  fi

  cat > "$caddyfile" <<EOF
${email_block}$CMESH_DOMAIN {
  reverse_proxy 127.0.0.1:8080
}
EOF

  caddy validate --config "$caddyfile"
  systemctl enable --now caddy.service
  systemctl reload caddy.service || systemctl restart caddy.service
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

if can_prompt; then
  echo "CMesh manager installer"
  echo
  prompt_default CMESH_DOMAIN "Domain for this cluster dashboard/API, leave blank to skip HTTPS setup" "$CMESH_DOMAIN"
  if [ -n "$CMESH_DOMAIN" ]; then
    if [ -z "$CMESH_PUBLIC_URL" ]; then
      CMESH_PUBLIC_URL="https://$CMESH_DOMAIN"
    fi
    prompt_default CMESH_ADMIN_EMAIL "Email for Let's Encrypt notices" "$CMESH_ADMIN_EMAIL"
    prompt_default CMESH_INSTALL_CADDY "Install/configure Caddy HTTPS reverse proxy" "yes"
  fi
elif [ -n "$CMESH_DOMAIN" ] && [ -z "$CMESH_PUBLIC_URL" ]; then
  CMESH_PUBLIC_URL="https://$CMESH_DOMAIN"
fi
if is_yes "$CMESH_INSTALL_CADDY" && [ -z "$CMESH_DOMAIN" ]; then
  echo "CMESH_DOMAIN is required when CMESH_INSTALL_CADDY=true" >&2
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

if is_yes "$CMESH_INSTALL_CADDY"; then
  configure_caddy
fi

echo "installed $($CMESH_BIN_DIR/cmesh version)"
echo "CMesh manager service is active: $(systemctl is-active cmesh.service)"
if [ -n "$CMESH_DOMAIN" ]; then
  echo "CMesh public URL: $CMESH_PUBLIC_URL"
else
  echo "CMesh local URL: http://127.0.0.1:8080"
fi
echo "manager tokens are stored in /etc/cmesh/manager.env"
echo "manager state is stored in $CMESH_STATE_PATH"
echo "operator token: $CMESH_OPERATOR_TOKEN"
echo "join token: $CMESH_JOIN_TOKEN"
