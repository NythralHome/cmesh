#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${CMESH_DRY_RUN_VERSION:-0.0.0-dry-run}"
COMMIT="${CMESH_DRY_RUN_COMMIT:-$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
DATE="${CMESH_DRY_RUN_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
ARTIFACT_DIR="${CMESH_DRY_RUN_ARTIFACT_DIR:-$ROOT_DIR/dist/release-dry-run}"
PORT="${CMESH_DRY_RUN_PORT:-18081}"
SKIP_DESKTOP_BUILD="${CMESH_DRY_RUN_SKIP_DESKTOP_BUILD:-false}"
SKIP_DMG="${CMESH_DRY_RUN_SKIP_DMG:-false}"
SKIP_INSTALLER_SMOKE="${CMESH_DRY_RUN_SKIP_INSTALLER_SMOKE:-false}"
SKIP_SECURITY_SMOKE="${CMESH_DRY_RUN_SKIP_SECURITY_SMOKE:-false}"
SKIP_OBSERVABILITY_SMOKE="${CMESH_DRY_RUN_SKIP_OBSERVABILITY_SMOKE:-false}"

BIN_DIR="$ROOT_DIR/bin"
BIN="$BIN_DIR/cmesh"
REPORT="$ARTIFACT_DIR/report.txt"
MANAGER_LOG="$ARTIFACT_DIR/manager.log"
MANAGER_PID=""

LDFLAGS="-X github.com/cmesh/cmesh/internal/version.Version=$VERSION -X github.com/cmesh/cmesh/internal/version.Commit=$COMMIT -X github.com/cmesh/cmesh/internal/version.Date=$DATE"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

file_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

cleanup() {
  if [[ -n "$MANAGER_PID" ]] && kill -0 "$MANAGER_PID" >/dev/null 2>&1; then
    kill "$MANAGER_PID" >/dev/null 2>&1 || true
    wait "$MANAGER_PID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

step() {
  local name="$1"
  shift
  echo
  echo "==> $name"
  printf "==> %s\n" "$name" >>"$REPORT"
  "$@"
  printf "ok: %s\n" "$name" >>"$REPORT"
}

write_header() {
  rm -rf "$ARTIFACT_DIR"
  mkdir -p "$ARTIFACT_DIR" "$BIN_DIR"
  cat >"$REPORT" <<EOF
CMesh release dry-run
version: $VERSION
commit:  $COMMIT
date:    $DATE
host:    $(uname -s)/$(uname -m)

EOF
}

go_tests() {
  (cd "$ROOT_DIR" && go test ./...)
}

flutter_tests() {
  (cd "$ROOT_DIR/apps/worker_desktop" && fvm flutter analyze && fvm flutter test)
}

build_cli() {
  (cd "$ROOT_DIR" && go build -ldflags "$LDFLAGS" -o "$BIN" ./cmd/cmesh)
  [[ -x "$BIN" ]] || fail "CLI binary was not built: $BIN"
  "$BIN" version | tee "$ARTIFACT_DIR/version.txt"
  "$BIN" version | grep -F "$VERSION" >/dev/null || fail "CLI version does not contain $VERSION"
  cp "$BIN" "$ARTIFACT_DIR/cmesh-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)"
}

desktop_build_host() {
  case "$(uname -s)" in
    Darwin) echo "macos" ;;
    Linux) echo "linux" ;;
    *) echo "unsupported" ;;
  esac
}

build_worker_desktop() {
  if [[ "$SKIP_DESKTOP_BUILD" == "true" ]]; then
    echo "desktop build skipped by CMESH_DRY_RUN_SKIP_DESKTOP_BUILD=true"
    return
  fi

  local host
  host="$(desktop_build_host)"
  [[ "$host" != "unsupported" ]] || fail "desktop build is unsupported on $(uname -s)"

  (cd "$ROOT_DIR/apps/worker_desktop" && fvm flutter build "$host" --dart-define="CMESH_WORKER_VERSION=$VERSION")

  case "$host" in
    macos)
      local app_bundle="$ROOT_DIR/apps/worker_desktop/build/macos/Build/Products/Release/CMesh Worker.app"
      local embedded="$app_bundle/Contents/Resources/cmesh"
      [[ -d "$app_bundle" ]] || fail "macOS app bundle missing: $app_bundle"
      cp "$BIN" "$embedded"
      chmod +x "$embedded"
      [[ -x "$embedded" ]] || fail "embedded cmesh is not executable: $embedded"
      [[ "$(file_sha256 "$BIN")" == "$(file_sha256 "$embedded")" ]] || fail "embedded cmesh checksum mismatch"
      ditto "$app_bundle" "$ARTIFACT_DIR/CMesh Worker.app"
      if [[ "$SKIP_DMG" != "true" ]]; then
        step "Package macOS DMG" "$ROOT_DIR/scripts/package-macos-dmg.sh" "$app_bundle" "$ARTIFACT_DIR/CMesh-Worker-Apple-Silicon.dmg" "CMesh Worker $VERSION"
      fi
      ;;
    linux)
      local bundle
      bundle="$(find "$ROOT_DIR/apps/worker_desktop/build/linux" -path '*/release/bundle' -type d | head -n 1)"
      [[ -n "$bundle" && -d "$bundle" ]] || fail "Linux worker desktop bundle missing"
      cp "$BIN" "$bundle/cmesh"
      chmod +x "$bundle/cmesh"
      "$bundle/cmesh" version | grep -F "$VERSION" >/dev/null || fail "embedded cmesh version mismatch"
      tar -C "$bundle" -czf "$ARTIFACT_DIR/CMesh-Worker-linux-local.tar.gz" .
      ;;
  esac
}

wait_for_manager() {
  local deadline=$((SECONDS + 15))
  while (( SECONDS < deadline )); do
    if curl -fsS "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "manager log:" >&2
  cat "$MANAGER_LOG" >&2 || true
  fail "manager did not become healthy on port $PORT"
}

manager_smoke() {
  "$BIN" manager start -addr "127.0.0.1:$PORT" -memory >"$MANAGER_LOG" 2>&1 &
  MANAGER_PID="$!"
  wait_for_manager

  curl -fsS "http://127.0.0.1:$PORT/health" >"$ARTIFACT_DIR/health.json"
  curl -fsS "http://127.0.0.1:$PORT/v1/cluster" >"$ARTIFACT_DIR/cluster.json"
  curl -fsS "http://127.0.0.1:$PORT/v1/models" >"$ARTIFACT_DIR/models.json"
  curl -fsS "http://127.0.0.1:$PORT/" >"$ARTIFACT_DIR/dashboard.html"

  grep -F "Cluster Readiness" "$ARTIFACT_DIR/dashboard.html" >/dev/null || fail "dashboard readiness screen did not render"
  grep -F "qwen2.5-0.5b-instruct-q4-k-m" "$ARTIFACT_DIR/models.json" >/dev/null || fail "model catalog smoke failed"
}

installer_dry_run_smoke() {
  if [[ "$SKIP_INSTALLER_SMOKE" == "true" ]]; then
    echo "installer dry-run smoke skipped by CMESH_DRY_RUN_SKIP_INSTALLER_SMOKE=true"
    return
  fi
  if ! command -v docker >/dev/null 2>&1; then
    echo "installer dry-run smoke skipped because docker is not available"
    return
  fi
  WORK_DIR="$ARTIFACT_DIR/installers-dry-run-smoke" "$ROOT_DIR/scripts/installers-dry-run-smoke.sh"
}

security_smoke() {
  if [[ "$SKIP_SECURITY_SMOKE" == "true" ]]; then
    echo "security smoke skipped by CMESH_DRY_RUN_SKIP_SECURITY_SMOKE=true"
    return
  fi
  CMESH_PRODUCTION_SECURITY_SMOKE_DIR="$ARTIFACT_DIR/production-security-smoke" "$ROOT_DIR/scripts/production-security-smoke.sh"
}

observability_smoke() {
  if [[ "$SKIP_OBSERVABILITY_SMOKE" == "true" ]]; then
    echo "observability smoke skipped by CMESH_DRY_RUN_SKIP_OBSERVABILITY_SMOKE=true"
    return
  fi
  CMESH_OBSERVABILITY_SMOKE_DIR="$ARTIFACT_DIR/observability-smoke" "$ROOT_DIR/scripts/observability-smoke.sh"
}

write_manifest() {
  {
    echo "artifacts:"
    find "$ARTIFACT_DIR" -maxdepth 2 -mindepth 1 -print | sort
    echo
    echo "PASS: release dry-run completed"
    echo "Artifacts: $ARTIFACT_DIR"
  } >>"$REPORT"
  echo
  echo "PASS: release dry-run completed"
  echo "Artifacts: $ARTIFACT_DIR"
}

main() {
  need go
  need curl
  need fvm

  write_header
  step "Go tests" go_tests
  step "Flutter worker tests" flutter_tests
  step "Build CMesh CLI" build_cli
  step "Build worker desktop" build_worker_desktop
  step "Installer dry-run smoke" installer_dry_run_smoke
  step "Production security smoke" security_smoke
  step "Observability smoke" observability_smoke
  step "Manager dashboard/API smoke" manager_smoke
  write_manifest
}

main "$@"
