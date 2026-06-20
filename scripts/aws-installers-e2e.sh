#!/usr/bin/env bash
set -euo pipefail

CMESH_AWS_REGION="${CMESH_AWS_REGION:-us-east-1}"
CMESH_AWS_VPC_ID="${CMESH_AWS_VPC_ID:-vpc-009bb3d2b3eb90ba7}"
CMESH_AWS_SUBNET_ID="${CMESH_AWS_SUBNET_ID:-subnet-0cfdff3dcc5b8a13b}"
CMESH_AWS_INSTANCE_TYPE="${CMESH_AWS_INSTANCE_TYPE:-t3.small}"
CMESH_AWS_INSTANCE_COUNT="${CMESH_AWS_INSTANCE_COUNT:-3}"
CMESH_AWS_VOLUME_SIZE="${CMESH_AWS_VOLUME_SIZE:-20}"
CMESH_AWS_AMI_ID="${CMESH_AWS_AMI_ID:-}"
CMESH_AWS_SSH_USER="${CMESH_AWS_SSH_USER:-ubuntu}"
CMESH_KEEP_AWS_RESOURCES="${CMESH_KEEP_AWS_RESOURCES:-false}"
CMESH_E2E_DIR="${CMESH_E2E_DIR:-}"
CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_RUN_ID:-cmesh-installers-e2e-$(date -u +%Y%m%d%H%M%S)}"
STATE_DIR="${CMESH_E2E_DIR:-/tmp/$RUN_ID}"
KEY_NAME="$RUN_ID-key"
KEY_PATH="$STATE_DIR/$KEY_NAME.pem"
SG_ID=""
INSTANCE_IDS=()
MANAGER_PUBLIC=""
MANAGER_PRIVATE=""
WORKER1_PUBLIC=""
WORKER2_PUBLIC=""
PACKAGE_DIR=""

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

ssh_run() {
  local host="$1"
  shift
  ssh -n -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$KEY_PATH" "$CMESH_AWS_SSH_USER@$host" "$@"
}

scp_to() {
  local source="$1"
  local host="$2"
  local target="$3"
  scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$KEY_PATH" "$source" "$CMESH_AWS_SSH_USER@$host:$target" >/dev/null
}

json_string_array() {
  if [[ $# -eq 0 ]]; then
    printf '[]'
    return
  fi
  local first=true value
  printf '['
  for value in "$@"; do
    if [[ "$first" == "true" ]]; then
      first=false
    else
      printf ','
    fi
    printf '"%s"' "${value//\"/\\\"}"
  done
  printf ']'
}

collect_remote_logs() {
  [[ -f "$KEY_PATH" ]] || return 0
  mkdir -p "$STATE_DIR/remote-logs"
  local role host
  for role in manager worker1 worker2; do
    case "$role" in
      manager) host="$MANAGER_PUBLIC" ;;
      worker1) host="$WORKER1_PUBLIC" ;;
      worker2) host="$WORKER2_PUBLIC" ;;
    esac
    [[ -n "$host" && "$host" != "null" ]] || continue
    ssh_run "$host" "journalctl -u cmesh.service -u cmesh-worker.service --no-pager -n 300 > /tmp/cmesh-journal.log 2>&1 || true" >/dev/null 2>&1
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$KEY_PATH" \
      "$CMESH_AWS_SSH_USER@$host:/tmp/cmesh-journal.log" \
      "$STATE_DIR/remote-logs/$role-journal.log" >/dev/null 2>&1 || true
  done
}

cleanup() {
  local rc=$?
  mkdir -p "$STATE_DIR"
  echo "$rc" > "$STATE_DIR/exit_code"
  if [[ "$CMESH_KEEP_AWS_RESOURCES" == "true" ]]; then
    echo "keeping AWS resources because CMESH_KEEP_AWS_RESOURCES=true"
    echo "state dir: $STATE_DIR"
    exit "$rc"
  fi
  set +e
  collect_remote_logs
  {
    echo "{"
    printf '  "run_id": "%s",\n' "$RUN_ID"
    printf '  "region": "%s",\n' "$CMESH_AWS_REGION"
    printf '  "exit_code": %s,\n' "$rc"
    printf '  "instances": %s,\n' "$(json_string_array "${INSTANCE_IDS[@]}")"
    printf '  "security_group": "%s",\n' "$SG_ID"
    printf '  "keep_resources": false\n'
    echo "}"
  } > "$STATE_DIR/cleanup-started.json"
  if [[ ${#INSTANCE_IDS[@]} -gt 0 ]]; then
    echo "terminating instances: ${INSTANCE_IDS[*]}"
    aws ec2 terminate-instances --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}" >/dev/null
    aws ec2 wait instance-terminated --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}"
    aws ec2 describe-instances --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}" \
      --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name}' --output json \
      > "$STATE_DIR/cleanup-instances.json"
  fi
  if [[ -n "$SG_ID" ]]; then
    aws ec2 delete-security-group --region "$CMESH_AWS_REGION" --group-id "$SG_ID" >/dev/null
  fi
  aws ec2 delete-key-pair --region "$CMESH_AWS_REGION" --key-name "$KEY_NAME" >/dev/null 2>&1
  rm -f "$KEY_PATH"
  set -e
  exit "$rc"
}
trap cleanup EXIT

latest_ubuntu_ami() {
  aws ssm get-parameter \
    --region "$CMESH_AWS_REGION" \
    --name /aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id \
    --query 'Parameter.Value' \
    --output text
}

wait_for_ssh() {
  local host="$1"
  local i
  for i in $(seq 1 90); do
    if ssh_run "$host" "echo ok" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  fail "SSH did not become ready for $host"
}

wait_for_workers_online() {
  local i summary
  for i in $(seq 1 90); do
    summary="$(curl -fsS -H "Authorization: Bearer $operator_token" "http://$MANAGER_PUBLIC:18080/v1/cluster")"
    printf '%s' "$summary" > "$STATE_DIR/cluster.json"
    if [[ "$(jq -r '.workers_online // 0' "$STATE_DIR/cluster.json")" -ge 2 ]]; then
      return 0
    fi
    sleep 2
  done
  jq . "$STATE_DIR/cluster.json" >&2 || true
  fail "workers did not come online"
}

prepare_host() {
  local host="$1"
  wait_for_ssh "$host"
  ssh_run "$host" "sudo apt-get update >/dev/null && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates >/dev/null"
  ssh_run "$host" "sudo mkdir -p /opt/cmesh && sudo chown -R $CMESH_AWS_SSH_USER:$CMESH_AWS_SSH_USER /opt/cmesh"
  if [[ -n "$PACKAGE_DIR" ]]; then
    scp_to "$PACKAGE_DIR/cmesh-linux-amd64" "$host" /opt/cmesh/cmesh-linux-amd64
    scp_to "$PACKAGE_DIR/install-manager-linux.sh" "$host" /opt/cmesh/install-manager-linux.sh
    scp_to "$PACKAGE_DIR/install-worker.sh" "$host" /opt/cmesh/install-worker.sh
    scp_to "$PACKAGE_DIR/manifest.json" "$host" /opt/cmesh/manifest.json
    scp_to "$PACKAGE_DIR/checksums.txt" "$host" /opt/cmesh/checksums.txt
  else
    scp_to "$ROOT_DIR/dist/cmesh-linux-amd64" "$host" /opt/cmesh/cmesh-linux-amd64
    scp_to "$ROOT_DIR/scripts/install-manager-linux.sh" "$host" /opt/cmesh/install-manager-linux.sh
    scp_to "$ROOT_DIR/scripts/install-worker.sh" "$host" /opt/cmesh/install-worker.sh
  fi
  ssh_run "$host" "chmod +x /opt/cmesh/cmesh-linux-amd64 /opt/cmesh/install-manager-linux.sh /opt/cmesh/install-worker.sh"
}

main() {
  need aws
  need curl
  need jq
  need ssh
  need scp
  need openssl

  if [[ "$CMESH_AWS_INSTANCE_COUNT" -ne 3 ]]; then
    fail "CMESH_AWS_INSTANCE_COUNT must be exactly 3 for this E2E"
  fi

  mkdir -p "$STATE_DIR" "$ROOT_DIR/dist"
  aws sts get-caller-identity --output json > "$STATE_DIR/aws-identity.json"

  if [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]]; then
    PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
    [[ -f "$PACKAGE_DIR/cmesh-linux-amd64" ]] || fail "package missing cmesh-linux-amd64: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/install-manager-linux.sh" ]] || fail "package missing install-manager-linux.sh: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/install-worker.sh" ]] || fail "package missing install-worker.sh: $PACKAGE_DIR"
    (cd "$PACKAGE_DIR" && shasum -a 256 -c checksums.txt >/dev/null)
    jq -e '.kind == "cmesh.linux.production.release.v1"' "$PACKAGE_DIR/manifest.json" >/dev/null
    jq -n --arg package_dir "$PACKAGE_DIR" '{package_dir:$package_dir, source:"linux-production-package"}' > "$STATE_DIR/package.json"
  else
    need go
    echo "building cmesh linux/amd64 binary"
    (
      cd "$ROOT_DIR"
      GOOS=linux GOARCH=amd64 go build \
        -ldflags "-X github.com/cmesh/cmesh/internal/version.Version=installers-e2e -X github.com/cmesh/cmesh/internal/version.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X github.com/cmesh/cmesh/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o dist/cmesh-linux-amd64 ./cmd/cmesh
    )
  fi

  local ami_id my_ip
  ami_id="$CMESH_AWS_AMI_ID"
  if [[ -z "$ami_id" ]]; then
    ami_id="$(latest_ubuntu_ami)"
  fi
  my_ip="$(curl -fsS https://checkip.amazonaws.com | tr -d '\n')"

  aws ec2 create-key-pair --region "$CMESH_AWS_REGION" --key-name "$KEY_NAME" --query 'KeyMaterial' --output text > "$KEY_PATH"
  chmod 600 "$KEY_PATH"

  SG_ID="$(aws ec2 create-security-group --region "$CMESH_AWS_REGION" --group-name "$RUN_ID-sg" --description "CMesh installer E2E $RUN_ID" --vpc-id "$CMESH_AWS_VPC_ID" --query 'GroupId' --output text)"
  aws ec2 create-tags --region "$CMESH_AWS_REGION" --resources "$SG_ID" --tags Key=Name,Value="$RUN_ID-sg" Key=CMeshRun,Value="$RUN_ID"
  aws ec2 authorize-security-group-ingress --region "$CMESH_AWS_REGION" --group-id "$SG_ID" --ip-permissions \
    "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=$my_ip/32,Description=ssh-local}]" \
    "IpProtocol=tcp,FromPort=18080,ToPort=18080,IpRanges=[{CidrIp=$my_ip/32,Description=manager-local}]" \
    "IpProtocol=tcp,FromPort=18080,ToPort=18080,UserIdGroupPairs=[{GroupId=$SG_ID,Description=manager-private}]" >/dev/null

  read -r -a INSTANCE_IDS <<< "$(aws ec2 run-instances --region "$CMESH_AWS_REGION" \
    --image-id "$ami_id" \
    --instance-type "$CMESH_AWS_INSTANCE_TYPE" \
    --count "$CMESH_AWS_INSTANCE_COUNT" \
    --key-name "$KEY_NAME" \
    --network-interfaces "DeviceIndex=0,SubnetId=$CMESH_AWS_SUBNET_ID,Groups=$SG_ID,AssociatePublicIpAddress=true" \
    --block-device-mappings "[{\"DeviceName\":\"/dev/sda1\",\"Ebs\":{\"VolumeSize\":$CMESH_AWS_VOLUME_SIZE,\"VolumeType\":\"gp3\",\"DeleteOnTermination\":true}}]" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$RUN_ID},{Key=CMeshRun,Value=$RUN_ID}]" "ResourceType=volume,Tags=[{Key=Name,Value=$RUN_ID},{Key=CMeshRun,Value=$RUN_ID}]" \
    --query 'Instances[].InstanceId' --output text)"
  aws ec2 wait instance-running --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}"
  aws ec2 describe-instances --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}" --query 'Reservations[].Instances[].{ID:InstanceId,Public:PublicIpAddress,Private:PrivateIpAddress,State:State.Name}' --output json > "$STATE_DIR/instances.json"

  MANAGER_PUBLIC="$(jq -r '.[0].Public' "$STATE_DIR/instances.json")"
  MANAGER_PRIVATE="$(jq -r '.[0].Private' "$STATE_DIR/instances.json")"
  WORKER1_PUBLIC="$(jq -r '.[1].Public' "$STATE_DIR/instances.json")"
  WORKER2_PUBLIC="$(jq -r '.[2].Public' "$STATE_DIR/instances.json")"

  for host in "$MANAGER_PUBLIC" "$WORKER1_PUBLIC" "$WORKER2_PUBLIC"; do
    prepare_host "$host"
  done

  join_token="$(openssl rand -hex 32)"
  operator_token="$(openssl rand -hex 32)"
  echo "$join_token" > "$STATE_DIR/join-token.txt"

  echo "installing manager through installer"
  ssh_run "$MANAGER_PUBLIC" "sudo env CMESH_BINARY_URL=file:///opt/cmesh/cmesh-linux-amd64 CMESH_NONINTERACTIVE=true CMESH_ADDR=0.0.0.0:18080 CMESH_PUBLIC_URL=http://$MANAGER_PUBLIC:18080 CMESH_JOIN_TOKEN=$join_token CMESH_OPERATOR_TOKEN=$operator_token /opt/cmesh/install-manager-linux.sh > /opt/cmesh/install-manager.log 2>&1"
  curl -fsS "http://$MANAGER_PUBLIC:18080/health" > "$STATE_DIR/manager-health.json"

  echo "installing workers through installer"
  for host in "$WORKER1_PUBLIC" "$WORKER2_PUBLIC"; do
    ssh_run "$host" "sudo env CMESH_BINARY_URL=file:///opt/cmesh/cmesh-linux-amd64 CMESH_NONINTERACTIVE=true CMESH_MANAGER_URL=http://$MANAGER_PRIVATE:18080 CMESH_JOIN_TOKEN=$join_token CMESH_INSTALL_SERVICE=true CMESH_LLAMA_CPP_RUNTIME_AUTO=false CMESH_BENCHMARK=false CMESH_CPU=2 CMESH_MEMORY_GB=2 CMESH_DISK_GB=10 /opt/cmesh/install-worker.sh > /opt/cmesh/install-worker.log 2>&1"
  done

  wait_for_workers_online
  ssh_run "$MANAGER_PUBLIC" "systemctl is-active cmesh.service" > "$STATE_DIR/manager-service.txt"
  ssh_run "$WORKER1_PUBLIC" "systemctl is-active cmesh-worker.service" > "$STATE_DIR/worker1-service.txt"
  ssh_run "$WORKER2_PUBLIC" "systemctl is-active cmesh-worker.service" > "$STATE_DIR/worker2-service.txt"

  jq -n \
    --slurpfile cluster "$STATE_DIR/cluster.json" \
    --arg manager "$MANAGER_PUBLIC" \
    --arg package_dir "$PACKAGE_DIR" \
    '{status:"succeeded", manager_public:$manager, workers_online:$cluster[0].workers_online, workers_total:$cluster[0].workers_total, package_dir:$package_dir}' \
    > "$STATE_DIR/summary.json"

  echo "PASS: AWS installer E2E succeeded"
  echo "evidence: $STATE_DIR"
}

main "$@"
