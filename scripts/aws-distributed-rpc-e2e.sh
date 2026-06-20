#!/usr/bin/env bash
set -euo pipefail

CMESH_AWS_REGION="${CMESH_AWS_REGION:-us-east-1}"
CMESH_AWS_VPC_ID="${CMESH_AWS_VPC_ID:-vpc-009bb3d2b3eb90ba7}"
CMESH_AWS_SUBNET_ID="${CMESH_AWS_SUBNET_ID:-subnet-0cfdff3dcc5b8a13b}"
CMESH_AWS_INSTANCE_TYPE="${CMESH_AWS_INSTANCE_TYPE:-t3.medium}"
CMESH_AWS_INSTANCE_COUNT="${CMESH_AWS_INSTANCE_COUNT:-3}"
CMESH_AWS_VOLUME_SIZE="${CMESH_AWS_VOLUME_SIZE:-40}"
CMESH_AWS_AMI_ID="${CMESH_AWS_AMI_ID:-}"
CMESH_AWS_SSH_USER="${CMESH_AWS_SSH_USER:-ubuntu}"
CMESH_LLAMA_CPP_REF="${CMESH_LLAMA_CPP_REF:-b9704}"
CMESH_RUNTIME_VERSION="${CMESH_RUNTIME_VERSION:-llama.cpp-${CMESH_LLAMA_CPP_REF}-linux-amd64-rpc}"
CMESH_MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-0.5b-instruct-q4-k-m}"
CMESH_PROMPT="${CMESH_PROMPT:-Reply in one short sentence: did this request use two distributed RPC backends?}"
CMESH_MAX_TOKENS="${CMESH_MAX_TOKENS:-32}"
CMESH_TEMPERATURE="${CMESH_TEMPERATURE:-0.2}"
CMESH_KEEP_AWS_RESOURCES="${CMESH_KEEP_AWS_RESOURCES:-false}"
CMESH_SKIP_RUNTIME_BUILD="${CMESH_SKIP_RUNTIME_BUILD:-false}"
CMESH_RUNTIME_ARCHIVE="${CMESH_RUNTIME_ARCHIVE:-}"
CMESH_E2E_DIR="${CMESH_E2E_DIR:-}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_RUN_ID:-cmesh-distributed-e2e-$(date -u +%Y%m%d%H%M%S)}"
STATE_DIR="${CMESH_E2E_DIR:-/tmp/$RUN_ID}"
KEY_NAME="$RUN_ID-key"
KEY_PATH="$STATE_DIR/$KEY_NAME.pem"
SG_ID=""
INSTANCE_IDS=()

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
  ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -i "$KEY_PATH" "$CMESH_AWS_SSH_USER@$host" "$@"
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
    return 0
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
  [[ -f "$STATE_DIR/roles.env" && -f "$KEY_PATH" ]] || return 0
  # shellcheck disable=SC1091
  source "$STATE_DIR/roles.env"
  mkdir -p "$STATE_DIR/remote-logs"
  local role host
  for role in manager backend1 backend2; do
    case "$role" in
      manager) host="${MANAGER_PUBLIC:-}" ;;
      backend1) host="${BACKEND1_PUBLIC:-}" ;;
      backend2) host="${BACKEND2_PUBLIC:-}" ;;
    esac
    [[ -n "$host" && "$host" != "null" ]] || continue
    ssh_run "$host" "tar -C /opt/cmesh -czf /tmp/cmesh-$role-logs.tar.gz *.log 2>/dev/null || true" >/dev/null 2>&1
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$KEY_PATH" \
      "$CMESH_AWS_SSH_USER@$host:/tmp/cmesh-$role-logs.tar.gz" \
      "$STATE_DIR/remote-logs/$role-logs.tar.gz" >/dev/null 2>&1 || true
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
    echo "deleting security group: $SG_ID"
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

wait_for_job() {
  local manager_public="$1"
  local job_id="$2"
  local operator_token="$3"
  local limit="${4:-240}"
  local i payload job_status
  for i in $(seq 1 "$limit"); do
    payload="$(curl -fsS -H "X-CMesh-Operator-Token: $operator_token" "http://$manager_public:18080/v1/jobs/$job_id")"
    job_status="$(printf '%s' "$payload" | jq -r .status)"
    echo "$job_id $job_status"
    printf '%s' "$payload" > "$STATE_DIR/$job_id.json"
    case "$job_status" in
      succeeded)
        printf '%s\n' "$payload" | jq '{id,type,status,result,error,started_at,finished_at}'
        return 0
        ;;
      failed|canceled)
        printf '%s\n' "$payload" | jq '{id,type,status,result,error,last_failure,started_at,finished_at}'
        return 1
        ;;
    esac
    sleep 3
  done
  fail "timed out waiting for job $job_id"
}

main() {
  need aws
  need curl
  need jq
  need ssh
  need scp
  need go
  need openssl

  if [[ "$CMESH_AWS_INSTANCE_COUNT" -lt 3 ]]; then
    fail "CMESH_AWS_INSTANCE_COUNT must be at least 3"
  fi

  if [[ "$CMESH_AWS_INSTANCE_COUNT" -gt 3 ]]; then
    fail "CMESH_AWS_INSTANCE_COUNT must be 3 or lower for this E2E"
  fi

  mkdir -p "$STATE_DIR" "$ROOT_DIR/dist"
  echo "$RUN_ID" > "$STATE_DIR/run_id"
  echo "$CMESH_AWS_REGION" > "$STATE_DIR/region"

  aws sts get-caller-identity --output json > "$STATE_DIR/aws-identity.json"
  cat > "$STATE_DIR/config.json" <<EOF
{
  "region": "$CMESH_AWS_REGION",
  "vpc_id": "$CMESH_AWS_VPC_ID",
  "subnet_id": "$CMESH_AWS_SUBNET_ID",
  "instance_type": "$CMESH_AWS_INSTANCE_TYPE",
  "instance_count": $CMESH_AWS_INSTANCE_COUNT,
  "volume_size_gb": $CMESH_AWS_VOLUME_SIZE,
  "model_id": "$CMESH_MODEL_ID",
  "runtime_version": "$CMESH_RUNTIME_VERSION",
  "keep_resources": $CMESH_KEEP_AWS_RESOURCES
}
EOF

  local ami_id
  ami_id="$CMESH_AWS_AMI_ID"
  if [[ -z "$ami_id" ]]; then
    ami_id="$(latest_ubuntu_ami)"
  fi

  echo "building cmesh linux/amd64 binary"
  (
    cd "$ROOT_DIR"
    GOOS=linux GOARCH=amd64 go build \
      -ldflags "-X github.com/cmesh/cmesh/internal/version.Version=distributed-e2e -X github.com/cmesh/cmesh/internal/version.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X github.com/cmesh/cmesh/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -o dist/cmesh-linux-amd64 ./cmd/cmesh
  )

  local my_ip
  my_ip="$(curl -fsS https://checkip.amazonaws.com | tr -d '\n')"
  echo "creating AWS resources for $RUN_ID"
  aws ec2 create-key-pair --region "$CMESH_AWS_REGION" --key-name "$KEY_NAME" --query 'KeyMaterial' --output text > "$KEY_PATH"
  chmod 600 "$KEY_PATH"
  echo "$KEY_NAME" > "$STATE_DIR/key_name"
  echo "$KEY_PATH" > "$STATE_DIR/key_path"

  SG_ID="$(aws ec2 create-security-group --region "$CMESH_AWS_REGION" --group-name "$RUN_ID-sg" --description "CMesh distributed E2E $RUN_ID" --vpc-id "$CMESH_AWS_VPC_ID" --query 'GroupId' --output text)"
  echo "$SG_ID" > "$STATE_DIR/sg_id"
  aws ec2 create-tags --region "$CMESH_AWS_REGION" --resources "$SG_ID" --tags Key=Name,Value="$RUN_ID-sg" Key=CMeshRun,Value="$RUN_ID"
  aws ec2 authorize-security-group-ingress --region "$CMESH_AWS_REGION" --group-id "$SG_ID" --ip-permissions \
    "IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=$my_ip/32,Description=ssh-local}]" \
    "IpProtocol=tcp,FromPort=18080,ToPort=18081,IpRanges=[{CidrIp=$my_ip/32,Description=manager-local}]" \
    "IpProtocol=tcp,FromPort=18080,ToPort=18081,UserIdGroupPairs=[{GroupId=$SG_ID,Description=manager-private}]" \
    "IpProtocol=tcp,FromPort=50052,ToPort=50052,UserIdGroupPairs=[{GroupId=$SG_ID,Description=rpc-private}]" >/dev/null

  read -r -a INSTANCE_IDS <<< "$(aws ec2 run-instances --region "$CMESH_AWS_REGION" \
    --image-id "$ami_id" \
    --instance-type "$CMESH_AWS_INSTANCE_TYPE" \
    --count "$CMESH_AWS_INSTANCE_COUNT" \
    --key-name "$KEY_NAME" \
    --network-interfaces "DeviceIndex=0,SubnetId=$CMESH_AWS_SUBNET_ID,Groups=$SG_ID,AssociatePublicIpAddress=true" \
    --block-device-mappings "[{\"DeviceName\":\"/dev/sda1\",\"Ebs\":{\"VolumeSize\":$CMESH_AWS_VOLUME_SIZE,\"VolumeType\":\"gp3\",\"DeleteOnTermination\":true}}]" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$RUN_ID},{Key=CMeshRun,Value=$RUN_ID}]" "ResourceType=volume,Tags=[{Key=Name,Value=$RUN_ID},{Key=CMeshRun,Value=$RUN_ID}]" \
    --query 'Instances[].InstanceId' --output text)"
  printf '%s\n' "${INSTANCE_IDS[@]}" > "$STATE_DIR/instance_ids"
  aws ec2 wait instance-running --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}"
  aws ec2 describe-instances --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}" --query 'Reservations[].Instances[].{ID:InstanceId,Public:PublicIpAddress,Private:PrivateIpAddress,State:State.Name}' --output json > "$STATE_DIR/instances.json"

  local manager_public manager_private backend1_public backend1_private backend2_public backend2_private
  manager_public="$(jq -r '.[0].Public' "$STATE_DIR/instances.json")"
  manager_private="$(jq -r '.[0].Private' "$STATE_DIR/instances.json")"
  backend1_public="$(jq -r '.[1].Public' "$STATE_DIR/instances.json")"
  backend1_private="$(jq -r '.[1].Private' "$STATE_DIR/instances.json")"
  backend2_public="$(jq -r '.[2].Public' "$STATE_DIR/instances.json")"
  backend2_private="$(jq -r '.[2].Private' "$STATE_DIR/instances.json")"
  cat > "$STATE_DIR/roles.env" <<EOF
MANAGER_PUBLIC=$manager_public
MANAGER_PRIVATE=$manager_private
BACKEND1_PUBLIC=$backend1_public
BACKEND1_PRIVATE=$backend1_private
BACKEND2_PUBLIC=$backend2_public
BACKEND2_PRIVATE=$backend2_private
EOF

  for host in "$manager_public" "$backend1_public" "$backend2_public"; do
    echo "preparing $host"
    wait_for_ssh "$host"
    ssh_run "$host" "sudo mkdir -p /opt/cmesh /var/lib/cmesh/cache && sudo chown -R $CMESH_AWS_SSH_USER:$CMESH_AWS_SSH_USER /opt/cmesh /var/lib/cmesh"
    scp_to "$ROOT_DIR/dist/cmesh-linux-amd64" "$host" "/opt/cmesh/cmesh"
    ssh_run "$host" "chmod +x /opt/cmesh/cmesh && /opt/cmesh/cmesh version"
  done

  scp_to "$ROOT_DIR/scripts/build-llamacpp-runtime.sh" "$manager_public" "/opt/cmesh/build-llamacpp-runtime.sh"
  if [[ -n "$CMESH_RUNTIME_ARCHIVE" ]]; then
    scp_to "$CMESH_RUNTIME_ARCHIVE" "$manager_public" "/opt/cmesh/${CMESH_RUNTIME_VERSION}.tar.gz"
    ssh_run "$manager_public" "mkdir -p /opt/cmesh/runtimes && mv /opt/cmesh/${CMESH_RUNTIME_VERSION}.tar.gz /opt/cmesh/runtimes/${CMESH_RUNTIME_VERSION}.tar.gz"
  elif [[ "$CMESH_SKIP_RUNTIME_BUILD" != "true" ]]; then
    echo "building pinned llama.cpp runtime on manager"
    ssh_run "$manager_public" "chmod +x /opt/cmesh/build-llamacpp-runtime.sh && sudo apt-get update -y && sudo apt-get install -y build-essential cmake git python3 jq && cd /opt/cmesh && LLAMA_CPP_REF='$CMESH_LLAMA_CPP_REF' JOBS=2 OUT_DIR=/opt/cmesh/runtimes WORK_DIR=/opt/cmesh/llamacpp-build ./build-llamacpp-runtime.sh"
  else
    fail "CMESH_SKIP_RUNTIME_BUILD=true requires CMESH_RUNTIME_ARCHIVE"
  fi

  for host in "$backend1_public" "$backend2_public"; do
    ssh_run "$host" "sudo apt-get update -y >/dev/null && sudo apt-get install -y libgomp1 python3 jq >/dev/null"
  done

  local join_token operator_token runtime_name runtime_url_private
  join_token="join-$(date +%s)-$(openssl rand -hex 8)"
  operator_token="operator-$(date +%s)-$(openssl rand -hex 8)"
  runtime_name="${CMESH_RUNTIME_VERSION}.tar.gz"
  runtime_url_private="http://$manager_private:18081/$runtime_name"
  echo "$join_token" > "$STATE_DIR/join_token"
  echo "$operator_token" > "$STATE_DIR/operator_token"

  echo "starting manager and workers"
  ssh_run "$manager_public" "nohup python3 -m http.server 18081 --directory /opt/cmesh/runtimes > /opt/cmesh/runtime-http.log 2>&1 & nohup /opt/cmesh/cmesh manager start -addr 0.0.0.0:18080 -join-token '$join_token' -operator-token '$operator_token' -public-url 'http://$manager_public:18080' -memory > /opt/cmesh/manager.log 2>&1 &"
  local i
  for i in $(seq 1 60); do
    if curl -fsS "http://$manager_public:18080/health" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  curl -fsS "http://$manager_public:18080/health" | tee "$STATE_DIR/health.json"

  ssh_run "$manager_public" "mkdir -p /var/lib/cmesh/cache/runtimes/llama.cpp/$CMESH_RUNTIME_VERSION && rm -rf /var/lib/cmesh/cache/runtimes/llama.cpp/$CMESH_RUNTIME_VERSION/* && tar -C /var/lib/cmesh/cache/runtimes/llama.cpp/$CMESH_RUNTIME_VERSION -xzf /opt/cmesh/runtimes/$runtime_name"
  ssh_run "$manager_public" "CMESH_LLAMA_CPP_RUNTIME_URL='http://127.0.0.1:18081/$runtime_name' CMESH_LLAMA_CPP_RUNTIME_NAME='$runtime_name' CMESH_LLAMA_CPP_RUNTIME_VERSION='$CMESH_RUNTIME_VERSION' CMESH_LLAMA_CPP_PREFER_CACHE=true nohup /opt/cmesh/cmesh worker run --manager 'http://$manager_private:18080' --token '$join_token' --name coordinator --cpu 2 --memory-gb 3 --disk-gb 20 --cache-dir /var/lib/cmesh/cache --benchmark=false > /opt/cmesh/coordinator.log 2>&1 &"
  ssh_run "$backend1_public" "CMESH_LLAMA_CPP_RUNTIME_URL='$runtime_url_private' CMESH_LLAMA_CPP_RUNTIME_NAME='$runtime_name' CMESH_LLAMA_CPP_RUNTIME_VERSION='$CMESH_RUNTIME_VERSION' CMESH_LLAMA_CPP_PREFER_CACHE=true nohup /opt/cmesh/cmesh worker run --manager 'http://$manager_private:18080' --token '$join_token' --name backend-1 --cpu 2 --memory-gb 3 --disk-gb 20 --cache-dir /var/lib/cmesh/cache --benchmark=false --rpc --rpc-host 0.0.0.0 --rpc-advertise-host '$backend1_private' --rpc-port 50052 > /opt/cmesh/backend.log 2>&1 &"
  ssh_run "$backend2_public" "CMESH_LLAMA_CPP_RUNTIME_URL='$runtime_url_private' CMESH_LLAMA_CPP_RUNTIME_NAME='$runtime_name' CMESH_LLAMA_CPP_RUNTIME_VERSION='$CMESH_RUNTIME_VERSION' CMESH_LLAMA_CPP_PREFER_CACHE=true nohup /opt/cmesh/cmesh worker run --manager 'http://$manager_private:18080' --token '$join_token' --name backend-2 --cpu 2 --memory-gb 3 --disk-gb 20 --cache-dir /var/lib/cmesh/cache --benchmark=false --rpc --rpc-host 0.0.0.0 --rpc-advertise-host '$backend2_private' --rpc-port 50052 > /opt/cmesh/backend.log 2>&1 &"

  sleep 25
  curl -fsS -H "X-CMesh-Operator-Token: $operator_token" "http://$manager_public:18080/v1/nodes" > "$STATE_DIR/nodes.json"
  jq '{nodes: [.nodes[] | {id,name,status:.status,runtimes:.resources.runtimes}]}' "$STATE_DIR/nodes.json"

  local coordinator_id install_job generate_job
  coordinator_id="$(jq -r '.nodes[] | select(.name=="coordinator") | .id' "$STATE_DIR/nodes.json")"
  [[ -n "$coordinator_id" && "$coordinator_id" != "null" ]] || fail "coordinator did not join"

  echo "installing model on coordinator"
  install_job="$(curl -fsS -X POST -H "X-CMesh-Operator-Token: $operator_token" -H 'Content-Type: application/json' -d "{\"node_id\":\"$coordinator_id\"}" "http://$manager_public:18080/v1/models/$CMESH_MODEL_ID/install" | tee "$STATE_DIR/install-job.json" | jq -r .id)"
  wait_for_job "$manager_public" "$install_job" "$operator_token" 240

  echo "refreshing RPC pool and planning distributed generate"
  curl -fsS -X POST -H "X-CMesh-Operator-Token: $operator_token" "http://$manager_public:18080/v1/runtime/rpc-pool/refresh?timeout_ms=1500" | tee "$STATE_DIR/rpc-refresh.json" | jq '{summary,endpoints,report:{checked:.report.checked,ready:.report.ready,failed:.report.failed,runnable_now:.report.runnable_now}}'
  curl -fsS -H "X-CMesh-Operator-Token: $operator_token" "http://$manager_public:18080/v1/models/$CMESH_MODEL_ID/distributed-rpc-plan?check=1&node_id=$coordinator_id" | tee "$STATE_DIR/rpc-plan.json" | jq '{executable_now,blockers,warnings,rpc_endpoints,backends}'
  [[ "$(jq -r .executable_now "$STATE_DIR/rpc-plan.json")" == "true" ]] || fail "distributed RPC plan is not executable"

  echo "submitting distributed generate"
  generate_job="$(curl -fsS -X POST -H "X-CMesh-Operator-Token: $operator_token" -H 'Content-Type: application/json' \
    -d "$(jq -n --arg node "$coordinator_id" --arg prompt "$CMESH_PROMPT" --arg temp "$CMESH_TEMPERATURE" --argjson max "$CMESH_MAX_TOKENS" '{node_id:$node,prompt:$prompt,max_tokens:$max,temperature:$temp}')" \
    "http://$manager_public:18080/v1/models/$CMESH_MODEL_ID/distributed-rpc-generate" | tee "$STATE_DIR/generate-job.json" | jq -r .id)"
  wait_for_job "$manager_public" "$generate_job" "$operator_token" 180
  curl -fsS -H "X-CMesh-Operator-Token: $operator_token" "http://$manager_public:18080/v1/jobs/$generate_job" > "$STATE_DIR/generate-job-final.json"
  jq -r '.result' "$STATE_DIR/generate-job-final.json" | jq '{kind,model_id,runtime_version,rpc_endpoint_count,rpc_endpoints,duration_ms:.execution_result.duration_ms,output:.execution_result.output}' | tee "$STATE_DIR/distributed-result.json"

  echo "PASS: distributed RPC E2E succeeded"
  echo "evidence: $STATE_DIR"
}

main "$@"
