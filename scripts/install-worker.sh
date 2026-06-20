#!/usr/bin/env sh
set -eu

ACTION="${1:-install}"
CMESH_VERSION="${CMESH_VERSION:-latest}"
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
CMESH_INSTALL_DRY_RUN="${CMESH_INSTALL_DRY_RUN:-false}"
CMESH_NONINTERACTIVE="${CMESH_NONINTERACTIVE:-false}"
CMESH_CACHE_DIR="${CMESH_CACHE_DIR:-$HOME/.cache/cmesh}"
CMESH_BINARY_URL="${CMESH_BINARY_URL:-}"
CMESH_LLAMA_CPP_RUNTIME_URL="${CMESH_LLAMA_CPP_RUNTIME_URL:-}"
CMESH_LLAMA_CPP_RUNTIME_NAME="${CMESH_LLAMA_CPP_RUNTIME_NAME:-}"
CMESH_LLAMA_CPP_RUNTIME_VERSION="${CMESH_LLAMA_CPP_RUNTIME_VERSION:-}"
CMESH_LLAMA_CPP_RUNTIME_SHA256="${CMESH_LLAMA_CPP_RUNTIME_SHA256:-}"
CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM="${CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM:-true}"
CMESH_LLAMA_CPP_PREFER_CACHE="${CMESH_LLAMA_CPP_PREFER_CACHE:-false}"
CMESH_LLAMA_CPP_RUNTIME_AUTO="${CMESH_LLAMA_CPP_RUNTIME_AUTO:-true}"
CMESH_STAGE_RUNNER_BIN="${CMESH_STAGE_RUNNER_BIN:-}"
CMESH_MODEL_ID="${CMESH_MODEL_ID:-}"
CMESH_MODEL_PATH="${CMESH_MODEL_PATH:-}"
CMESH_MODEL_URL="${CMESH_MODEL_URL:-}"
CMESH_MODEL_FILE="${CMESH_MODEL_FILE:-}"
CMESH_MODEL_LAYERS="${CMESH_MODEL_LAYERS:-0}"
CMESH_MODEL_RUNTIME="${CMESH_MODEL_RUNTIME:-llama.cpp}"
CMESH_RPC="${CMESH_RPC:-false}"
CMESH_RPC_HOST="${CMESH_RPC_HOST:-0.0.0.0}"
CMESH_RPC_ADVERTISE_HOST="${CMESH_RPC_ADVERTISE_HOST:-}"
CMESH_RPC_PORT="${CMESH_RPC_PORT:-50052}"
CMESH_RPC_CACHE="${CMESH_RPC_CACHE:-true}"
CMESH_STAGE_DAEMON="${CMESH_STAGE_DAEMON:-auto}"
CMESH_STAGE_DAEMON_HOST="${CMESH_STAGE_DAEMON_HOST:-127.0.0.1}"
CMESH_STAGE_DAEMON_PORT="${CMESH_STAGE_DAEMON_PORT:-19781}"
CMESH_STAGE_DAEMON_URL="${CMESH_STAGE_DAEMON_URL:-}"
CMESH_STAGE_DAEMON_SESSION_DIR="${CMESH_STAGE_DAEMON_SESSION_DIR:-}"
CMESH_STAGE_DAEMON_BACKEND="${CMESH_STAGE_DAEMON_BACKEND:-mock}"

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

release_download_base_url() {
  if [ "$CMESH_VERSION" = "latest" ]; then
    printf "https://github.com/NythralHome/cmesh/releases/latest/download"
  else
    printf "https://github.com/NythralHome/cmesh/releases/download/%s" "$CMESH_VERSION"
  fi
}

detect_llamacpp_runtime_asset() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os:$arch" in
    linux:x86_64|linux:amd64)
      printf "llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz"
      ;;
    *)
      return 1
      ;;
  esac
}

configure_explicit_llamacpp_runtime() {
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_URL" ]; then
    return
  fi
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_NAME" ]; then
    runtime_asset="${CMESH_LLAMA_CPP_RUNTIME_URL%%\?*}"
    runtime_asset="${runtime_asset##*/}"
    CMESH_LLAMA_CPP_RUNTIME_NAME="$runtime_asset"
  fi
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_VERSION" ]; then
    case "$CMESH_LLAMA_CPP_RUNTIME_NAME" in
      *.tar.gz) CMESH_LLAMA_CPP_RUNTIME_VERSION="${CMESH_LLAMA_CPP_RUNTIME_NAME%.tar.gz}" ;;
      *) CMESH_LLAMA_CPP_RUNTIME_VERSION="$CMESH_LLAMA_CPP_RUNTIME_NAME" ;;
    esac
  fi
}

configure_default_llamacpp_runtime() {
  if ! is_yes "$CMESH_LLAMA_CPP_RUNTIME_AUTO"; then
    return
  fi
  if [ -n "$CMESH_LLAMA_CPP_RUNTIME_URL" ]; then
    return
  fi
  runtime_asset="$(detect_llamacpp_runtime_asset || true)"
  if [ -z "$runtime_asset" ]; then
    return
  fi
  runtime_version="${runtime_asset%.tar.gz}"
  CMESH_LLAMA_CPP_RUNTIME_NAME="$runtime_asset"
  CMESH_LLAMA_CPP_RUNTIME_VERSION="$runtime_version"
  CMESH_LLAMA_CPP_RUNTIME_URL="$(release_download_base_url)/$runtime_asset"
  CMESH_LLAMA_CPP_PREFER_CACHE="true"
}

configure_service_cache_dir() {
  if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ]; then
    CMESH_CACHE_DIR="/var/lib/cmesh/cache"
  fi
}

runtime_cache_dir() {
  printf "%s/runtimes/llama.cpp/%s" "$CMESH_CACHE_DIR" "$CMESH_LLAMA_CPP_RUNTIME_VERSION"
}

configure_stage_runner_path() {
  if [ -n "$CMESH_STAGE_RUNNER_BIN" ]; then
    return
  fi
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_URL" ] || [ -z "$CMESH_LLAMA_CPP_RUNTIME_VERSION" ]; then
    return
  fi
  CMESH_STAGE_RUNNER_BIN="$(runtime_cache_dir)/bin/cmesh-stage-runner"
}

configure_stage_daemon() {
  if [ "$CMESH_STAGE_DAEMON" = "auto" ]; then
    if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ] && [ -n "$CMESH_STAGE_RUNNER_BIN" ]; then
      CMESH_STAGE_DAEMON="true"
    else
      CMESH_STAGE_DAEMON="false"
    fi
  fi
  if is_yes "$CMESH_STAGE_DAEMON"; then
    if [ -z "$CMESH_STAGE_DAEMON_URL" ]; then
      CMESH_STAGE_DAEMON_URL="http://$CMESH_STAGE_DAEMON_HOST:$CMESH_STAGE_DAEMON_PORT"
    fi
    if [ -z "$CMESH_STAGE_DAEMON_SESSION_DIR" ]; then
      if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ]; then
        CMESH_STAGE_DAEMON_SESSION_DIR="/var/lib/cmesh/stage-sessions"
      else
        CMESH_STAGE_DAEMON_SESSION_DIR="$CMESH_CACHE_DIR/stage-sessions"
      fi
    fi
  fi
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

sha256_value() {
  path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
  else
    echo "sha256sum or shasum is required for runtime verification" >&2
    exit 1
  fi
}

runtime_checksum_url() {
  runtime_url="${CMESH_LLAMA_CPP_RUNTIME_URL%%\?*}"
  printf "%s.sha256" "$runtime_url"
}

ensure_runtime_checksum() {
  if [ -n "$CMESH_LLAMA_CPP_RUNTIME_SHA256" ]; then
    return
  fi
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_URL" ]; then
    return
  fi
  checksum_tmp="${TMPDIR:-/tmp}/cmesh-runtime-sha256.$$"
  checksum_url="$(runtime_checksum_url)"
  if download "$checksum_url" "$checksum_tmp" >/dev/null 2>&1; then
    CMESH_LLAMA_CPP_RUNTIME_SHA256="$(awk 'NF {print $1; exit}' "$checksum_tmp")"
    rm -f "$checksum_tmp"
    return
  fi
  rm -f "$checksum_tmp"
  if is_yes "$CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM"; then
    echo "runtime checksum is required but could not be loaded: $checksum_url" >&2
    exit 1
  fi
}

verify_runtime_checksum() {
  archive_path="$1"
  ensure_runtime_checksum
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_SHA256" ]; then
    return
  fi
  actual="$(sha256_value "$archive_path")"
  if [ "$actual" != "$CMESH_LLAMA_CPP_RUNTIME_SHA256" ]; then
    echo "runtime checksum mismatch" >&2
    echo "expected: $CMESH_LLAMA_CPP_RUNTIME_SHA256" >&2
    echo "actual:   $actual" >&2
    exit 1
  fi
}

install_runtime_system_dependencies() {
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_URL" ]; then
    return
  fi
  if [ "$(uname -s)" != "Linux" ]; then
    return
  fi
  if command -v ldconfig >/dev/null 2>&1 && ldconfig -p 2>/dev/null | grep -F "libgomp.so.1" >/dev/null; then
    return
  fi
  if [ "$(id -u)" -ne 0 ]; then
    echo "warning: llama.cpp runtime may require libgomp.so.1; install libgomp before running stage daemon" >&2
    return
  fi
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y libgomp1
    return
  fi
  if command -v dnf >/dev/null 2>&1; then
    dnf install -y libgomp
    return
  fi
  if command -v yum >/dev/null 2>&1; then
    yum install -y libgomp
    return
  fi
  echo "warning: could not install libgomp automatically on this distribution" >&2
}

download_runtime_artifact() {
  if [ -z "$CMESH_LLAMA_CPP_RUNTIME_URL" ] || [ -z "$CMESH_LLAMA_CPP_RUNTIME_VERSION" ]; then
    return
  fi
  install_runtime_system_dependencies
  runtime_dir="$(runtime_cache_dir)"
  if [ -x "$runtime_dir/bin/llama-cli" ]; then
    if [ -z "$CMESH_STAGE_RUNNER_BIN" ] || [ -x "$CMESH_STAGE_RUNNER_BIN" ]; then
      echo "runtime already exists: $runtime_dir"
      return
    fi
  fi
  archive_name="$CMESH_LLAMA_CPP_RUNTIME_NAME"
  if [ -z "$archive_name" ]; then
    archive_name="${CMESH_LLAMA_CPP_RUNTIME_URL%%\?*}"
    archive_name="${archive_name##*/}"
  fi
  tmp="${TMPDIR:-/tmp}/cmesh-runtime.$$"
  rm -rf "$runtime_dir"
  mkdir -p "$runtime_dir"
  download "$CMESH_LLAMA_CPP_RUNTIME_URL" "$tmp"
  verify_runtime_checksum "$tmp"
  case "$archive_name" in
    *.tar.gz) tar -C "$runtime_dir" -xzf "$tmp" ;;
    *) echo "unsupported runtime archive: $archive_name" >&2; rm -f "$tmp"; exit 1 ;;
  esac
  rm -f "$tmp"
  chmod +x "$runtime_dir/bin/llama-cli" 2>/dev/null || true
  chmod +x "$runtime_dir/bin/cmesh-stage-runner" 2>/dev/null || true
  echo "downloaded runtime artifact: $runtime_dir"
}

wait_for_worker_service() {
  deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if systemctl is-active --quiet cmesh-worker.service; then
      echo "CMesh worker service is active"
      return
    fi
    sleep 1
  done
  echo "CMesh worker service did not become active" >&2
  systemctl --no-pager status cmesh-worker.service || true
  exit 1
}

wait_for_stage_daemon_service() {
  if ! is_yes "$CMESH_STAGE_DAEMON"; then
    return
  fi
  deadline=$(( $(date +%s) + 30 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if systemctl is-active --quiet cmesh-stage-daemon.service; then
      echo "CMesh stage daemon service is active"
      return
    fi
    sleep 1
  done
  echo "CMesh stage daemon service did not become active" >&2
  systemctl --no-pager status cmesh-stage-daemon.service || true
  exit 1
}

install_binary() {
  asset="$(detect_asset)"
  url="$(release_download_base_url)/$asset"
  if [ -n "$CMESH_BINARY_URL" ]; then
    url="$CMESH_BINARY_URL"
  fi
  tmp="${TMPDIR:-/tmp}/$asset.$$"

  mkdir -p "$CMESH_BIN_DIR"
  download "$url" "$tmp"
  chmod +x "$tmp"
  mv "$tmp" "$CMESH_BIN_DIR/cmesh"

  echo "installed $("$CMESH_BIN_DIR/cmesh" version)"
}

configure_model_download_path() {
  if [ -z "$CMESH_MODEL_URL" ] || [ -n "$CMESH_MODEL_PATH" ]; then
    return
  fi
  if [ -z "$CMESH_MODEL_ID" ]; then
    echo "CMESH_MODEL_ID is required when CMESH_MODEL_URL is set" >&2
    exit 1
  fi
  file="$CMESH_MODEL_FILE"
  if [ -z "$file" ]; then
    file="${CMESH_MODEL_URL%%\?*}"
    file="${file##*/}"
  fi
  if [ -z "$file" ]; then
    file="$CMESH_MODEL_ID.gguf"
  fi
  CMESH_MODEL_FILE="$file"
  if [ "$CMESH_INSTALL_SERVICE" = "true" ] && [ "$(uname -s)" = "Linux" ]; then
    CMESH_MODEL_PATH="/var/lib/cmesh/models/$file"
  else
    CMESH_MODEL_PATH="$CMESH_CACHE_DIR/models/$CMESH_MODEL_ID/$file"
  fi
}

download_model_artifact() {
  if [ -z "$CMESH_MODEL_URL" ]; then
    return
  fi
  configure_model_download_path
  if [ -f "$CMESH_MODEL_PATH" ]; then
    echo "model already exists: $CMESH_MODEL_PATH"
    return
  fi
  mkdir -p "$(dirname "$CMESH_MODEL_PATH")"
  tmp="$CMESH_MODEL_PATH.tmp.$$"
  download "$CMESH_MODEL_URL" "$tmp"
  mv "$tmp" "$CMESH_MODEL_PATH"
  echo "downloaded model artifact: $CMESH_MODEL_PATH"
}

print_install_plan() {
  configure_model_download_path
  asset="$(detect_asset)"
  binary_url="$(release_download_base_url)/$asset"
  if [ -n "$CMESH_BINARY_URL" ]; then
    binary_url="$CMESH_BINARY_URL"
  fi
  runtime_url="$CMESH_LLAMA_CPP_RUNTIME_URL"
  if [ -z "$runtime_url" ]; then
    runtime_url="-"
  fi
  runtime_name="$CMESH_LLAMA_CPP_RUNTIME_NAME"
  if [ -z "$runtime_name" ]; then
    runtime_name="-"
  fi
  runtime_version="$CMESH_LLAMA_CPP_RUNTIME_VERSION"
  if [ -z "$runtime_version" ]; then
    runtime_version="-"
  fi
  runtime_system_dependencies="-"
  if [ -n "$CMESH_LLAMA_CPP_RUNTIME_URL" ] && [ "$(uname -s)" = "Linux" ]; then
    runtime_system_dependencies="libgomp1"
  fi
  runtime_sha256="$CMESH_LLAMA_CPP_RUNTIME_SHA256"
  if [ -z "$runtime_sha256" ]; then
    if is_yes "$CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM"; then
      runtime_sha256="auto_required"
    else
      runtime_sha256="-"
    fi
  else
    runtime_sha256="configured"
  fi
  rpc_flags="$(rpc_args)"
  if [ -z "$rpc_flags" ]; then
    rpc_flags="-"
  fi
  benchmark_flags="$(benchmark_arg)"
  if [ -z "$benchmark_flags" ]; then
    benchmark_flags="-"
  fi
  model_flags="$(model_args)"
  if [ -z "$model_flags" ]; then
    model_flags="-"
  fi
  stage_daemon_flags="$(stage_daemon_args)"
  if [ -z "$stage_daemon_flags" ]; then
    stage_daemon_flags="-"
  fi
  stage_runner_bin="$CMESH_STAGE_RUNNER_BIN"
  if [ -z "$stage_runner_bin" ]; then
    stage_runner_bin="-"
  fi
  stage_daemon_service_command="-"
  if is_yes "$CMESH_STAGE_DAEMON"; then
    stage_daemon_service_command="$CMESH_BIN_DIR/cmesh stage-runner daemon --addr $CMESH_STAGE_DAEMON_HOST:$CMESH_STAGE_DAEMON_PORT --session-dir $CMESH_STAGE_DAEMON_SESSION_DIR --backend $CMESH_STAGE_DAEMON_BACKEND --runner-bin $CMESH_STAGE_RUNNER_BIN"
  fi
  model_url="$CMESH_MODEL_URL"
  if [ -z "$model_url" ]; then
    model_url="-"
  fi
  model_path="$CMESH_MODEL_PATH"
  if [ -z "$model_path" ]; then
    model_path="-"
  fi

  cat <<EOF
CMesh worker install dry run
version: $CMESH_VERSION
binary_asset: $asset
binary_url: $binary_url
bin_dir: $CMESH_BIN_DIR
manager_url: $CMESH_MANAGER_URL
node_name: $CMESH_NODE_NAME
cpu: $CMESH_CPU
memory_gb: $CMESH_MEMORY_GB
disk_gb: $CMESH_DISK_GB
gpu: $CMESH_GPU
vram_gb: $CMESH_VRAM_GB
install_service: $CMESH_INSTALL_SERVICE
service_active_check: 30s
cache_dir: $CMESH_CACHE_DIR
runtime_auto: $CMESH_LLAMA_CPP_RUNTIME_AUTO
runtime_url: $runtime_url
runtime_name: $runtime_name
runtime_version: $runtime_version
runtime_sha256: $runtime_sha256
runtime_require_checksum: $CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM
runtime_system_dependencies: $runtime_system_dependencies
runtime_prefer_cache: $CMESH_LLAMA_CPP_PREFER_CACHE
stage_runner_bin: $stage_runner_bin
stage_daemon: $CMESH_STAGE_DAEMON
stage_daemon_backend: $CMESH_STAGE_DAEMON_BACKEND
stage_daemon_url: ${CMESH_STAGE_DAEMON_URL:-"-"}
stage_daemon_session_dir: ${CMESH_STAGE_DAEMON_SESSION_DIR:-"-"}
stage_daemon_session_owner: cmesh:cmesh
stage_daemon_service_command: $stage_daemon_service_command
model_url: $model_url
model_path: $model_path
model_flags: $model_flags
rpc_flags: $rpc_flags
stage_daemon_flags: $stage_daemon_flags
benchmark_flags: $benchmark_flags
EOF
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
  prompt_default CMESH_RPC "Start llama.cpp RPC backend" "$CMESH_RPC"
  if is_yes "$CMESH_RPC"; then
    prompt_default CMESH_RPC_HOST "RPC bind host" "$CMESH_RPC_HOST"
    prompt_default CMESH_RPC_ADVERTISE_HOST "RPC advertise host, blank means auto-detect" "$CMESH_RPC_ADVERTISE_HOST"
    prompt_default CMESH_RPC_PORT "RPC port" "$CMESH_RPC_PORT"
    prompt_default CMESH_RPC_CACHE "Enable RPC local cache" "$CMESH_RPC_CACHE"
  fi
  prompt_default CMESH_STAGE_DAEMON "Start local CMesh stage daemon for resident stage sessions" "$CMESH_STAGE_DAEMON"
  if is_yes "$CMESH_STAGE_DAEMON"; then
    prompt_default CMESH_STAGE_DAEMON_HOST "Stage daemon bind host" "$CMESH_STAGE_DAEMON_HOST"
    prompt_default CMESH_STAGE_DAEMON_PORT "Stage daemon port" "$CMESH_STAGE_DAEMON_PORT"
    prompt_default CMESH_STAGE_DAEMON_BACKEND "Stage daemon backend, mock or llama.cpp-resident" "$CMESH_STAGE_DAEMON_BACKEND"
  fi
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

rpc_args() {
  if is_yes "$CMESH_RPC"; then
    printf "%s" "--rpc --rpc-host $CMESH_RPC_HOST --rpc-port $CMESH_RPC_PORT --rpc-cache=$CMESH_RPC_CACHE"
    if [ -n "$CMESH_RPC_ADVERTISE_HOST" ]; then
      printf " --rpc-advertise-host %s" "$CMESH_RPC_ADVERTISE_HOST"
    fi
  fi
}

model_args() {
  if [ -n "$CMESH_MODEL_ID" ]; then
    printf "%s" "--model-id $CMESH_MODEL_ID --runtime $CMESH_MODEL_RUNTIME"
    if [ -n "$CMESH_MODEL_PATH" ]; then
      printf " --model-path %s" "$CMESH_MODEL_PATH"
    fi
    if [ "$CMESH_MODEL_LAYERS" != "0" ]; then
      printf " --model-layers %s" "$CMESH_MODEL_LAYERS"
    fi
  fi
}

stage_daemon_args() {
  if is_yes "$CMESH_STAGE_DAEMON" && [ -n "$CMESH_STAGE_DAEMON_URL" ]; then
    printf "%s" "--stage-daemon-url $CMESH_STAGE_DAEMON_URL"
  fi
}

run_foreground() {
  export CMESH_LLAMA_CPP_RUNTIME_URL
  export CMESH_LLAMA_CPP_RUNTIME_NAME
  export CMESH_LLAMA_CPP_RUNTIME_VERSION
  export CMESH_LLAMA_CPP_RUNTIME_SHA256
  export CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM
  export CMESH_LLAMA_CPP_PREFER_CACHE
  export CMESH_LLAMA_CPP_RUNTIME_AUTO
  export CMESH_STAGE_RUNNER_BIN
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
    $(model_args) \
    $(stage_daemon_args) \
    $(rpc_args) \
    $(benchmark_arg)
}

install_stage_daemon_systemd_service() {
  if ! is_yes "$CMESH_STAGE_DAEMON"; then
    rm -f /etc/systemd/system/cmesh-stage-daemon.service
    return
  fi
  if [ -z "$CMESH_STAGE_RUNNER_BIN" ]; then
    echo "CMESH_STAGE_RUNNER_BIN is required when CMESH_STAGE_DAEMON=true" >&2
    exit 1
  fi
  install -d -m 0755 "$CMESH_STAGE_DAEMON_SESSION_DIR"
  chown cmesh:cmesh "$CMESH_STAGE_DAEMON_SESSION_DIR"
  cat > /etc/systemd/system/cmesh-stage-daemon.service <<EOF
[Unit]
Description=CMesh Stage Daemon
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=cmesh
Group=cmesh
WorkingDirectory=/var/lib/cmesh
EnvironmentFile=/etc/cmesh/worker.env
ExecStart=/bin/sh -c 'exec $CMESH_BIN_DIR/cmesh stage-runner daemon --addr "$CMESH_STAGE_DAEMON_HOST:$CMESH_STAGE_DAEMON_PORT" --session-dir "$CMESH_STAGE_DAEMON_SESSION_DIR" --backend "$CMESH_STAGE_DAEMON_BACKEND" --runner-bin "$CMESH_STAGE_RUNNER_BIN"'
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

  id cmesh >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/cmesh --shell /usr/sbin/nologin cmesh 2>/dev/null || useradd --system --create-home --home-dir /var/lib/cmesh --shell /sbin/nologin cmesh
  install -d -m 0755 /etc/cmesh /var/lib/cmesh
  chown -R cmesh:cmesh /var/lib/cmesh
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
CMESH_LLAMA_CPP_RUNTIME_URL="$CMESH_LLAMA_CPP_RUNTIME_URL"
CMESH_LLAMA_CPP_RUNTIME_NAME="$CMESH_LLAMA_CPP_RUNTIME_NAME"
CMESH_LLAMA_CPP_RUNTIME_VERSION="$CMESH_LLAMA_CPP_RUNTIME_VERSION"
CMESH_LLAMA_CPP_RUNTIME_SHA256="$CMESH_LLAMA_CPP_RUNTIME_SHA256"
CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM="$CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM"
CMESH_LLAMA_CPP_PREFER_CACHE="$CMESH_LLAMA_CPP_PREFER_CACHE"
CMESH_LLAMA_CPP_RUNTIME_AUTO="$CMESH_LLAMA_CPP_RUNTIME_AUTO"
CMESH_STAGE_RUNNER_BIN="$CMESH_STAGE_RUNNER_BIN"
CMESH_MODEL_ID="$CMESH_MODEL_ID"
CMESH_MODEL_PATH="$CMESH_MODEL_PATH"
CMESH_MODEL_LAYERS="$CMESH_MODEL_LAYERS"
CMESH_MODEL_RUNTIME="$CMESH_MODEL_RUNTIME"
CMESH_RPC="$CMESH_RPC"
CMESH_RPC_HOST="$CMESH_RPC_HOST"
CMESH_RPC_ADVERTISE_HOST="$CMESH_RPC_ADVERTISE_HOST"
CMESH_RPC_PORT="$CMESH_RPC_PORT"
CMESH_RPC_CACHE="$CMESH_RPC_CACHE"
CMESH_STAGE_DAEMON="$CMESH_STAGE_DAEMON"
CMESH_STAGE_DAEMON_HOST="$CMESH_STAGE_DAEMON_HOST"
CMESH_STAGE_DAEMON_PORT="$CMESH_STAGE_DAEMON_PORT"
CMESH_STAGE_DAEMON_URL="$CMESH_STAGE_DAEMON_URL"
CMESH_STAGE_DAEMON_SESSION_DIR="$CMESH_STAGE_DAEMON_SESSION_DIR"
CMESH_STAGE_DAEMON_BACKEND="$CMESH_STAGE_DAEMON_BACKEND"
EOF
  chmod 600 /etc/cmesh/worker.env

  install_stage_daemon_systemd_service

  cat > /etc/systemd/system/cmesh-worker.service <<EOF
[Unit]
Description=CMesh Worker
After=network-online.target
Wants=network-online.target
After=cmesh-stage-daemon.service

[Service]
Type=simple
User=cmesh
Group=cmesh
WorkingDirectory=/var/lib/cmesh
EnvironmentFile=/etc/cmesh/worker.env
ExecStart=/bin/sh -c 'benchmark=""; case "\$CMESH_BENCHMARK" in true|TRUE|1|yes|YES|y|Y) benchmark="--benchmark" ;; esac; rpc=""; case "\$CMESH_RPC" in true|TRUE|1|yes|YES|y|Y) rpc="--rpc --rpc-host \$CMESH_RPC_HOST --rpc-port \$CMESH_RPC_PORT --rpc-cache=\$CMESH_RPC_CACHE"; if [ -n "\$CMESH_RPC_ADVERTISE_HOST" ]; then rpc="\$rpc --rpc-advertise-host \$CMESH_RPC_ADVERTISE_HOST"; fi ;; esac; stage_daemon=""; case "\$CMESH_STAGE_DAEMON" in true|TRUE|1|yes|YES|y|Y) if [ -n "\$CMESH_STAGE_DAEMON_URL" ]; then stage_daemon="--stage-daemon-url \$CMESH_STAGE_DAEMON_URL"; fi ;; esac; model=""; if [ -n "\$CMESH_MODEL_ID" ]; then model="--model-id \$CMESH_MODEL_ID --runtime \$CMESH_MODEL_RUNTIME"; if [ -n "\$CMESH_MODEL_PATH" ]; then model="\$model --model-path \$CMESH_MODEL_PATH"; fi; if [ -n "\$CMESH_MODEL_LAYERS" ] && [ "\$CMESH_MODEL_LAYERS" != "0" ]; then model="\$model --model-layers \$CMESH_MODEL_LAYERS"; fi; fi; exec $CMESH_BIN_DIR/cmesh worker run --manager "\$CMESH_MANAGER_URL" --token "\$CMESH_JOIN_TOKEN" --name "\$CMESH_NODE_NAME" --cpu "\$CMESH_CPU" --memory-gb "\$CMESH_MEMORY_GB" --disk-gb "\$CMESH_DISK_GB" --vram-gb "\$CMESH_VRAM_GB" --gpu="\$CMESH_GPU" --cache-dir "\$CMESH_CACHE_DIR" \$model \$stage_daemon \$rpc \$benchmark'
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
  if is_yes "$CMESH_STAGE_DAEMON"; then
    systemctl enable --now cmesh-stage-daemon.service
    wait_for_stage_daemon_service
  fi
  systemctl enable --now cmesh-worker.service
  wait_for_worker_service
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
  model_args_xml=""
  if [ -n "$CMESH_MODEL_ID" ]; then
    model_args_xml="
    <string>--model-id</string><string>$(xml_escape "$CMESH_MODEL_ID")</string>
    <string>--runtime</string><string>$(xml_escape "$CMESH_MODEL_RUNTIME")</string>"
    if [ -n "$CMESH_MODEL_PATH" ]; then
      model_args_xml="$model_args_xml
    <string>--model-path</string><string>$(xml_escape "$CMESH_MODEL_PATH")</string>"
    fi
    if [ "$CMESH_MODEL_LAYERS" != "0" ]; then
      model_args_xml="$model_args_xml
    <string>--model-layers</string><string>$(xml_escape "$CMESH_MODEL_LAYERS")</string>"
    fi
  fi

  cat > "$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.cmesh.worker</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CMESH_LLAMA_CPP_RUNTIME_URL</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_URL")</string>
    <key>CMESH_LLAMA_CPP_RUNTIME_NAME</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_NAME")</string>
    <key>CMESH_LLAMA_CPP_RUNTIME_VERSION</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_VERSION")</string>
    <key>CMESH_LLAMA_CPP_RUNTIME_SHA256</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_SHA256")</string>
    <key>CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM")</string>
    <key>CMESH_LLAMA_CPP_PREFER_CACHE</key><string>$(xml_escape "$CMESH_LLAMA_CPP_PREFER_CACHE")</string>
    <key>CMESH_LLAMA_CPP_RUNTIME_AUTO</key><string>$(xml_escape "$CMESH_LLAMA_CPP_RUNTIME_AUTO")</string>
    <key>CMESH_STAGE_RUNNER_BIN</key><string>$(xml_escape "$CMESH_STAGE_RUNNER_BIN")</string>
  </dict>
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
    $model_args_xml
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
    status)
      systemctl --no-pager status cmesh-stage-daemon.service || true
      systemctl --no-pager status cmesh-worker.service || true
      ;;
    start)
      systemctl start cmesh-stage-daemon.service >/dev/null 2>&1 || true
      systemctl start cmesh-worker.service
      systemctl --no-pager status cmesh-worker.service || true
      ;;
    restart)
      systemctl restart cmesh-stage-daemon.service >/dev/null 2>&1 || true
      systemctl restart cmesh-worker.service
      systemctl --no-pager status cmesh-worker.service || true
      ;;
    stop)
      systemctl stop cmesh-worker.service >/dev/null 2>&1 || true
      systemctl stop cmesh-stage-daemon.service >/dev/null 2>&1 || true
      echo "CMesh worker stopped"
      ;;
    uninstall)
      systemctl disable --now cmesh-worker.service >/dev/null 2>&1 || true
      systemctl disable --now cmesh-stage-daemon.service >/dev/null 2>&1 || true
      rm -f /etc/systemd/system/cmesh-worker.service
      rm -f /etc/systemd/system/cmesh-stage-daemon.service
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
    restart)
      launchctl bootout "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
      if [ -f "$plist" ]; then
        launchctl bootstrap "gui/$(id -u)" "$plist" >/dev/null 2>&1 || true
        launchctl kickstart -k "gui/$(id -u)/com.cmesh.worker"
      fi
      launchctl print "gui/$(id -u)/com.cmesh.worker" || true
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
    configure_service_cache_dir
    configure_explicit_llamacpp_runtime
    configure_default_llamacpp_runtime
    configure_stage_runner_path
    configure_stage_daemon
    configure_model_download_path
    if is_yes "$CMESH_INSTALL_DRY_RUN"; then
      print_install_plan
      exit 0
    fi
    install_binary
    download_runtime_artifact
    download_model_artifact
    if [ "$CMESH_INSTALL_SERVICE" = "true" ]; then
      case "$(uname -s)" in
        Linux) install_systemd_service ;;
        Darwin) install_launchd_service ;;
      esac
    else
      run_foreground
    fi
    ;;
  status|start|stop|restart|uninstall)
    service_action "$ACTION"
    ;;
  *)
    echo "usage: install-worker.sh [install|status|start|stop|restart|uninstall]" >&2
    exit 1
    ;;
esac
