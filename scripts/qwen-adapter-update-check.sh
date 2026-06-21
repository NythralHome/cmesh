#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CMESH_BIN="${CMESH_BIN:-}"

if [[ -z "$CMESH_BIN" ]]; then
  CMESH_BIN="$ROOT_DIR/dist/cmesh"
  if [[ ! -x "$CMESH_BIN" ]]; then
    CMESH_BIN="go run ./cmd/cmesh"
  fi
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required" >&2
  exit 2
fi

cd "$ROOT_DIR"

catalog_json="$($CMESH_BIN model catalog --family qwen --json)"
status_file="$(mktemp "${TMPDIR:-/tmp}/cmesh-qwen-update-status.XXXXXX")"
trap 'rm -f "$status_file"' EXIT
printf '0' >"$status_file"

echo "$catalog_json" | jq -c '.[]' | while read -r model; do
  id="$(jq -r '.id' <<<"$model")"
  repo="$(jq -r '.repo' <<<"$model")"
  pinned="$(jq -r '.repo_sha // ""' <<<"$model")"
  adapter="$(jq -r '.adapter // ""' <<<"$model")"

  if [[ -z "$repo" || "$repo" == "null" ]]; then
    echo "FAIL $id has no repo"
    exit 3
  fi
  if [[ -z "$pinned" || "$pinned" == "null" ]]; then
    echo "REVIEW $id has no pinned repo sha adapter=$adapter repo=$repo"
    printf '1' >"$status_file"
    continue
  fi

  current="$(curl -fsSL "https://huggingface.co/api/models/$repo" | jq -r '.sha // ""')"
  if [[ -z "$current" || "$current" == "null" ]]; then
    echo "REVIEW $id could not resolve current repo sha adapter=$adapter repo=$repo"
    printf '1' >"$status_file"
    continue
  fi

  if [[ "$current" == "$pinned" ]]; then
    echo "OK $id adapter=$adapter repo=$repo sha=$current"
  else
    echo "REVIEW $id adapter=$adapter repo=$repo pinned=$pinned current=$current"
    printf '1' >"$status_file"
  fi
done

exit "$(cat "$status_file")"
