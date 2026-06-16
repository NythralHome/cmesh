#!/usr/bin/env bash
set -euo pipefail

CMESH_GITHUB_REPO="${CMESH_GITHUB_REPO:-NythralHome/cmesh}"
CMESH_VERSION="${CMESH_VERSION:-}"
CMESH_AWS_REGION="${CMESH_AWS_REGION:-us-east-1}"
CMESH_ALPHA_INSTANCE_ID="${CMESH_ALPHA_INSTANCE_ID:-i-0e9e899a661c02505}"
CMESH_MANAGER_HEALTH_URL="${CMESH_MANAGER_HEALTH_URL:-https://alpha.cmesh.nythral.com/health}"
CMESH_BATTLESHIFT_HEALTH_URL="${CMESH_BATTLESHIFT_HEALTH_URL:-https://api.getbattleshift.com/health}"
CMESH_DRY_RUN="${CMESH_DRY_RUN:-false}"

required_assets=(
  "cmesh-linux-amd64"
  "cmesh-linux-arm64"
  "cmesh-darwin-amd64"
  "cmesh-darwin-arm64"
  "cmesh-windows-amd64.exe"
  "CMesh-Worker-Apple-Silicon.dmg"
  "CMesh-Worker-linux-amd64.tar.gz"
  "CMesh-Worker-windows-amd64.zip"
  "checksums.txt"
)

usage() {
  cat <<EOF
Usage:
  CMESH_VERSION=v0.1.0-alpha.44 scripts/deploy-alpha.sh

Environment:
  CMESH_VERSION                 Release tag to deploy. Defaults to latest local git tag.
  CMESH_GITHUB_REPO             GitHub repo. Default: $CMESH_GITHUB_REPO
  CMESH_AWS_REGION              AWS region. Default: $CMESH_AWS_REGION
  CMESH_ALPHA_INSTANCE_ID        EC2 instance id. Default: $CMESH_ALPHA_INSTANCE_ID
  CMESH_MANAGER_HEALTH_URL       Manager health URL. Default: $CMESH_MANAGER_HEALTH_URL
  CMESH_BATTLESHIFT_HEALTH_URL   Neighbor service health URL checked after deploy.
  CMESH_DRY_RUN=true             Check release assets without deploying.
EOF
}

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

latest_local_tag() {
  git describe --tags --abbrev=0 2>/dev/null || true
}

release_base_url() {
  printf "https://github.com/%s/releases/download/%s" "$CMESH_GITHUB_REPO" "$CMESH_VERSION"
}

asset_url() {
  printf "%s/%s" "$(release_base_url)" "$1"
}

check_asset() {
  local asset="$1"
  local url
  url="$(asset_url "$asset")"
  if ! curl -fsIL "$url" >/dev/null; then
    echo "missing: $asset" >&2
    return 1
  fi
  echo "ok: $asset"
}

check_required_assets() {
  echo "checking release assets for $CMESH_VERSION"
  local missing=0
  for asset in "${required_assets[@]}"; do
    if ! check_asset "$asset"; then
      missing=1
    fi
  done
  if [[ "$missing" -ne 0 ]]; then
    fail "release $CMESH_VERSION is not fully published; refusing to deploy"
  fi
}

send_deploy_command() {
  local command_id
  local binary_url
  binary_url="$(asset_url cmesh-linux-amd64)"

  local commands
  commands=$(printf '%s' "[
  \"set -eu\",
  \"curl -fsSL '$binary_url' -o /tmp/cmesh-linux-amd64\",
  \"chmod +x /tmp/cmesh-linux-amd64\",
  \"install -m 0755 /tmp/cmesh-linux-amd64 /usr/local/bin/cmesh\",
  \"rm -f /tmp/cmesh-linux-amd64\",
  \"systemctl restart cmesh.service\",
  \"sleep 2\",
  \"systemctl is-active cmesh.service\",
  \"/usr/local/bin/cmesh version\",
  \"systemctl is-active battleshift.service || true\",
  \"systemctl is-active battleshift-develop.service || true\"
]")

  command_id="$(aws ssm send-command \
    --region "$CMESH_AWS_REGION" \
    --instance-ids "$CMESH_ALPHA_INSTANCE_ID" \
    --document-name AWS-RunShellScript \
    --comment "Deploy CMesh $CMESH_VERSION" \
    --parameters "commands=$commands" \
    --query 'Command.CommandId' \
    --output text)"
  echo "$command_id"
}

wait_for_command() {
  local command_id="$1"
  local status
  while true; do
    status="$(aws ssm get-command-invocation \
      --region "$CMESH_AWS_REGION" \
      --command-id "$command_id" \
      --instance-id "$CMESH_ALPHA_INSTANCE_ID" \
      --query 'Status' \
      --output text)"
    case "$status" in
      Success|Cancelled|TimedOut|Failed|Cancelling)
        break
        ;;
    esac
    echo "ssm status: $status"
    sleep 3
  done

  aws ssm get-command-invocation \
    --region "$CMESH_AWS_REGION" \
    --command-id "$command_id" \
    --instance-id "$CMESH_ALPHA_INSTANCE_ID" \
    --query '{Status:Status,Stdout:StandardOutputContent,Stderr:StandardErrorContent}' \
    --output json

  [[ "$status" == "Success" ]] || fail "SSM deploy failed with status $status"
}

check_health() {
  local url="$1"
  [[ -z "$url" ]] && return 0
  echo "checking health: $url"
  curl -fsS "$url"
  echo
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi

  need curl
  need aws

  if [[ -z "$CMESH_VERSION" ]]; then
    CMESH_VERSION="$(latest_local_tag)"
  fi
  [[ -n "$CMESH_VERSION" ]] || fail "CMESH_VERSION is required when no local git tag is available"

  check_required_assets

  if [[ "$CMESH_DRY_RUN" == "true" ]]; then
    echo "dry run complete; release is fully published"
    exit 0
  fi

  local command_id
  command_id="$(send_deploy_command)"
  echo "ssm command: $command_id"
  wait_for_command "$command_id"
  check_health "$CMESH_MANAGER_HEALTH_URL"
  check_health "$CMESH_BATTLESHIFT_HEALTH_URL"
  echo "deployed $CMESH_VERSION"
}

main "$@"
