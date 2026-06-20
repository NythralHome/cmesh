#!/usr/bin/env bash
set -euo pipefail

CMESH_AWS_REGION="${CMESH_AWS_REGION:-us-east-1}"
CMESH_AWS_VPC_ID="${CMESH_AWS_VPC_ID:-vpc-009bb3d2b3eb90ba7}"
CMESH_AWS_SUBNET_ID="${CMESH_AWS_SUBNET_ID:-subnet-0cfdff3dcc5b8a13b}"
CMESH_AWS_INSTANCE_TYPE="${CMESH_AWS_INSTANCE_TYPE:-t3.large}"
CMESH_AWS_INSTANCE_COUNT="${CMESH_AWS_INSTANCE_COUNT:-3}"
CMESH_AWS_VOLUME_SIZE="${CMESH_AWS_VOLUME_SIZE:-60}"
CMESH_AWS_AMI_ID="${CMESH_AWS_AMI_ID:-}"
CMESH_AWS_SSH_USER="${CMESH_AWS_SSH_USER:-ubuntu}"
CMESH_MODEL_ID="${CMESH_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
CMESH_MODEL_URL="${CMESH_MODEL_URL:-https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf}"
CMESH_MODEL_FILE="${CMESH_MODEL_FILE:-qwen2.5-0.5b-instruct-q4_k_m.gguf}"
CMESH_EXPECTED_MODEL_LAYERS="${CMESH_EXPECTED_MODEL_LAYERS:-24}"
CMESH_PROMPT="${CMESH_PROMPT:-hello from cmesh real cdip stage workers}"
CMESH_STAGE_TIMEOUT_MS="${CMESH_STAGE_TIMEOUT_MS:-120000}"
CMESH_KEEP_AWS_RESOURCES="${CMESH_KEEP_AWS_RESOURCES:-false}"
CMESH_E2E_DIR="${CMESH_E2E_DIR:-}"
CMESH_STAGE_RUNTIME_ARCHIVE="${CMESH_STAGE_RUNTIME_ARCHIVE:-}"
CMESH_STAGE_RUNTIME_URL="${CMESH_STAGE_RUNTIME_URL:-}"
CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT="${CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT:-false}"
CMESH_INSTALL_MANAGER_SERVICE="${CMESH_INSTALL_MANAGER_SERVICE:-false}"
CMESH_INSTALL_STAGE_WORKER_SERVICES="${CMESH_INSTALL_STAGE_WORKER_SERVICES:-false}"
CMESH_MANAGER_AS_STAGE_WORKER="${CMESH_MANAGER_AS_STAGE_WORKER:-true}"
CMESH_STAGE_WORKER_MEMORY_GB="${CMESH_STAGE_WORKER_MEMORY_GB:-}"
CMESH_STAGE_WORKER_DISK_GB="${CMESH_STAGE_WORKER_DISK_GB:-50}"
CMESH_PLACEMENT_PROOF_MODEL_ID="${CMESH_PLACEMENT_PROOF_MODEL_ID:-qwen2.5-14b-instruct-q4-k-m}"
CMESH_PREFLIGHT_ONLY="${CMESH_PREFLIGHT_ONLY:-false}"
CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION="${CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION:-false}"
CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-}"
if [[ -z "$CMESH_STAGE_WORKER_MEMORY_GB" ]]; then
  if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
    CMESH_STAGE_WORKER_MEMORY_GB=8
  else
    CMESH_STAGE_WORKER_MEMORY_GB=20
  fi
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ID="${CMESH_RUN_ID:-cmesh-cdip-real-gguf-e2e-$(date -u +%Y%m%d%H%M%S)}"
STATE_DIR="${CMESH_E2E_DIR:-/tmp/$RUN_ID}"
KEY_NAME="$RUN_ID-key"
KEY_PATH="$STATE_DIR/$KEY_NAME.pem"
SG_ID=""
INSTANCE_IDS=()
MANAGER_PUBLIC=""
MANAGER_PRIVATE=""
NODE_STAGE0=""
STAGE1_PUBLIC=""
STAGE1_PRIVATE=""
NODE_STAGE1=""
STAGE2_PUBLIC=""
STAGE2_PRIVATE=""
NODE_STAGE2=""
PACKAGE_DIR=""

REMOTE_ROOT="/opt/cmesh"
REMOTE_SOURCE="$REMOTE_ROOT/source"
REMOTE_BIN="$REMOTE_ROOT/cmesh"
REMOTE_MODEL="/var/lib/cmesh/models/$CMESH_MODEL_FILE"
REMOTE_STAGE_SHARDS="/var/lib/cmesh/stage-shards"
REMOTE_STAGE_RUNTIME="/var/lib/cmesh/stage-runtime"
REMOTE_RUNNER="$REMOTE_STAGE_RUNTIME/bin/cmesh-stage-runner"
REMOTE_PREP_RUNNER="$REMOTE_STAGE_RUNTIME/bin/cmesh-stage-runner"
REMOTE_STAGE_WORK="/var/lib/cmesh/stage-work"
STAGE_RUNTIME_NAME=""
STAGE_RUNTIME_VERSION=""
STAGE_RUNTIME_ARCHIVE_SHA256=""

if [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]]; then
  PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  if [[ -z "$CMESH_STAGE_RUNTIME_ARCHIVE" && -z "$CMESH_STAGE_RUNTIME_URL" ]]; then
    CMESH_STAGE_RUNTIME_ARCHIVE="$PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz"
  fi
fi

if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" ]]; then
  STAGE_RUNTIME_NAME="$(basename "$CMESH_STAGE_RUNTIME_ARCHIVE")"
elif [[ -n "$CMESH_STAGE_RUNTIME_URL" ]]; then
  STAGE_RUNTIME_NAME="${CMESH_STAGE_RUNTIME_URL%%\?*}"
  STAGE_RUNTIME_NAME="${STAGE_RUNTIME_NAME##*/}"
fi
if [[ -n "$STAGE_RUNTIME_NAME" ]]; then
  STAGE_RUNTIME_VERSION="${STAGE_RUNTIME_NAME%.tar.gz}"
fi
if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" && -n "$STAGE_RUNTIME_VERSION" ]]; then
  REMOTE_RUNNER="/var/lib/cmesh/cache/runtimes/llama.cpp/$STAGE_RUNTIME_VERSION/bin/cmesh-stage-runner"
fi

if [[ -z "$CMESH_STAGE_RUNTIME_ARCHIVE" && -z "$CMESH_STAGE_RUNTIME_URL" ]]; then
  REMOTE_RUNNER="/var/lib/cmesh/stage-runner/src/build-cmesh-stage/bin/cmesh-stage-runner"
fi

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

require_allowed_instance_type() {
  case "$CMESH_AWS_INSTANCE_TYPE" in
    t3.large|t3a.large|m6i.large|m7i.large)
      return 0
      ;;
  esac
  fail "CMESH_AWS_INSTANCE_TYPE=$CMESH_AWS_INSTANCE_TYPE is not in the cost guard allowlist"
}

file_sha256() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$path" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required"
  fi
}

gb_to_bytes() {
  awk -v gb="$1" 'BEGIN { printf "%.0f", gb * 1073741824 }'
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

scp_from() {
  local host="$1"
  local source="$2"
  local target="$3"
  scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i "$KEY_PATH" "$CMESH_AWS_SSH_USER@$host:$source" "$target" >/dev/null
}

scp_from_sudo() {
  local host="$1"
  local source="$2"
  local target="$3"
  local tmp="/tmp/cmesh-e2e-copy-$(basename "$source")"
  ssh_run "$host" "sudo cp '$source' '$tmp' && sudo chown '$CMESH_AWS_SSH_USER:$CMESH_AWS_SSH_USER' '$tmp'"
  scp_from "$host" "$tmp" "$target"
  ssh_run "$host" "rm -f '$tmp'" >/dev/null 2>&1 || true
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

stage_public_hosts() {
  if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
    printf '%s\n' "$MANAGER_PUBLIC" "$STAGE1_PUBLIC" "$STAGE2_PUBLIC"
  else
    printf '%s\n' "$STAGE1_PUBLIC" "$STAGE2_PUBLIC"
  fi
}

stage_node_names() {
  if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
    printf '%s\n' real-cdip-stage-0 real-cdip-stage-1 real-cdip-stage-2
  else
    printf '%s\n' real-cdip-stage-1 real-cdip-stage-2
  fi
}

collect_remote_logs() {
  [[ -f "$KEY_PATH" ]] || return 0
  mkdir -p "$STATE_DIR/remote-logs"
  local role host
  for role in manager stage1 stage2; do
    case "$role" in
      manager) host="$MANAGER_PUBLIC" ;;
      stage1) host="$STAGE1_PUBLIC" ;;
      stage2) host="$STAGE2_PUBLIC" ;;
    esac
    [[ -n "$host" && "$host" != "null" ]] || continue
    ssh_run "$host" "journalctl -u cmesh.service -u cmesh-worker.service -u cmesh-stage-daemon.service --no-pager -n 300 > /opt/cmesh/journal.log 2>&1 || true; files=\$(find /opt/cmesh -maxdepth 1 -name '*.log' -printf '%f ' 2>/dev/null); if [ -n \"\$files\" ]; then tar -C /opt/cmesh -czf /tmp/cmesh-$role-logs.tar.gz \$files; else tar -czf /tmp/cmesh-$role-logs.tar.gz --files-from /dev/null; fi" >/dev/null 2>&1
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
  set +u
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
  for i in $(seq 1 60); do
    if ssh -n -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -i "$KEY_PATH" "$CMESH_AWS_SSH_USER@$host" "echo ok" >/dev/null 2>&1; then
      return 0
    fi
    if (( i % 10 == 0 )); then
      echo "waiting for SSH on $host ($i/60)"
    fi
    sleep 2
  done
  fail "SSH did not become ready for $host"
}

wait_for_manager() {
  local i
  for i in $(seq 1 90); do
    if curl -fsS "http://$MANAGER_PUBLIC:18080/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "manager health did not become ready"
}

post_manager_json() {
  local path="$1"
  local request_file="$2"
  local response_file="$3"
  local status_file="$response_file.status"
  local status
  status="$(curl -sS -w "%{http_code}" -o "$response_file" \
    -X POST "http://$MANAGER_PUBLIC:18080$path" \
    -H "Authorization: Bearer $OPERATOR_TOKEN" \
    -H 'Content-Type: application/json' \
    -d @"$request_file")"
  printf '%s\n' "$status" > "$status_file"
  if [[ "$status" -lt 200 || "$status" -ge 300 ]]; then
    echo "manager POST $path returned HTTP $status" >&2
    cat "$response_file" >&2 || true
    fail "manager POST failed: $path"
  fi
}

register_stage_worker() {
  local name="$1"
  local n_layer="$2"
  local memory_bytes storage_bytes storage_total_bytes
  local response node_id
  memory_bytes="$(gb_to_bytes "$CMESH_STAGE_WORKER_MEMORY_GB")"
  storage_bytes="$(gb_to_bytes "$CMESH_STAGE_WORKER_DISK_GB")"
  storage_total_bytes="$storage_bytes"
  response="$(curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/workers/join" \
    -H 'Content-Type: application/json' \
    -d "{\"node_name\":\"$name\",\"role\":\"worker\",\"resources\":{\"cpu\":{\"cores_total\":8,\"cores_allowed\":4},\"memory\":{\"total_bytes\":$memory_bytes,\"allowed_bytes\":$memory_bytes},\"storage\":{\"total_bytes\":$storage_total_bytes,\"allowed_bytes\":$storage_bytes,\"free_bytes\":$storage_bytes},\"models\":[{\"id\":\"$CMESH_MODEL_ID\",\"runtime\":\"llama.cpp\",\"path\":\"$REMOTE_MODEL\",\"layers\":$n_layer,\"ready\":true}],\"runtimes\":[{\"name\":\"llama.cpp\",\"ready\":true,\"capabilities\":[\"logical-stage-runtime\",\"llama.cpp-stage-runner\"]}]},\"join_token\":\"$JOIN_TOKEN\"}")"
  node_id="$(jq -r '.node_id' <<<"$response")"
  jq -r '.node_auth_token' <<<"$response" >"$STATE_DIR/node-token-$node_id"
  printf '%s\n' "$node_id"
}

stage_shard_path() {
  local index="$1"
  printf '%s/stage-%s.gguf' "$REMOTE_STAGE_SHARDS" "$index"
}

stage_model_paths_json() {
  printf '["%s","%s","%s"]' "$(stage_shard_path 0)" "$(stage_shard_path 1)" "$(stage_shard_path 2)"
}

stage_node_ids_json() {
  printf '["%s","%s","%s"]' "$NODE_STAGE0" "$NODE_STAGE1" "$NODE_STAGE2"
}

write_remote_stage_gguf_shard() {
  local host="$1"
  local index="$2"
  local start="$3"
  local end="$4"
  local first="$5"
  local terminal="$6"
  local args="--command write-shard-bundle --model '$REMOTE_MODEL' --stage-start '$start' --stage-end '$end' --stage-index '$index' --output-file '$REMOTE_STAGE_SHARDS/stage-$index.cmesh-shard'"
  if [[ "$first" == "true" ]]; then
    args="$args --first-stage"
  fi
  if [[ "$terminal" == "true" ]]; then
    args="$args --terminal-stage"
  fi
  ssh_run "$host" "mkdir -p '$REMOTE_STAGE_SHARDS' && '$REMOTE_PREP_RUNNER' $args > '$REMOTE_ROOT/write-shard-bundle-$index.json' && '$REMOTE_PREP_RUNNER' --command verify-shard-bundle-source --bundle-file '$REMOTE_STAGE_SHARDS/stage-$index.cmesh-shard' --model '$REMOTE_MODEL' > '$REMOTE_ROOT/verify-shard-bundle-source-$index.json' && '$REMOTE_PREP_RUNNER' --command write-stage-gguf-shard --bundle-file '$REMOTE_STAGE_SHARDS/stage-$index.cmesh-shard' --model '$REMOTE_MODEL' --output-file '$(stage_shard_path "$index")' > '$REMOTE_ROOT/write-stage-gguf-shard-$index.json' && '$REMOTE_PREP_RUNNER' --command probe-stage-gguf-load --model '$(stage_shard_path "$index")' > '$REMOTE_ROOT/probe-stage-gguf-load-$index.json'"
  scp_from_sudo "$host" "$REMOTE_ROOT/probe-stage-gguf-load-$index.json" "$STATE_DIR/probe-stage-gguf-load-$index.json"
  jq -e --arg model "$(stage_shard_path "$index")" --argjson start "$start" --argjson end "$end" '
    .kind == "cmesh.llamacpp_stage_gguf_load_probe" and
    .status == "stage_model_loaded_partial" and
    .loaded == true and
    .model_path == $model and
    .stage_start == $start and
    .stage_end == $end
  ' "$STATE_DIR/probe-stage-gguf-load-$index.json" >/dev/null
}

prepare_remote_stage_gguf_shards() {
  [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]] || fail "physical stage GGUF E2E requires CMESH_MANAGER_AS_STAGE_WORKER=true"
  [[ "$N_LAYER" -ge 3 ]] || fail "physical stage GGUF E2E requires at least 3 layers"
  FIRST_END=$((N_LAYER / 3 - 1))
  if [[ "$FIRST_END" -lt 0 ]]; then FIRST_END=0; fi
  MIDDLE_START=$((FIRST_END + 1))
  MIDDLE_END=$((2 * N_LAYER / 3 - 1))
  if [[ "$MIDDLE_END" -lt "$MIDDLE_START" ]]; then MIDDLE_END="$MIDDLE_START"; fi
  TERMINAL_START=$((MIDDLE_END + 1))
  if [[ "$TERMINAL_START" -ge "$N_LAYER" ]]; then TERMINAL_START=$((N_LAYER - 1)); fi
  TERMINAL_END=$((N_LAYER - 1))
  jq -n \
    --argjson n_layer "$N_LAYER" \
    --argjson first_end "$FIRST_END" \
    --argjson middle_start "$MIDDLE_START" \
    --argjson middle_end "$MIDDLE_END" \
    --argjson terminal_start "$TERMINAL_START" \
    --argjson terminal_end "$TERMINAL_END" \
    '{n_layer:$n_layer,stages:[{index:0,start:0,end:$first_end},{index:1,start:$middle_start,end:$middle_end},{index:2,start:$terminal_start,end:$terminal_end}]}' \
    > "$STATE_DIR/stage-shard-ranges.json"
  write_remote_stage_gguf_shard "$MANAGER_PUBLIC" 0 0 "$FIRST_END" true false
  write_remote_stage_gguf_shard "$STAGE1_PUBLIC" 1 "$MIDDLE_START" "$MIDDLE_END" false false
  write_remote_stage_gguf_shard "$STAGE2_PUBLIC" 2 "$TERMINAL_START" "$TERMINAL_END" false true
}

run_poll_once() {
  local host="$1"
  local node_id="$2"
  local name="$3"
  local node_auth_token
  node_auth_token="$(cat "$STATE_DIR/node-token-$node_id")"
  ssh_run "$host" "CMESH_STAGE_RUNNER_BIN='$REMOTE_RUNNER' '$REMOTE_BIN' worker poll-once --manager 'http://$MANAGER_PRIVATE:18080' --node-id '$node_id' --node-auth-token '$node_auth_token' --cache-dir '/var/lib/cmesh/cache-$name' --model-id '$CMESH_MODEL_ID' --model-path '$REMOTE_MODEL' --model-layers '$N_LAYER' --runtime 'llama.cpp' --cpu 4 --memory-gb '$CMESH_STAGE_WORKER_MEMORY_GB' --disk-gb '$CMESH_STAGE_WORKER_DISK_GB' > '$REMOTE_ROOT/poll-$name.log' 2>&1"
}

record_job() {
  local job_id="$1"
  local target="$2"
  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "http://$MANAGER_PUBLIC:18080/v1/jobs/$job_id" > "$target"
}

wait_for_job_state() {
  local job_id="$1"
  local target="$2"
  local expected_status="$3"
  local expected_cdip="$4"
  local i status cdip_state
  for i in $(seq 1 120); do
    record_job "$job_id" "$target"
    status="$(jq -r '.status' "$target")"
    cdip_state="$(jq -r '.cdip_state // ""' "$target")"
    if [[ "$status" == "$expected_status" && "$cdip_state" == "$expected_cdip" ]]; then
      return 0
    fi
    if [[ "$status" == "failed" || "$cdip_state" == "failed" ]]; then
      jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result}' "$target" >&2
      fail "job $job_id failed while waiting for status=$expected_status cdip_state=$expected_cdip"
    fi
    sleep 2
  done
  jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result}' "$target" >&2 || true
  fail "timed out waiting for job $job_id status=$expected_status cdip_state=$expected_cdip"
}

wait_for_parent_status() {
  local job_id="$1"
  local target="$2"
  local expected_status="$3"
  local i status
  for i in $(seq 1 180); do
    record_job "$job_id" "$target"
    status="$(jq -r '.status' "$target")"
    if [[ "$status" == "$expected_status" ]]; then
      return 0
    fi
    if [[ "$status" == "failed" || "$status" == "canceled" ]]; then
      jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result}' "$target" >&2
      fail "parent job $job_id became $status while waiting for $expected_status"
    fi
    sleep 2
  done
  jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result}' "$target" >&2 || true
  fail "timed out waiting for parent job $job_id status=$expected_status"
}

wait_for_dispatch_step() {
  local parent_id="$1"
  local target="$2"
  local expected_step="$3"
  local expected_kv="$4"
  local i
  for i in $(seq 1 180); do
    curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "http://$MANAGER_PUBLIC:18080/v1/jobs" > "$target"
    if jq -e --arg parent "$parent_id" --arg kv "$expected_kv" --argjson step "$expected_step" '
      [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
      | length >= 2
      and all(.step == $step and .kv_cache_key == $kv)
      and any(.stage_command == "source_decode")
      and any(.stage_command == "terminal_decode")
    ' "$target" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  jq --arg parent "$parent_id" '[.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | {id,type,status,cdip_state,input:(.input | fromjson?)}]' "$target" >&2 || true
  fail "timed out waiting for dispatch step $expected_step for parent $parent_id"
}

extract_stage_runner_json() {
  python3 - "$1" <<'PY'
import json
import sys

path = sys.argv[1]
text = open(path, encoding="utf-8", errors="replace").read()
for index, char in enumerate(text):
    if char != "{":
        continue
    try:
        parsed = json.loads(text[index:])
    except json.JSONDecodeError:
        continue
    print(json.dumps(parsed))
    raise SystemExit(0)
raise SystemExit(f"no JSON report found in {path}")
PY
}

collect_dispatch_runner_reports() {
  local source_host="$1"
  local source_index="$2"
  local terminal_host="$3"
  local terminal_index="$4"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
    jq -n \
      --arg source_host "$source_host" \
      --arg terminal_host "$terminal_host" \
      --arg source_index "$source_index" \
      --arg terminal_index "$terminal_index" \
      '{kind:"cmesh.dispatch_runner_reports",status:"skipped",reason:"resident stage worker services report execution through daemon sessions and manager job results",source_host:$source_host,terminal_host:$terminal_host,source_index:($source_index|tonumber),terminal_index:($terminal_index|tonumber)}' \
      > "$STATE_DIR/dispatch-runner-reports-skipped.json"
    return 0
  fi
  scp_from_sudo "$source_host" "$REMOTE_STAGE_WORK-dispatch/stage-$source_index/stage-runner-source-decode.json" "$STATE_DIR/dispatch-source-runner.raw"
  scp_from_sudo "$terminal_host" "$REMOTE_STAGE_WORK-dispatch/stage-$terminal_index/stage-runner-terminal-decode.json" "$STATE_DIR/dispatch-terminal-runner.raw"
  extract_stage_runner_json "$STATE_DIR/dispatch-source-runner.raw" > "$STATE_DIR/dispatch-source-runner-final.json"
  extract_stage_runner_json "$STATE_DIR/dispatch-terminal-runner.raw" > "$STATE_DIR/dispatch-terminal-runner-final.json"
  jq -e '.kind == "cmesh.llamacpp_stage_source_decode" and (.token_count // 0) > 0' \
    "$STATE_DIR/dispatch-source-runner-final.json" >/dev/null
  jq -e '.kind == "cmesh.llamacpp_stage_terminal_decode" and (.tokens | length) >= 1' \
    "$STATE_DIR/dispatch-terminal-runner-final.json" >/dev/null
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" != "true" ]]; then
    jq -e '.kv_session.enabled == true and .kv_session.loaded_bytes > 0 and .kv_session.saved_bytes > 0' \
      "$STATE_DIR/dispatch-source-runner-final.json" >/dev/null
    jq -e '.kv_session.enabled == true and .kv_session.loaded_bytes > 0 and .kv_session.saved_bytes > 0' \
      "$STATE_DIR/dispatch-terminal-runner-final.json" >/dev/null
  fi
}

require_job_state() {
  local job_file="$1"
  local expected_status="$2"
  local expected_cdip="$3"
  local status cdip_state
  status="$(jq -r '.status' "$job_file")"
  cdip_state="$(jq -r '.cdip_state // ""' "$job_file")"
  if [[ "$status" != "$expected_status" || "$cdip_state" != "$expected_cdip" ]]; then
    jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result}' "$job_file" >&2
    fail "expected $job_file to be status=$expected_status cdip_state=$expected_cdip, got status=$status cdip_state=$cdip_state"
  fi
}

require_dispatch_stage_step_done_or_advanced() {
  local job_file="$1"
  local expected_step="$2"
  if jq -e --argjson step "$expected_step" '
    .cdip_state == "decode" and (
      (.status == "succeeded" and ((.input | fromjson).step == $step)) or
      (.status == "scheduled" and (((.input | fromjson).step // 0) > $step))
    )
  ' "$job_file" >/dev/null; then
    return 0
  fi
  jq '{id,type,status,assigned_to,error,last_failure,cdip_state,result,input:(.input | fromjson?)}' "$job_file" >&2
  fail "expected $job_file to complete dispatch step $expected_step or advance to a later step"
}

prepare_stage_host() {
  local host="$1"
  echo "preparing stage runtime on $host"
  if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" || -n "$CMESH_STAGE_RUNTIME_URL" ]]; then
    ssh_run "$host" "sudo apt-get update -y >/dev/null && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq libgomp1 >/dev/null"
  else
    ssh_run "$host" "sudo apt-get update -y >/dev/null && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y build-essential cmake git python3 jq libgomp1 >/dev/null"
  fi
  ssh_run "$host" "curl -fL --retry 3 --retry-delay 2 -o '$REMOTE_MODEL.tmp' '$CMESH_MODEL_URL' && mv '$REMOTE_MODEL.tmp' '$REMOTE_MODEL'"
  if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" ]]; then
    scp_to "$CMESH_STAGE_RUNTIME_ARCHIVE" "$host" "$REMOTE_ROOT/stage-runtime.tar.gz"
    if [[ -f "$CMESH_STAGE_RUNTIME_ARCHIVE.sha256" ]]; then
      scp_to "$CMESH_STAGE_RUNTIME_ARCHIVE.sha256" "$host" "$REMOTE_ROOT/stage-runtime.tar.gz.sha256"
    fi
    ssh_run "$host" "rm -rf '$REMOTE_STAGE_RUNTIME' && mkdir -p '$REMOTE_STAGE_RUNTIME' && tar -C '$REMOTE_STAGE_RUNTIME' -xzf '$REMOTE_ROOT/stage-runtime.tar.gz' && chmod +x '$REMOTE_PREP_RUNNER' && ('$REMOTE_PREP_RUNNER' --probe > '$REMOTE_ROOT/prepare-runner.log' 2>&1 || true)"
  elif [[ -n "$CMESH_STAGE_RUNTIME_URL" ]]; then
    ssh_run "$host" "rm -rf '$REMOTE_STAGE_RUNTIME' && mkdir -p '$REMOTE_STAGE_RUNTIME' && curl -fL --retry 3 --retry-delay 2 -o '$REMOTE_ROOT/stage-runtime.tar.gz' '$CMESH_STAGE_RUNTIME_URL' && tar -C '$REMOTE_STAGE_RUNTIME' -xzf '$REMOTE_ROOT/stage-runtime.tar.gz' && chmod +x '$REMOTE_PREP_RUNNER' && ('$REMOTE_PREP_RUNNER' --probe > '$REMOTE_ROOT/prepare-runner.log' 2>&1 || true)"
  else
    ssh_run "$host" "WORK_DIR=/var/lib/cmesh/stage-runner '$REMOTE_SOURCE/scripts/prepare-llamacpp-stage-runner-worktree.sh' > '$REMOTE_ROOT/prepare-runner.log' 2>&1"
  fi
}

install_stage_worker_service() {
  local host="$1"
  local name="$2"
  local runtime_env="CMESH_LLAMA_CPP_RUNTIME_AUTO=false CMESH_STAGE_RUNNER_BIN=$REMOTE_RUNNER"
  if [[ -n "$STAGE_RUNTIME_VERSION" ]]; then
    runtime_env="CMESH_LLAMA_CPP_RUNTIME_AUTO=false CMESH_LLAMA_CPP_RUNTIME_NAME=$STAGE_RUNTIME_NAME CMESH_LLAMA_CPP_RUNTIME_VERSION=$STAGE_RUNTIME_VERSION CMESH_LLAMA_CPP_PREFER_CACHE=true"
    if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" ]]; then
      runtime_env="$runtime_env CMESH_LLAMA_CPP_RUNTIME_URL=file://$REMOTE_ROOT/stage-runtime.tar.gz"
    elif [[ -n "$CMESH_STAGE_RUNTIME_URL" ]]; then
      runtime_env="$runtime_env CMESH_LLAMA_CPP_RUNTIME_URL=$CMESH_STAGE_RUNTIME_URL"
    else
      runtime_env="$runtime_env CMESH_STAGE_RUNNER_BIN=$REMOTE_RUNNER"
    fi
  fi
  echo "installing stage worker service on $host"
  ssh_run "$host" "sudo env CMESH_BINARY_URL=file://$REMOTE_BIN CMESH_NONINTERACTIVE=true CMESH_MANAGER_URL=http://$MANAGER_PRIVATE:18080 CMESH_JOIN_TOKEN=$JOIN_TOKEN CMESH_NODE_NAME=$name CMESH_INSTALL_SERVICE=true CMESH_BENCHMARK=false CMESH_CPU=4 CMESH_MEMORY_GB=$CMESH_STAGE_WORKER_MEMORY_GB CMESH_DISK_GB=$CMESH_STAGE_WORKER_DISK_GB $runtime_env CMESH_STAGE_DAEMON_BACKEND=llama.cpp-resident CMESH_MODEL_ID=$CMESH_MODEL_ID CMESH_MODEL_URL=$CMESH_MODEL_URL CMESH_MODEL_FILE=$CMESH_MODEL_FILE CMESH_MODEL_LAYERS=$N_LAYER CMESH_MODEL_RUNTIME=llama.cpp '$REMOTE_ROOT/install-worker.sh' > '$REMOTE_ROOT/install-worker.log' 2>&1"
  ssh_run "$host" "systemctl is-active cmesh-worker.service" > "$STATE_DIR/$name-service.txt"
  ssh_run "$host" "systemctl is-active cmesh-stage-daemon.service" > "$STATE_DIR/$name-stage-daemon-service.txt"
  ssh_run "$host" "curl -fsS http://127.0.0.1:19781/health" > "$STATE_DIR/$name-stage-daemon-health.json"
  jq -e '.status == "ok" and .protocol == "cdip.stage-session-v1" and .backend == "llama.cpp-resident" and .native_kv == true' "$STATE_DIR/$name-stage-daemon-health.json" >/dev/null
}

wait_for_stage_service_workers() {
  local i nodes expected_names stage_nodes
  expected_names=()
  while IFS= read -r name; do
    expected_names+=("$name")
  done < <(stage_node_names)
  for i in $(seq 1 120); do
    nodes="$(curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "http://$MANAGER_PUBLIC:18080/v1/nodes")"
    printf '%s' "$nodes" > "$STATE_DIR/nodes.json"
    stage_nodes="$(jq -r --argjson names "$(json_string_array "${expected_names[@]}")" --arg model "$CMESH_MODEL_ID" '
      $names[] as $name |
      .nodes[] |
      select(.name == $name and .status == "online" and
        ((.resources.models // []) | any(.id == $model and .ready == true)) and
        ((.resources.runtimes // []) | any(.name == "llama.cpp" and ((.stage_runtimes // []) | any(.name == "cmesh-stage-daemon" and .ready == true and .endpoint == "http://127.0.0.1:19781" and .protocol == "cdip.stage-session-v1"))))
      ) |
      "\($name)=\(.id)"
    ' "$STATE_DIR/nodes.json")"
    NODE_STAGE0="$(printf '%s\n' "$stage_nodes" | awk -F= '$1=="real-cdip-stage-0"{print $2; exit}')"
    NODE_STAGE1="$(printf '%s\n' "$stage_nodes" | awk -F= '$1=="real-cdip-stage-1"{print $2; exit}')"
    NODE_STAGE2="$(printf '%s\n' "$stage_nodes" | awk -F= '$1=="real-cdip-stage-2"{print $2; exit}')"
    if { [[ "$CMESH_MANAGER_AS_STAGE_WORKER" != "true" ]] || [[ -n "$NODE_STAGE0" ]]; } && [[ -n "$NODE_STAGE1" && -n "$NODE_STAGE2" ]]; then
      jq -n \
        --arg stage0 "$NODE_STAGE0" \
        --arg stage1 "$NODE_STAGE1" \
        --arg stage2 "$NODE_STAGE2" \
        --argjson n_layer "$N_LAYER" \
        --arg source "worker-services" \
        '{stage0:$stage0,stage1:$stage1,stage2:$stage2,n_layer:$n_layer,source:$source}' > "$STATE_DIR/stage-nodes.json"
      return 0
    fi
    sleep 2
  done
  jq '{nodes:[.nodes[] | {id,name,status,models:.resources.models,runtimes:.resources.runtimes}]}' "$STATE_DIR/nodes.json" >&2 || true
  fail "stage worker services did not report online model inventory"
}

stage_daemon_session_from_host() {
  local host="$1"
  local session_id="$2"
  local target="$3"
  ssh_run "$host" "curl -fsS 'http://127.0.0.1:19781/v1/sessions/$session_id'" > "$target"
}

delete_stage_daemon_session_from_host() {
  local host="$1"
  local session_id="$2"
  local target="$3"
  ssh_run "$host" "curl -fsS -X DELETE 'http://127.0.0.1:19781/v1/sessions/$session_id'" > "$target"
}

verify_service_stage_daemon_sessions() {
  local prefix="$1"
  local jobs_file="$2"
  local expected_decode_steps="$3"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" != "true" ]]; then
    return 0
  fi
  local source_session terminal_session source_node terminal_node stage_info stage_index stage_session stage_node stage_target
  source_session="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[0].input | fromjson | .stage_session_id' "$jobs_file")"
  terminal_session="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[-1].input | fromjson | .stage_session_id' "$jobs_file")"
  source_node="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[0].assigned_to' "$jobs_file")"
  terminal_node="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[-1].assigned_to' "$jobs_file")"
  jq -e '.stage_jobs | all((.input | fromjson | .stage_daemon_url) == "http://127.0.0.1:19781")' "$jobs_file" >/dev/null
  while IFS= read -r stage_info; do
    stage_index="$(jq -r '.index' <<<"$stage_info")"
    stage_session="$(jq -r '.session' <<<"$stage_info")"
    stage_node="$(jq -r '.node' <<<"$stage_info")"
    stage_target="$STATE_DIR/$prefix-stage-$stage_index-daemon-session.json"
    stage_daemon_session_from_host "$(stage_host_for_node "$stage_node")" "$stage_session" "$stage_target"
    jq -e --argjson steps "$expected_decode_steps" '.session.persistent_kv_in_memory == true and .decode_steps == $steps and .last_step >= 1 and .last_payload_bytes > 0' "$stage_target" >/dev/null
  done < <(jq -c '.stage_jobs | sort_by(.input | fromjson | .stage.index)[] | {index:(.input | fromjson | .stage.index), session:(.input | fromjson | .stage_session_id), node:.assigned_to}' "$jobs_file")
  stage_daemon_session_from_host "$(stage_host_for_node "$source_node")" "$source_session" "$STATE_DIR/$prefix-source-daemon-session.json"
  stage_daemon_session_from_host "$(stage_host_for_node "$terminal_node")" "$terminal_session" "$STATE_DIR/$prefix-terminal-daemon-session.json"
  jq -e --argjson steps "$expected_decode_steps" '.session.persistent_kv_in_memory == true and .decode_steps == $steps and .last_step >= 1 and .last_stage_command == "source_decode" and .last_payload_bytes > 0' "$STATE_DIR/$prefix-source-daemon-session.json" >/dev/null
  jq -e --argjson steps "$expected_decode_steps" '.session.persistent_kv_in_memory == true and .decode_steps == $steps and .last_step >= 1 and .last_stage_command == "terminal_decode" and .last_payload_bytes > 0' "$STATE_DIR/$prefix-terminal-daemon-session.json" >/dev/null
}

close_service_stage_daemon_sessions() {
  local prefix="$1"
  local jobs_file="$2"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" != "true" ]]; then
    return 0
  fi
  local stage_info stage_index stage_session stage_node stage_target
  while IFS= read -r stage_info; do
    stage_index="$(jq -r '.index' <<<"$stage_info")"
    stage_session="$(jq -r '.session' <<<"$stage_info")"
    stage_node="$(jq -r '.node' <<<"$stage_info")"
    stage_target="$STATE_DIR/$prefix-stage-$stage_index-daemon-session-close.json"
    delete_stage_daemon_session_from_host "$(stage_host_for_node "$stage_node")" "$stage_session" "$stage_target"
    jq -e --arg session "$stage_session" '.closed == true and .session_id == $session' "$stage_target" >/dev/null
  done < <(jq -c '.stage_jobs | sort_by(.input | fromjson | .stage.index)[] | {index:(.input | fromjson | .stage.index), session:(.input | fromjson | .stage_session_id), node:.assigned_to}' "$jobs_file")
}

verify_stage_prepare_artifact() {
  local job_file="$1"
  local node_id="$2"
  local prefix="$3"
  local artifact_file="$STATE_DIR/$prefix-artifact.json"
  local manifest_file="$STATE_DIR/$prefix-materialization-plan.json"
  local physical_file="$STATE_DIR/$prefix-physical-shard-plan.json"
  local bundle_file="$STATE_DIR/$prefix.cmesh-shard"
  local uri remote_path expected_checksum actual_checksum expected_bytes physical_uri physical_remote_path bundle_uri bundle_remote_path bundle_checksum bundle_actual_checksum bundle_bytes
  if jq -e '
    .status == "succeeded" and
    (.result | fromjson | .kind == "cdip.stage.state") and
    (.result | fromjson | .worker_result.kind == "cdip.stage_ready") and
    (.result | fromjson | .worker_result.artifact.protocol == "cdip.shard-artifact-v1") and
    (.result | fromjson | .worker_result.artifact.status == "physical_stage_gguf_ready") and
    (.result | fromjson | .worker_result.artifact.physical_artifact_ready == true) and
    ((.result | fromjson | .worker_result.artifact.expected_bytes // 0) > 0) and
    ((.result | fromjson | .worker_result.artifact.uri // "") | startswith("file://")) and
    (.result | fromjson | .worker_result.physical_shard_plan.protocol == "cdip.physical-shard-plan-v1") and
    (.result | fromjson | .worker_result.physical_shard_plan.status == "physical_stage_gguf_ready") and
    (.result | fromjson | .worker_result.physical_shard_plan.artifact_kind == "stage_gguf_shard") and
    (.result | fromjson | .worker_result.physical_shard_plan.loadable_gguf == true) and
    (.result | fromjson | .worker_result.physical_shard_plan.physical_artifact_ready == true) and
    ((.result | fromjson | .worker_result.physical_shard_plan.target_uri // "") | startswith("file://"))
  ' "$job_file" >/dev/null; then
    jq '.result | fromjson | .worker_result.artifact' "$job_file" > "$artifact_file"
    jq '.result | fromjson | .worker_result.physical_shard_plan' "$job_file" > "$STATE_DIR/$prefix-physical-shard-plan-result.json"
    uri="$(jq -r '.uri' "$artifact_file")"
    remote_path="${uri#file://}"
    [[ -n "$remote_path" && "$remote_path" != "$uri" ]] || fail "stage GGUF artifact URI is not file:// in $job_file"
    ssh_run "$(stage_host_for_node "$node_id")" "test -s '$remote_path'"
    return 0
  fi
  jq -e '
    .status == "succeeded" and
    (.result | fromjson | .kind == "cdip.stage.state") and
    (.result | fromjson | .worker_result.kind == "cdip.stage_ready") and
    (.result | fromjson | .worker_result.artifact.protocol == "cdip.shard-artifact-v1") and
    (.result | fromjson | .worker_result.artifact.status == "selected_tensor_manifest_ready") and
    (.result | fromjson | .worker_result.artifact.physical_artifact_ready == false) and
    ((.result | fromjson | .worker_result.artifact.expected_bytes // 0) > 0) and
    ((.result | fromjson | .worker_result.artifact.checksum // "") | startswith("sha256:")) and
    ((.result | fromjson | .worker_result.artifact.uri // "") | startswith("file://")) and
    (.result | fromjson | .worker_result.physical_shard_plan.protocol == "cdip.physical-shard-plan-v1") and
    (.result | fromjson | .worker_result.physical_shard_plan.status == "physical_tensor_bundle_ready_not_loadable_gguf") and
    (.result | fromjson | .worker_result.physical_shard_plan.artifact_kind == "cmesh_shard_bundle") and
    (.result | fromjson | .worker_result.physical_shard_plan.loadable_gguf == false) and
    (.result | fromjson | .worker_result.physical_shard_plan.physical_artifact_ready == true) and
    ((.result | fromjson | .worker_result.physical_shard_plan.target_checksum // "") | startswith("sha256:")) and
    ((.result | fromjson | .worker_result.physical_shard_plan.artifact_bytes // 0) > 0) and
    ((.result | fromjson | .worker_result.physical_shard_plan.plan_uri // "") | startswith("file://")) and
    ((.result | fromjson | .worker_result.physical_shard_plan.target_uri // "") | startswith("file://"))
  ' "$job_file" >/dev/null
  jq '.result | fromjson | .worker_result.artifact' "$job_file" > "$artifact_file"
  jq '.result | fromjson | .worker_result.physical_shard_plan' "$job_file" > "$STATE_DIR/$prefix-physical-shard-plan-result.json"
  uri="$(jq -r '.uri' "$artifact_file")"
  remote_path="${uri#file://}"
  [[ -n "$remote_path" && "$remote_path" != "$uri" ]] || fail "prepare artifact URI is not file:// in $job_file"
  scp_from_sudo "$(stage_host_for_node "$node_id")" "$remote_path" "$manifest_file"
  physical_uri="$(jq -r '.plan_uri' "$STATE_DIR/$prefix-physical-shard-plan-result.json")"
  physical_remote_path="${physical_uri#file://}"
  [[ -n "$physical_remote_path" && "$physical_remote_path" != "$physical_uri" ]] || fail "physical shard plan URI is not file:// in $job_file"
  scp_from_sudo "$(stage_host_for_node "$node_id")" "$physical_remote_path" "$physical_file"
  expected_checksum="$(jq -r '.checksum' "$artifact_file")"
  actual_checksum="sha256:$(file_sha256 "$manifest_file")"
  [[ "$actual_checksum" == "$expected_checksum" ]] || fail "prepare artifact checksum mismatch for $prefix: expected $expected_checksum got $actual_checksum"
  expected_bytes="$(jq -r '.expected_bytes' "$artifact_file")"
  jq -e --argjson expected_bytes "$expected_bytes" '
    .protocol == "cdip.stage-materialization-plan-v1" and
    .manifest_only == true and
    .selected_bytes == $expected_bytes and
    (.selected_tensor_count == (.stage_tensor_count + .boundary_tensor_count)) and
    .selected_tensor_materialization_ready == true and
    .materialization_probe.requested == true and
    .materialization_probe.attempted == true and
    .materialization_probe.loaded == true and
    .materialization_probe.status == "loaded" and
    .materialization_probe.selected_tensor_count == .selected_tensor_count and
    .materialization_probe.selected_bytes == .selected_bytes
  ' "$manifest_file" >/dev/null
  bundle_uri="$(jq -r '.target_uri' "$physical_file")"
  bundle_remote_path="${bundle_uri#file://}"
  [[ -n "$bundle_remote_path" && "$bundle_remote_path" != "$bundle_uri" ]] || fail "physical bundle URI is not file:// in $physical_file"
  scp_from_sudo "$(stage_host_for_node "$node_id")" "$bundle_remote_path" "$bundle_file"
  bundle_checksum="$(jq -r '.target_checksum' "$physical_file")"
  bundle_actual_checksum="sha256:$(file_sha256 "$bundle_file")"
  [[ "$bundle_actual_checksum" == "$bundle_checksum" ]] || fail "physical bundle checksum mismatch for $prefix: expected $bundle_checksum got $bundle_actual_checksum"
  bundle_bytes="$(jq -r '.artifact_bytes' "$physical_file")"
  [[ "$(wc -c < "$bundle_file" | tr -d ' ')" == "$bundle_bytes" ]] || fail "physical bundle byte count mismatch for $prefix"
  head -c 22 "$bundle_file" | grep -q "CMESH_SHARD_BUNDLE_V1" || fail "physical bundle magic header missing for $prefix"
  jq -e --arg manifest_uri "$uri" --arg manifest_checksum "$expected_checksum" --arg physical_uri "$physical_uri" --arg bundle_uri "$bundle_uri" --arg bundle_checksum "$bundle_checksum" --argjson expected_bytes "$expected_bytes" '
    .protocol == "cdip.physical-shard-plan-v1" and
    .status == "physical_tensor_bundle_ready_not_loadable_gguf" and
    .artifact_kind == "cmesh_shard_bundle" and
    .loadable_gguf == false and
    .physical_artifact_ready == true and
    .selected_tensor_manifest_uri == $manifest_uri and
    .selected_tensor_manifest_checksum == $manifest_checksum and
    .selected_bytes == $expected_bytes and
    .plan_uri == $physical_uri and
    .target_uri == $bundle_uri and
    .target_checksum == $bundle_checksum and
    (.blockers | length) > 0
  ' "$physical_file" >/dev/null
}

verify_memory_aware_placement_plan() {
  if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" != "true" ]]; then
    return 0
  fi
  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" \
    "http://$MANAGER_PUBLIC:18080/v1/models/$CMESH_PLACEMENT_PROOF_MODEL_ID/distributed-plan" \
    > "$STATE_DIR/memory-aware-placement-plan.json"
  jq -e '
    .plan.feasible == true and
    .plan.executable_now == true and
    ((.plan.blockers // []) | length) == 0 and
    (.plan.stages | length) == 3 and
    .plan.placement.strategy == "memory_disk_weighted_layers" and
    .plan.placement.total_layers == .plan.total_layers and
    (.plan.placement.candidates | length) >= 3 and
    (.plan.placement.candidates | map(select(.selected == true)) | length) == 3 and
    (.plan.placement.candidates | map(select(.selected == true)) | all(.assigned_layers >= 1 and .assigned_layers <= .layer_capacity and .assigned_memory_bytes <= .allowed_memory_bytes and .assigned_disk_bytes <= .effective_storage_bytes)) and
    (.plan.required_memory_bytes > ([.plan.stages[].allowed_memory_bytes] | max)) and
    (.plan.stages | all(.memory_bytes <= .allowed_memory_bytes and .disk_bytes <= .allowed_storage_bytes and .layers >= 1)) and
    ([.plan.stages[].layer_start] | min) == 0 and
    ([.plan.stages[].layer_end] | max) == (.plan.total_layers - 1)
  ' "$STATE_DIR/memory-aware-placement-plan.json" >/dev/null
}

stage_host_for_node() {
  local node_id="$1"
  if [[ -n "$NODE_STAGE0" && "$node_id" == "$NODE_STAGE0" ]]; then
    printf '%s\n' "$MANAGER_PUBLIC"
  elif [[ "$node_id" == "$NODE_STAGE1" ]]; then
    printf '%s\n' "$STAGE1_PUBLIC"
  elif [[ "$node_id" == "$NODE_STAGE2" ]]; then
    printf '%s\n' "$STAGE2_PUBLIC"
  else
    fail "unknown stage node id $node_id"
  fi
}

main() {
  need aws
  need curl
  need jq
  need ssh
  need scp
  if [[ -z "$PACKAGE_DIR" ]]; then
    need go
  fi

  if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" && -n "$CMESH_STAGE_RUNTIME_URL" ]]; then
    fail "set only one of CMESH_STAGE_RUNTIME_ARCHIVE or CMESH_STAGE_RUNTIME_URL"
  fi
  if [[ "$CMESH_REQUIRE_STAGE_RUNTIME_ARTIFACT" == "true" && -z "$CMESH_STAGE_RUNTIME_ARCHIVE" && -z "$CMESH_STAGE_RUNTIME_URL" ]]; then
    fail "production sliced E2E requires CMESH_STAGE_RUNTIME_ARCHIVE or CMESH_STAGE_RUNTIME_URL; refusing remote llama.cpp compile fallback"
  fi
  if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" && ! -f "$CMESH_STAGE_RUNTIME_ARCHIVE" ]]; then
    fail "CMESH_STAGE_RUNTIME_ARCHIVE does not exist: $CMESH_STAGE_RUNTIME_ARCHIVE"
  fi

  mkdir -p "$STATE_DIR" "$ROOT_DIR/dist"
  if [[ -n "$PACKAGE_DIR" ]]; then
    [[ -f "$PACKAGE_DIR/cmesh-linux-amd64" ]] || fail "package missing cmesh-linux-amd64: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/install-manager-linux.sh" ]] || fail "package missing install-manager-linux.sh: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/install-worker.sh" ]] || fail "package missing install-worker.sh: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz" ]] || fail "package missing stage runtime archive: $PACKAGE_DIR"
    [[ -f "$PACKAGE_DIR/llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz.sha256" ]] || fail "package missing stage runtime checksum: $PACKAGE_DIR"
    (cd "$PACKAGE_DIR" && shasum -a 256 -c checksums.txt >/dev/null)
    jq -e '.kind == "cmesh.linux.production.release.v1"' "$PACKAGE_DIR/manifest.json" >/dev/null
    jq -n --arg package_dir "$PACKAGE_DIR" '{package_dir:$package_dir, source:"linux-production-package"}' > "$STATE_DIR/package.json"
  fi
  if [[ -n "$CMESH_STAGE_RUNTIME_ARCHIVE" ]]; then
    "$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh" "$CMESH_STAGE_RUNTIME_ARCHIVE" > "$STATE_DIR/stage-runtime-archive-verify.txt"
    STAGE_RUNTIME_ARCHIVE_SHA256="$(file_sha256 "$CMESH_STAGE_RUNTIME_ARCHIVE")"
    printf '%s  %s\n' "$STAGE_RUNTIME_ARCHIVE_SHA256" "$CMESH_STAGE_RUNTIME_ARCHIVE" > "$STATE_DIR/stage-runtime-archive-sha256.txt"
  fi
  if [[ "$CMESH_AWS_INSTANCE_COUNT" -ne 3 ]]; then
    fail "CMESH_AWS_INSTANCE_COUNT must be exactly 3 for this E2E"
  fi
  require_allowed_instance_type
  cat > "$STATE_DIR/config.json" <<EOF
{
  "region": "$CMESH_AWS_REGION",
  "vpc_id": "$CMESH_AWS_VPC_ID",
  "subnet_id": "$CMESH_AWS_SUBNET_ID",
  "instance_type": "$CMESH_AWS_INSTANCE_TYPE",
  "instance_count": $CMESH_AWS_INSTANCE_COUNT,
  "volume_size_gb": $CMESH_AWS_VOLUME_SIZE,
  "model_id": "$CMESH_MODEL_ID",
  "model_url": "$CMESH_MODEL_URL",
  "model_file": "$CMESH_MODEL_FILE",
  "expected_model_layers": $CMESH_EXPECTED_MODEL_LAYERS,
  "stage_runtime_archive": "$CMESH_STAGE_RUNTIME_ARCHIVE",
  "stage_runtime_archive_sha256": "$STAGE_RUNTIME_ARCHIVE_SHA256",
  "stage_runtime_url": "$CMESH_STAGE_RUNTIME_URL",
  "linux_package_dir": "$PACKAGE_DIR",
  "install_manager_service": $CMESH_INSTALL_MANAGER_SERVICE,
  "install_stage_worker_services": $CMESH_INSTALL_STAGE_WORKER_SERVICES,
  "manager_as_stage_worker": $CMESH_MANAGER_AS_STAGE_WORKER,
  "stage_worker_memory_gb": $CMESH_STAGE_WORKER_MEMORY_GB,
  "stage_worker_disk_gb": $CMESH_STAGE_WORKER_DISK_GB,
  "placement_proof_model_id": "$CMESH_PLACEMENT_PROOF_MODEL_ID",
  "require_memory_pressure_execution": $CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION,
  "preflight_only": $CMESH_PREFLIGHT_ONLY,
  "keep_resources": $CMESH_KEEP_AWS_RESOURCES
}
EOF
  if [[ "$CMESH_PREFLIGHT_ONLY" == "true" ]]; then
    echo "PASS: AWS CDIP real GGUF E2E preflight succeeded"
    echo "evidence: $STATE_DIR"
    return 0
  fi
  aws sts get-caller-identity --output json > "$STATE_DIR/aws-identity.json"

  if [[ -z "$PACKAGE_DIR" ]]; then
    echo "building cmesh linux/amd64 binary"
    (
      cd "$ROOT_DIR"
      GOOS=linux GOARCH=amd64 go build \
        -ldflags "-X github.com/cmesh/cmesh/internal/version.Version=cdip-real-e2e -X github.com/cmesh/cmesh/internal/version.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo unknown) -X github.com/cmesh/cmesh/internal/version.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        -o dist/cmesh-linux-amd64 ./cmd/cmesh
    )
  fi

  if [[ -z "$CMESH_STAGE_RUNTIME_ARCHIVE" && -z "$CMESH_STAGE_RUNTIME_URL" ]]; then
    echo "packing stage-runner source"
    COPYFILE_DISABLE=1 tar -C "$ROOT_DIR" -czf "$STATE_DIR/cmesh-stage-source.tar.gz" \
      scripts/prepare-llamacpp-stage-runner-worktree.sh \
      integrations/llamacpp/cmesh-stage-runner \
      integrations/llamacpp/patches
  fi

  local ami_id my_ip
  ami_id="$CMESH_AWS_AMI_ID"
  if [[ -z "$ami_id" ]]; then
    ami_id="$(latest_ubuntu_ami)"
  fi
  my_ip="$(curl -fsS https://checkip.amazonaws.com | tr -d '\n')"

  echo "creating AWS resources for $RUN_ID"
  aws ec2 create-key-pair --region "$CMESH_AWS_REGION" --key-name "$KEY_NAME" --query 'KeyMaterial' --output text > "$KEY_PATH"
  chmod 600 "$KEY_PATH"

  SG_ID="$(aws ec2 create-security-group --region "$CMESH_AWS_REGION" --group-name "$RUN_ID-sg" --description "CMesh CDIP real GGUF E2E $RUN_ID" --vpc-id "$CMESH_AWS_VPC_ID" --query 'GroupId' --output text)"
  echo "$SG_ID" > "$STATE_DIR/sg_id"
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
  printf '%s\n' "${INSTANCE_IDS[@]}" > "$STATE_DIR/instance_ids"
  aws ec2 wait instance-running --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}"
  aws ec2 describe-instances --region "$CMESH_AWS_REGION" --instance-ids "${INSTANCE_IDS[@]}" --query 'Reservations[].Instances[].{ID:InstanceId,Public:PublicIpAddress,Private:PrivateIpAddress,State:State.Name}' --output json > "$STATE_DIR/instances.json"

  MANAGER_PUBLIC="$(jq -r '.[0].Public' "$STATE_DIR/instances.json")"
  MANAGER_PRIVATE="$(jq -r '.[0].Private' "$STATE_DIR/instances.json")"
  STAGE1_PUBLIC="$(jq -r '.[1].Public' "$STATE_DIR/instances.json")"
  STAGE1_PRIVATE="$(jq -r '.[1].Private' "$STATE_DIR/instances.json")"
  STAGE2_PUBLIC="$(jq -r '.[2].Public' "$STATE_DIR/instances.json")"
  STAGE2_PRIVATE="$(jq -r '.[2].Private' "$STATE_DIR/instances.json")"
  cat > "$STATE_DIR/roles.env" <<EOF
MANAGER_PUBLIC=$MANAGER_PUBLIC
MANAGER_PRIVATE=$MANAGER_PRIVATE
STAGE1_PUBLIC=$STAGE1_PUBLIC
STAGE1_PRIVATE=$STAGE1_PRIVATE
STAGE2_PUBLIC=$STAGE2_PUBLIC
STAGE2_PRIVATE=$STAGE2_PRIVATE
EOF

  for host in "$MANAGER_PUBLIC" "$STAGE1_PUBLIC" "$STAGE2_PUBLIC"; do
    echo "preparing $host"
    wait_for_ssh "$host"
    ssh_run "$host" "sudo mkdir -p '$REMOTE_ROOT' '$REMOTE_SOURCE' /var/lib/cmesh/models '$REMOTE_STAGE_SHARDS' /var/lib/cmesh/stage-runner && sudo chown -R $CMESH_AWS_SSH_USER:$CMESH_AWS_SSH_USER '$REMOTE_ROOT' /var/lib/cmesh"
    if [[ -n "$PACKAGE_DIR" ]]; then
      scp_to "$PACKAGE_DIR/cmesh-linux-amd64" "$host" "$REMOTE_BIN"
      scp_to "$PACKAGE_DIR/install-manager-linux.sh" "$host" "$REMOTE_ROOT/install-manager-linux.sh"
      scp_to "$PACKAGE_DIR/install-worker.sh" "$host" "$REMOTE_ROOT/install-worker.sh"
      scp_to "$PACKAGE_DIR/manifest.json" "$host" "$REMOTE_ROOT/manifest.json"
      scp_to "$PACKAGE_DIR/checksums.txt" "$host" "$REMOTE_ROOT/checksums.txt"
    else
      scp_to "$ROOT_DIR/dist/cmesh-linux-amd64" "$host" "$REMOTE_BIN"
      scp_to "$ROOT_DIR/scripts/install-manager-linux.sh" "$host" "$REMOTE_ROOT/install-manager-linux.sh"
      scp_to "$ROOT_DIR/scripts/install-worker.sh" "$host" "$REMOTE_ROOT/install-worker.sh"
    fi
    if [[ -f "$STATE_DIR/cmesh-stage-source.tar.gz" ]]; then
      scp_to "$STATE_DIR/cmesh-stage-source.tar.gz" "$host" "$REMOTE_ROOT/cmesh-stage-source.tar.gz"
      ssh_run "$host" "chmod +x '$REMOTE_BIN' '$REMOTE_ROOT/install-manager-linux.sh' '$REMOTE_ROOT/install-worker.sh' && tar -C '$REMOTE_SOURCE' -xzf '$REMOTE_ROOT/cmesh-stage-source.tar.gz' && '$REMOTE_BIN' version"
    else
      ssh_run "$host" "chmod +x '$REMOTE_BIN' '$REMOTE_ROOT/install-manager-linux.sh' '$REMOTE_ROOT/install-worker.sh' && '$REMOTE_BIN' version"
    fi
  done

  stage_prepare_pids=()
  if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
    prepare_stage_host "$MANAGER_PUBLIC" &
    stage_prepare_pids+=($!)
  fi
  prepare_stage_host "$STAGE1_PUBLIC" &
  stage_prepare_pids+=($!)
  prepare_stage_host "$STAGE2_PUBLIC" &
  stage_prepare_pids+=($!)
  for pid in "${stage_prepare_pids[@]}"; do
    wait "$pid"
  done

  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
    N_LAYER="$CMESH_EXPECTED_MODEL_LAYERS"
    jq -n --argjson n_layer "$N_LAYER" --arg source "expected-model-layers" '{n_layer:$n_layer,source:$source}' > "$STATE_DIR/model-prepare.json"
  else
    N_LAYER="$(ssh_run "$STAGE1_PUBLIC" "'$REMOTE_RUNNER' --command prepare --model '$REMOTE_MODEL' --stage-start 0 --stage-end 0 --stage-index 0" | tee "$STATE_DIR/model-prepare.json" | jq -r '.n_layer')"
  fi
  [[ -n "$N_LAYER" && "$N_LAYER" != "null" && "$N_LAYER" -ge 2 ]] || fail "expected model with at least 2 layers, got n_layer=$N_LAYER"
  prepare_remote_stage_gguf_shards

  JOIN_TOKEN="$(openssl rand -hex 32)"
  OPERATOR_TOKEN="$(openssl rand -hex 32)"
  echo "$JOIN_TOKEN" > "$STATE_DIR/join-token.txt"

  echo "starting manager"
  if [[ "$CMESH_INSTALL_MANAGER_SERVICE" == "true" ]]; then
    ssh_run "$MANAGER_PUBLIC" "sudo env CMESH_BINARY_URL=file://$REMOTE_BIN CMESH_NONINTERACTIVE=true CMESH_ADDR=0.0.0.0:18080 CMESH_PUBLIC_URL=http://$MANAGER_PUBLIC:18080 CMESH_JOIN_TOKEN=$JOIN_TOKEN CMESH_OPERATOR_TOKEN=$OPERATOR_TOKEN CMESH_EXTRA_MANAGER_ARGS='--cdip-auto-advance=false' '$REMOTE_ROOT/install-manager-linux.sh' > '$REMOTE_ROOT/install-manager.log' 2>&1"
    ssh_run "$MANAGER_PUBLIC" "systemctl is-active cmesh.service" > "$STATE_DIR/manager-service.txt"
  else
    ssh_run "$MANAGER_PUBLIC" "nohup '$REMOTE_BIN' manager start --memory --addr 0.0.0.0:18080 --join-token '$JOIN_TOKEN' --operator-token '$OPERATOR_TOKEN' --public-url 'http://$MANAGER_PUBLIC:18080' --cdip-auto-advance=false > '$REMOTE_ROOT/manager.log' 2>&1 &"
  fi
  wait_for_manager
  curl -fsS "http://$MANAGER_PUBLIC:18080/health" | tee "$STATE_DIR/health.json"

  echo "registering stage workers"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
    if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
      install_stage_worker_service "$MANAGER_PUBLIC" real-cdip-stage-0
    fi
    install_stage_worker_service "$STAGE1_PUBLIC" real-cdip-stage-1
    install_stage_worker_service "$STAGE2_PUBLIC" real-cdip-stage-2
    wait_for_stage_service_workers
    verify_memory_aware_placement_plan
  else
    if [[ "$CMESH_MANAGER_AS_STAGE_WORKER" == "true" ]]; then
      NODE_STAGE0="$(register_stage_worker real-cdip-stage-0 "$N_LAYER")"
    fi
    NODE_STAGE1="$(register_stage_worker real-cdip-stage-1 "$N_LAYER")"
    NODE_STAGE2="$(register_stage_worker real-cdip-stage-2 "$N_LAYER")"
    cat > "$STATE_DIR/stage-nodes.json" <<EOF
{"stage0":"$NODE_STAGE0","stage1":"$NODE_STAGE1","stage2":"$NODE_STAGE2","n_layer":$N_LAYER,"source":"synthetic-registration"}
EOF
  fi

  echo "creating distributed CDIP job"
  jq -n \
    --arg prompt "$CMESH_PROMPT" \
    --arg runner "$REMOTE_RUNNER" \
    --argjson stage_model_paths "$(stage_model_paths_json)" \
    --argjson stage_node_ids "$(stage_node_ids_json)" \
    --arg work_dir "$REMOTE_STAGE_WORK" \
    --argjson total_layers "$N_LAYER" \
    --argjson timeout_ms "$CMESH_STAGE_TIMEOUT_MS" \
    '{prompt:$prompt,max_tokens:1,temperature:"0.1",stage_runner_bin:$runner,stage_model_paths:$stage_model_paths,stage_node_ids:$stage_node_ids,work_dir:$work_dir,total_layers:$total_layers,timeout_ms:$timeout_ms}' \
    > "$STATE_DIR/distributed-generate-request.json"
  post_manager_json "/v1/models/$CMESH_MODEL_ID/distributed-generate" \
    "$STATE_DIR/distributed-generate-request.json" \
    "$STATE_DIR/distributed-generate.json"
  parent_id="$(jq -r '.job.id' "$STATE_DIR/distributed-generate.json")"
  jq -e --argjson total_layers "$N_LAYER" '.plan.total_layers == $total_layers' "$STATE_DIR/distributed-generate.json" >/dev/null
  jq -e --argjson paths "$(stage_model_paths_json)" --argjson nodes "$(stage_node_ids_json)" '
    (.stage_jobs | length) == 3 and
    ([.stage_jobs[] | (.input | fromjson).model_path] == $paths) and
    ([.stage_jobs[] | .assigned_to] == $nodes)
  ' "$STATE_DIR/distributed-generate.json" >/dev/null
  jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$STATE_DIR/distributed-generate.json" > "$STATE_DIR/stage-workers.jsonl"

  echo "preparing remote stages"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${parent_id}/prepare" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{}' > "$STATE_DIR/prepare.json"
  while IFS= read -r stage_worker; do
    node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
    stage_id="$(jq -r '.id' <<<"$stage_worker")"
    if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
      wait_for_job_state "$stage_id" "$STATE_DIR/after-prepare-$stage_id.json" "succeeded" "ready"
      verify_stage_prepare_artifact "$STATE_DIR/after-prepare-$stage_id.json" "$node_id" "prepare-$stage_id"
      continue
    fi
    run_poll_once "$(stage_host_for_node "$node_id")" "$node_id" "prepare-$node_id"
    record_job "$stage_id" "$STATE_DIR/after-prepare-$stage_id.json"
    require_job_state "$STATE_DIR/after-prepare-$stage_id.json" "succeeded" "ready"
    verify_stage_prepare_artifact "$STATE_DIR/after-prepare-$stage_id.json" "$node_id" "prepare-$stage_id"
  done < "$STATE_DIR/stage-workers.jsonl"

  echo "running remote source, relay, and terminal decode"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${parent_id}/prefill" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{}' > "$STATE_DIR/prefill.json"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${parent_id}/decode" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{"mode":"relay_decode","step":1}' > "$STATE_DIR/decode.json"

  source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$STATE_DIR/decode.json")"
  terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$STATE_DIR/decode.json")"
  source_stage="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].id' "$STATE_DIR/decode.json")"
  terminal_stage="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].id' "$STATE_DIR/decode.json")"
  jq -c '.stage_jobs | sort_by(.cdip_stage_index)[] | {id, assigned_to, index:.cdip_stage_index}' "$STATE_DIR/decode.json" > "$STATE_DIR/decode-stage-workers.jsonl"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
    while IFS= read -r stage_worker; do
      node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
      stage_id="$(jq -r '.id' <<<"$stage_worker")"
      stage_index="$(jq -r '.index' <<<"$stage_worker")"
      wait_for_job_state "$stage_id" "$STATE_DIR/after-decode-stage-$stage_index-$stage_id.json" "succeeded" "decode"
    done < "$STATE_DIR/decode-stage-workers.jsonl"
  else
    while IFS= read -r stage_worker; do
      node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
      stage_id="$(jq -r '.id' <<<"$stage_worker")"
      stage_index="$(jq -r '.index' <<<"$stage_worker")"
      run_poll_once "$(stage_host_for_node "$node_id")" "$node_id" "decode-stage-$stage_index-$node_id"
      record_job "$stage_id" "$STATE_DIR/after-decode-stage-$stage_index-$stage_id.json"
      require_job_state "$STATE_DIR/after-decode-stage-$stage_index-$stage_id.json" "succeeded" "decode"
    done < "$STATE_DIR/decode-stage-workers.jsonl"
  fi

  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "http://$MANAGER_PUBLIC:18080/v1/jobs/$parent_id" > "$STATE_DIR/parent.json"
  jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and ((.result | fromjson | .tokens | length) >= 1)' "$STATE_DIR/parent.json" >/dev/null
  jq -e '(.result | fromjson | .execution_mode == "resident-stage-daemon" and .resident_kv_in_memory == true and .runner_mode == "llama.cpp-stage-daemon")' "$STATE_DIR/parent.json" >/dev/null
  verify_service_stage_daemon_sessions "single-decode" "$STATE_DIR/distributed-generate.json" 1
  close_service_stage_daemon_sessions "single-decode" "$STATE_DIR/distributed-generate.json"

  echo "creating distributed CDIP decode-loop dispatch job"
  jq -n \
    --arg prompt "$CMESH_PROMPT" \
    --arg runner "$REMOTE_RUNNER" \
    --argjson stage_model_paths "$(stage_model_paths_json)" \
    --argjson stage_node_ids "$(stage_node_ids_json)" \
    --arg work_dir "$REMOTE_STAGE_WORK-dispatch" \
    --argjson total_layers "$N_LAYER" \
    --argjson timeout_ms "$CMESH_STAGE_TIMEOUT_MS" \
    '{prompt:$prompt,max_tokens:3,temperature:"0.1",stage_runner_bin:$runner,stage_model_paths:$stage_model_paths,stage_node_ids:$stage_node_ids,work_dir:$work_dir,total_layers:$total_layers,timeout_ms:$timeout_ms}' \
    > "$STATE_DIR/dispatch-distributed-generate-request.json"
  post_manager_json "/v1/models/$CMESH_MODEL_ID/distributed-generate" \
    "$STATE_DIR/dispatch-distributed-generate-request.json" \
    "$STATE_DIR/dispatch-distributed-generate.json"
  dispatch_parent_id="$(jq -r '.job.id' "$STATE_DIR/dispatch-distributed-generate.json")"
  jq -e --argjson total_layers "$N_LAYER" '.plan.total_layers == $total_layers' "$STATE_DIR/dispatch-distributed-generate.json" >/dev/null
  jq -e --argjson paths "$(stage_model_paths_json)" --argjson nodes "$(stage_node_ids_json)" '
    (.stage_jobs | length) == 3 and
    ([.stage_jobs[] | (.input | fromjson).model_path] == $paths) and
    ([.stage_jobs[] | .assigned_to] == $nodes)
  ' "$STATE_DIR/dispatch-distributed-generate.json" >/dev/null
  jq -c '.stage_jobs[] | {id, assigned_to, index:.cdip_stage_index}' "$STATE_DIR/dispatch-distributed-generate.json" > "$STATE_DIR/dispatch-stage-workers.jsonl"

  echo "preparing remote dispatch stages"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${dispatch_parent_id}/prepare" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{}' > "$STATE_DIR/dispatch-prepare.json"
  while IFS= read -r stage_worker; do
    node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
    stage_id="$(jq -r '.id' <<<"$stage_worker")"
    if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
      wait_for_job_state "$stage_id" "$STATE_DIR/dispatch-after-prepare-$stage_id.json" "succeeded" "ready"
      verify_stage_prepare_artifact "$STATE_DIR/dispatch-after-prepare-$stage_id.json" "$node_id" "dispatch-prepare-$stage_id"
      continue
    fi
    run_poll_once "$(stage_host_for_node "$node_id")" "$node_id" "dispatch-prepare-$node_id"
    record_job "$stage_id" "$STATE_DIR/dispatch-after-prepare-$stage_id.json"
    require_job_state "$STATE_DIR/dispatch-after-prepare-$stage_id.json" "succeeded" "ready"
    verify_stage_prepare_artifact "$STATE_DIR/dispatch-after-prepare-$stage_id.json" "$node_id" "dispatch-prepare-$stage_id"
  done < "$STATE_DIR/dispatch-stage-workers.jsonl"

  echo "running remote decode-loop dispatch step"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${dispatch_parent_id}/prefill" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{}' > "$STATE_DIR/dispatch-prefill.json"
  curl -fsS -X POST "http://$MANAGER_PUBLIC:18080/v1/cdip/jobs/${dispatch_parent_id}/decode-loop" -H "Authorization: Bearer $OPERATOR_TOKEN" -H 'Content-Type: application/json' -d '{"mode":"dispatch","step":2,"max_tokens":3,"terminal_force_final":false}' > "$STATE_DIR/dispatch-decode-loop.json"
  jq -e --arg parent "$dispatch_parent_id" '
    .parent_job.id == $parent and
    .parent_job.status != "succeeded" and
    .trace.mode == "worker-dispatch" and
    .trace.kv_cache_key == ("cdip-session-" + $parent + ":kv") and
    (.messages | length) >= 2 and
    (.messages | all(.step == 2)) and
    (.stage_jobs | all(.status == "scheduled" and .cdip_state == "decode"))
  ' "$STATE_DIR/dispatch-decode-loop.json" >/dev/null

  dispatch_source_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].assigned_to' "$STATE_DIR/dispatch-decode-loop.json")"
  dispatch_terminal_node="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].assigned_to' "$STATE_DIR/dispatch-decode-loop.json")"
  dispatch_source_stage="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[0].id' "$STATE_DIR/dispatch-decode-loop.json")"
  dispatch_terminal_stage="$(jq -r '.stage_jobs | sort_by(.cdip_stage_index) | .[-1].id' "$STATE_DIR/dispatch-decode-loop.json")"
  jq -c '.stage_jobs | sort_by(.cdip_stage_index)[] | {id, assigned_to, index:.cdip_stage_index}' "$STATE_DIR/dispatch-decode-loop.json" > "$STATE_DIR/dispatch-step2-stage-workers.jsonl"
  if [[ "$CMESH_INSTALL_STAGE_WORKER_SERVICES" == "true" ]]; then
    wait_for_dispatch_step "$dispatch_parent_id" "$STATE_DIR/dispatch-after-partial-jobs.json" 3 "cdip-session-${dispatch_parent_id}:kv"
    record_job "$dispatch_parent_id" "$STATE_DIR/dispatch-parent-after-partial.json"
    wait_for_parent_status "$dispatch_parent_id" "$STATE_DIR/dispatch-parent-final.json" "succeeded"
  else
    while IFS= read -r stage_worker; do
      node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
      stage_id="$(jq -r '.id' <<<"$stage_worker")"
      stage_index="$(jq -r '.index' <<<"$stage_worker")"
      run_poll_once "$(stage_host_for_node "$node_id")" "$node_id" "dispatch-step2-stage-$stage_index-$node_id"
      record_job "$stage_id" "$STATE_DIR/dispatch-after-step2-stage-$stage_index-$stage_id.json"
      require_dispatch_stage_step_done_or_advanced "$STATE_DIR/dispatch-after-step2-stage-$stage_index-$stage_id.json" 2
    done < "$STATE_DIR/dispatch-step2-stage-workers.jsonl"
    record_job "$dispatch_parent_id" "$STATE_DIR/dispatch-parent-after-partial.json"
    jq -e '.status != "succeeded"' "$STATE_DIR/dispatch-parent-after-partial.json" >/dev/null
    wait_for_dispatch_step "$dispatch_parent_id" "$STATE_DIR/dispatch-after-partial-jobs.json" 3 "cdip-session-${dispatch_parent_id}:kv"
    jq -c --arg parent "$dispatch_parent_id" '[.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent)] | sort_by(.cdip_stage_index)[] | {id, assigned_to, index:.cdip_stage_index}' "$STATE_DIR/dispatch-after-partial-jobs.json" > "$STATE_DIR/dispatch-step3-stage-workers.jsonl"
    while IFS= read -r stage_worker; do
      node_id="$(jq -r '.assigned_to' <<<"$stage_worker")"
      stage_id="$(jq -r '.id' <<<"$stage_worker")"
      stage_index="$(jq -r '.index' <<<"$stage_worker")"
      run_poll_once "$(stage_host_for_node "$node_id")" "$node_id" "dispatch-step3-stage-$stage_index-$node_id"
      record_job "$stage_id" "$STATE_DIR/dispatch-after-step3-stage-$stage_index-$stage_id.json"
      require_job_state "$STATE_DIR/dispatch-after-step3-stage-$stage_index-$stage_id.json" "succeeded" "decode"
    done < "$STATE_DIR/dispatch-step3-stage-workers.jsonl"
    wait_for_parent_status "$dispatch_parent_id" "$STATE_DIR/dispatch-parent-final.json" "succeeded"
  fi

  cp "$STATE_DIR/dispatch-parent-final.json" "$STATE_DIR/dispatch-parent.json"
  jq -e '.status == "succeeded" and (.result | fromjson | .kind == "cdip.distributed_terminal_result") and ((.result | fromjson | .tokens | length) >= 1) and (.result | fromjson | .step == 3)' "$STATE_DIR/dispatch-parent.json" >/dev/null
  jq -e '(.result | fromjson | .execution_mode == "resident-stage-daemon" and .resident_kv_in_memory == true and .runner_mode == "llama.cpp-stage-daemon")' "$STATE_DIR/dispatch-parent.json" >/dev/null
  verify_service_stage_daemon_sessions "dispatch-loop" "$STATE_DIR/dispatch-distributed-generate.json" 2
  dispatch_source_index="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[0].input | fromjson | .stage.index' "$STATE_DIR/dispatch-decode-loop.json")"
  dispatch_terminal_index="$(jq -r '.stage_jobs | sort_by(.input | fromjson | .stage.index) | .[-1].input | fromjson | .stage.index' "$STATE_DIR/dispatch-decode-loop.json")"
  collect_dispatch_runner_reports "$(stage_host_for_node "$dispatch_source_node")" "$dispatch_source_index" "$(stage_host_for_node "$dispatch_terminal_node")" "$dispatch_terminal_index"
  curl -fsS -H "Authorization: Bearer $OPERATOR_TOKEN" "http://$MANAGER_PUBLIC:18080/v1/jobs" > "$STATE_DIR/dispatch-jobs.json"
  jq -e --arg parent "$dispatch_parent_id" --arg kv "cdip-session-${dispatch_parent_id}:kv" '
    [.jobs[] | select(.type == "model.generate.distributed.stage" and .cdip_parent_job_id == $parent) | (.input | fromjson)]
    | length >= 2
    and all(.step == 3 and .kv_cache_key == $kv)
    and any(.stage_command == "source_decode")
    and any(.stage_command == "terminal_decode")
  ' "$STATE_DIR/dispatch-jobs.json" >/dev/null

  jq -n \
    --arg model "$REMOTE_MODEL" \
    --arg model_id "$CMESH_MODEL_ID" \
    --arg model_url "$CMESH_MODEL_URL" \
    --arg model_file "$CMESH_MODEL_FILE" \
    --arg placement_proof_model_id "$CMESH_PLACEMENT_PROOF_MODEL_ID" \
    --arg parent "$parent_id" \
    --arg dispatch_parent "$dispatch_parent_id" \
    --argjson n_layer "$N_LAYER" \
    --argjson placement_proof "$(if [[ -f "$STATE_DIR/memory-aware-placement-plan.json" ]]; then jq -c '.plan' "$STATE_DIR/memory-aware-placement-plan.json"; else printf 'null'; fi)" \
    --slurpfile parent_json "$STATE_DIR/parent.json" \
    --slurpfile dispatch_parent_json "$STATE_DIR/dispatch-parent.json" \
    '{
      model:$model,
      model_id:$model_id,
      model_url:$model_url,
      model_file:$model_file,
      n_layer:$n_layer,
      placement_proof_model_id:$placement_proof_model_id,
      placement_proof:$placement_proof,
      memory_pressure_placement_verified: (
        $placement_proof != null
        and $placement_proof.feasible == true
        and $placement_proof.executable_now == true
        and (($placement_proof.blockers // []) | length) == 0
        and ($placement_proof.required_memory_bytes > ([$placement_proof.stages[].allowed_memory_bytes] | max))
      ),
      memory_pressure_execution_verified: (
        $placement_proof != null
        and $model_id == $placement_proof_model_id
        and $n_layer == $placement_proof.total_layers
        and ($placement_proof.required_memory_bytes > ([$placement_proof.stages[].allowed_memory_bytes] | max))
      ),
      execution_fixture_note: (
        if ($placement_proof != null and ($model_id != $placement_proof_model_id or $n_layer != $placement_proof.total_layers))
        then "execution used a cheaper GGUF fixture; placement proof used placement_proof_model_id"
        else "execution model matched placement proof metadata"
        end
      ),
      parent_job:$parent,
      dispatch_parent_job:$dispatch_parent,
      status:$parent_json[0].status,
      result:($parent_json[0].result | fromjson),
      dispatch_status:$dispatch_parent_json[0].status,
      dispatch_result:($dispatch_parent_json[0].result | fromjson)
    }' \
    > "$STATE_DIR/summary.json"
  if [[ "$CMESH_REQUIRE_MEMORY_PRESSURE_EXECUTION" == "true" ]]; then
    jq -e '.memory_pressure_execution_verified == true' "$STATE_DIR/summary.json" >/dev/null ||
      fail "memory-pressure execution was required but summary.json did not verify it"
  fi

  echo "PASS: AWS CDIP real GGUF stage-worker E2E succeeded"
  echo "evidence: $STATE_DIR"
}

main "$@"
