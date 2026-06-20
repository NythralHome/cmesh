#!/usr/bin/env sh
set -eu

ACTION="${1:-install}"
CMESH_VERSION="${CMESH_VERSION:-latest}"
CMESH_ADDR="${CMESH_ADDR:-127.0.0.1:8080}"
CMESH_DOMAIN="${CMESH_DOMAIN:-}"
CMESH_ADMIN_EMAIL="${CMESH_ADMIN_EMAIL:-}"
CMESH_INSTALL_CADDY="${CMESH_INSTALL_CADDY:-}"
CMESH_INSTALL_DRY_RUN="${CMESH_INSTALL_DRY_RUN:-false}"
CMESH_NONINTERACTIVE="${CMESH_NONINTERACTIVE:-false}"
CMESH_PRINT_SECRETS="${CMESH_PRINT_SECRETS:-false}"
CMESH_JOIN_TOKEN="${CMESH_JOIN_TOKEN:-}"
CMESH_OPERATOR_TOKEN="${CMESH_OPERATOR_TOKEN:-}"
CMESH_PUBLIC_URL="${CMESH_PUBLIC_URL:-}"
CMESH_STATE_PATH="${CMESH_STATE_PATH:-/var/lib/cmesh/cmesh-state.json}"
CMESH_EXTRA_MANAGER_ARGS="${CMESH_EXTRA_MANAGER_ARGS:-}"
DATABASE_URL="${DATABASE_URL:-}"
CMESH_BIN_DIR="${CMESH_BIN_DIR:-/usr/local/bin}"
CMESH_BINARY_URL="${CMESH_BINARY_URL:-}"

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

release_download_base_url() {
  if [ "$CMESH_VERSION" = "latest" ]; then
    printf "https://github.com/NythralHome/cmesh/releases/latest/download"
  else
    printf "https://github.com/NythralHome/cmesh/releases/download/%s" "$CMESH_VERSION"
  fi
}

caddy_upstream_addr() {
  case "$CMESH_ADDR" in
    :*) printf "127.0.0.1%s" "$CMESH_ADDR" ;;
    0.0.0.0:*) printf "127.0.0.1:%s" "${CMESH_ADDR##*:}" ;;
    "[::]:"*) printf "127.0.0.1:%s" "${CMESH_ADDR##*:}" ;;
    *) printf "%s" "$CMESH_ADDR" ;;
  esac
}

manager_health_url() {
  printf "http://%s/health" "$(caddy_upstream_addr)"
}

print_install_plan() {
  asset="$(detect_asset)"
  url="$(release_download_base_url)/$asset"
  if [ -n "$CMESH_BINARY_URL" ]; then
    url="$CMESH_BINARY_URL"
  fi
  public_url="$CMESH_PUBLIC_URL"
  if [ -z "$public_url" ]; then
    public_url="-"
  fi
  caddy="$CMESH_INSTALL_CADDY"
  if [ -z "$caddy" ]; then
    caddy="-"
  fi
  domain="$CMESH_DOMAIN"
  if [ -z "$domain" ]; then
    domain="-"
  fi
  admin_email="$CMESH_ADMIN_EMAIL"
  if [ -z "$admin_email" ]; then
    admin_email="-"
  fi
  database_url="-"
  if [ -n "$DATABASE_URL" ]; then
    database_url="configured"
  fi
  join_token_status="configured"
  if [ -z "$CMESH_JOIN_TOKEN" ]; then
    join_token_status="generated_on_install"
  fi
  operator_token_status="configured"
  if [ -z "$CMESH_OPERATOR_TOKEN" ]; then
    operator_token_status="generated_on_install"
  fi

  cat <<EOF
CMesh manager install dry run
version: $CMESH_VERSION
binary_asset: $asset
binary_url: $url
bin_dir: $CMESH_BIN_DIR
addr: $CMESH_ADDR
domain: $domain
public_url: $public_url
admin_email: $admin_email
install_caddy: $caddy
caddy_upstream: $(caddy_upstream_addr)
health_url: $(manager_health_url)
state_path: $CMESH_STATE_PATH
database_url: $database_url
join_token: $join_token_status
operator_token: $operator_token_status
extra_manager_args: ${CMESH_EXTRA_MANAGER_ARGS:--}
systemd_unit: /etc/systemd/system/cmesh.service
env_file: /etc/cmesh/manager.env
EOF
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

wait_for_manager_health() {
  if ! command -v curl >/dev/null 2>&1; then
    echo "curl is not available; skipping manager HTTP health check"
    return
  fi
  health_url="$(manager_health_url)"
  deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if curl -fsS "$health_url" >/dev/null 2>&1; then
      echo "CMesh manager health check passed: $health_url"
      return
    fi
    sleep 1
  done
  echo "CMesh manager did not become healthy: $health_url" >&2
  systemctl --no-pager status cmesh.service || true
  journalctl -u cmesh.service --no-pager -n 120 || true
  exit 1
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
  reverse_proxy $(caddy_upstream_addr)
}
EOF

  caddy validate --config "$caddyfile"
  systemctl enable --now caddy.service
  systemctl reload caddy.service || systemctl restart caddy.service
}

service_action() {
  action="$1"
  case "$action" in
    status)
      if ! command -v systemctl >/dev/null 2>&1; then
        echo "systemd is required" >&2
        exit 1
      fi
      systemctl --no-pager status cmesh.service || true
      if command -v caddy >/dev/null 2>&1; then
        systemctl --no-pager status caddy.service || true
      fi
      ;;
    start)
      systemctl start cmesh.service
      systemctl --no-pager status cmesh.service || true
      ;;
    stop)
      systemctl stop cmesh.service
      echo "CMesh manager stopped"
      ;;
    restart)
      systemctl restart cmesh.service
      systemctl --no-pager status cmesh.service || true
      ;;
    uninstall)
      systemctl disable --now cmesh.service >/dev/null 2>&1 || true
      rm -f /etc/systemd/system/cmesh.service
      systemctl daemon-reload
      echo "CMesh manager service removed"
      echo "State and tokens were left in /var/lib/cmesh and /etc/cmesh"
      ;;
    *)
      echo "usage: install-manager-linux.sh [install|status|start|stop|restart|uninstall]" >&2
      exit 1
      ;;
  esac
}

if [ "$(uname -s)" != "Linux" ]; then
  echo "manager installer currently supports Linux only" >&2
  exit 1
fi

case "$ACTION" in
  status)
    service_action "$ACTION"
    exit 0
    ;;
  start|stop|restart|uninstall)
    if [ "$(id -u)" -ne 0 ]; then
      echo "manager service action requires root; rerun with sudo" >&2
      exit 1
    fi
    service_action "$ACTION"
    exit 0
    ;;
  install)
    ;;
  *)
    echo "usage: install-manager-linux.sh [install|status|start|stop|restart|uninstall]" >&2
    exit 1
    ;;
esac

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

if is_yes "$CMESH_INSTALL_DRY_RUN"; then
  print_install_plan
  exit 0
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

if [ "$(id -u)" -ne 0 ]; then
  echo "manager install requires root; rerun with sudo" >&2
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd is required" >&2
  exit 1
fi

asset="$(detect_asset)"
url="$(release_download_base_url)/$asset"
if [ -n "$CMESH_BINARY_URL" ]; then
  url="$CMESH_BINARY_URL"
fi
tmp="${TMPDIR:-/tmp}/$asset.$$"

download "$url" "$tmp"
install -m 0755 -o root -g root "$tmp" "$CMESH_BIN_DIR/cmesh"
rm -f "$tmp"

id cmesh >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/cmesh --shell /usr/sbin/nologin cmesh 2>/dev/null || useradd --system --create-home --home-dir /var/lib/cmesh --shell /sbin/nologin cmesh
install -d -m 0755 /etc/cmesh /var/lib/cmesh
chown cmesh:cmesh /var/lib/cmesh

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
ExecStart=$CMESH_BIN_DIR/cmesh manager start --addr $CMESH_ADDR $CMESH_EXTRA_MANAGER_ARGS
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/cmesh

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now cmesh.service
wait_for_manager_health

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
if is_yes "$CMESH_PRINT_SECRETS"; then
  echo "operator token: $CMESH_OPERATOR_TOKEN"
  echo "join token: $CMESH_JOIN_TOKEN"
else
  echo "operator token: stored in /etc/cmesh/manager.env"
  echo "join token: stored in /etc/cmesh/manager.env"
fi
