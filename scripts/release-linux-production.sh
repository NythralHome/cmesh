#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${CMESH_LINUX_RELEASE_VERSION:-${1:-}}"
OUT_ROOT="${CMESH_LINUX_RELEASE_DIR:-$ROOT_DIR/dist/linux-production}"
ALLOW_DIRTY="${CMESH_LINUX_RELEASE_ALLOW_DIRTY:-true}"
LLAMA_CPP_REF="${LLAMA_CPP_REF:-b9704}"
RUNTIME_OS="${CMESH_STAGE_RUNTIME_TARGET_OS:-linux}"
RUNTIME_CPU="${CMESH_STAGE_RUNTIME_TARGET_CPU:-amd64}"
RUNTIME_ARCHIVE_NAME="llama.cpp-$LLAMA_CPP_REF-$RUNTIME_OS-$RUNTIME_CPU-rpc-stage.tar.gz"
RUNTIME_ARCHIVE="${CMESH_STAGE_RUNTIME_ARCHIVE:-$ROOT_DIR/dist/runtimes-$RUNTIME_OS-$RUNTIME_CPU-current/$RUNTIME_ARCHIVE_NAME}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"
}

default_version() {
  local commit
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  echo "v0.1.0-linux-dev-$commit"
}

assert_version() {
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]] ||
    fail "CMESH_LINUX_RELEASE_VERSION must look like a release tag, got: $VERSION"
}

assert_clean_tree() {
  if [[ "$ALLOW_DIRTY" == "true" ]]; then
    return
  fi
  if [[ -n "$(git -C "$ROOT_DIR" status --short)" ]]; then
    fail "working tree is dirty; commit or set CMESH_LINUX_RELEASE_ALLOW_DIRTY=true"
  fi
}

build_binary() {
  local goos="$1"
  local goarch="$2"
  local output="$3"
  local commit date ldflags
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ldflags="-X github.com/cmesh/cmesh/internal/version.Version=$VERSION -X github.com/cmesh/cmesh/internal/version.Commit=$commit -X github.com/cmesh/cmesh/internal/version.Date=$date"
  echo "building $goos/$goarch -> $output"
  (
    cd "$ROOT_DIR"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags "$ldflags" -o "$output" ./cmd/cmesh
  )
}

prepare_runtime_archive() {
  if [[ ! -f "$RUNTIME_ARCHIVE" ]]; then
    echo "preparing current Linux stage runtime artifact"
    RUNTIME_ARCHIVE="$(CMESH_STAGE_RUNTIME_TARGET_OS="$RUNTIME_OS" CMESH_STAGE_RUNTIME_TARGET_CPU="$RUNTIME_CPU" "$ROOT_DIR/scripts/prepare-current-stage-runtime-artifact.sh")"
  fi
  [[ -f "$RUNTIME_ARCHIVE" ]] || fail "missing runtime archive: $RUNTIME_ARCHIVE"
  "$ROOT_DIR/scripts/verify-llamacpp-runtime-artifact.sh" "$RUNTIME_ARCHIVE" >/dev/null
}

write_manifest() {
  local out_dir="$1"
  local path="$out_dir/manifest.json"
  local commit date
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"
  date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  cat >"$path" <<EOF
{
  "kind": "cmesh.linux.production.release.v1",
  "version": $(json_escape "$VERSION"),
  "commit": $(json_escape "$commit"),
  "date": $(json_escape "$date"),
  "platforms": ["linux/amd64", "linux/arm64"],
  "assets": [
    {"name": "cmesh-linux-amd64", "platform": "linux/amd64", "type": "binary"},
    {"name": "cmesh-linux-arm64", "platform": "linux/arm64", "type": "binary"},
    {"name": "install-manager-linux.sh", "platform": "linux", "type": "installer"},
    {"name": "install-worker.sh", "platform": "linux", "type": "installer"},
    {"name": "$RUNTIME_ARCHIVE_NAME", "platform": "$RUNTIME_OS/$RUNTIME_CPU", "type": "llama.cpp-stage-runtime"},
    {"name": "$RUNTIME_ARCHIVE_NAME.sha256", "platform": "$RUNTIME_OS/$RUNTIME_CPU", "type": "llama.cpp-stage-runtime-checksum"},
    {"name": "docs/LINUX_PRODUCTION.md", "platform": "linux", "type": "public-production-guide"},
    {"name": "docs/PRODUCTION_INSTALL.md", "platform": "linux", "type": "runbook"},
    {"name": "docs/LINUX_SLICED_RUNBOOK.md", "platform": "linux", "type": "sliced-runbook"},
    {"name": "docs/LINUX_SECURITY_HARDENING.md", "platform": "linux", "type": "security-hardening"},
    {"name": "docs/LINUX_OBSERVABILITY.md", "platform": "linux", "type": "observability"},
    {"name": "docs/LINUX_BACKUP_RESTORE.md", "platform": "linux", "type": "backup-restore"},
    {"name": "docs/LINUX_MODEL_MATRIX.md", "platform": "linux", "type": "model-matrix"},
    {"name": "docs/LINUX_PRODUCTION_MILESTONES.md", "platform": "linux", "type": "milestones"},
    {"name": "docs/RELEASE_SCOPE.md", "platform": "linux", "type": "release-scope"},
    {"name": "docs/RELEASE_SIGNING.md", "platform": "linux", "type": "release-signing"},
    {"name": "docs/RELEASE_MILESTONES.md", "platform": "linux", "type": "release-milestones"},
    {"name": "docs/RELEASE_BRANCH_AUDIT.md", "platform": "linux", "type": "release-branch-audit"},
    {"name": "docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md", "platform": "linux", "type": "release-notes"},
    {"name": "docs/GITHUB_RELEASE_CHECKLIST.md", "platform": "linux", "type": "github-release-checklist"},
    {"name": "docs/THIRD_PARTY_NOTICES.md", "platform": "linux", "type": "third-party-notices"}
  ],
  "checksums": "checksums.txt",
  "milestones": "docs/LINUX_PRODUCTION_MILESTONES.md",
  "notes": "Linux-only production release lane. macOS and Windows artifacts are intentionally not required."
}
EOF
}

write_readme() {
  local out_dir="$1"
  cat >"$out_dir/README.md" <<EOF
# CMesh Linux Production Package $VERSION

This directory contains the Linux production package for CMesh.

## Assets

- \`cmesh-linux-amd64\`
- \`cmesh-linux-arm64\`
- \`install-manager-linux.sh\`
- \`install-worker.sh\`
- \`$RUNTIME_ARCHIVE_NAME\`
- \`$RUNTIME_ARCHIVE_NAME.sha256\`
- \`checksums.txt\`
- \`manifest.json\`
- \`docs/LINUX_PRODUCTION.md\`
- \`docs/PRODUCTION_INSTALL.md\`
- \`docs/LINUX_SLICED_RUNBOOK.md\`
- \`docs/LINUX_SECURITY_HARDENING.md\`
- \`docs/LINUX_OBSERVABILITY.md\`
- \`docs/LINUX_BACKUP_RESTORE.md\`
- \`docs/LINUX_MODEL_MATRIX.md\`
- \`docs/LINUX_PRODUCTION_MILESTONES.md\`
- \`docs/RELEASE_SCOPE.md\`
- \`docs/RELEASE_SIGNING.md\`
- \`docs/RELEASE_MILESTONES.md\`
- \`docs/RELEASE_BRANCH_AUDIT.md\`
- \`docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md\`
- \`docs/GITHUB_RELEASE_CHECKLIST.md\`
- \`docs/THIRD_PARTY_NOTICES.md\`

## Manager install

\`\`\`sh
sudo CMESH_BINARY_URL=https://example.com/cmesh-linux-amd64 \\
  CMESH_MANAGER_DOMAIN=cluster.example.com \\
  ./install-manager-linux.sh install
\`\`\`

## Worker install

\`\`\`sh
sudo CMESH_BINARY_URL=https://example.com/cmesh-linux-amd64 \\
  CMESH_MANAGER_URL=https://cluster.example.com \\
  CMESH_JOIN_TOKEN=replace-with-join-token \\
  CMESH_LLAMA_CPP_RUNTIME_AUTO=true \\
  CMESH_LLAMA_CPP_RUNTIME_URL=https://example.com/$RUNTIME_ARCHIVE_NAME \\
  CMESH_LLAMA_CPP_RUNTIME_REQUIRE_CHECKSUM=true \\
  ./install-worker.sh install
\`\`\`

Use \`checksums.txt\` to verify downloaded artifacts before publishing or
mirroring this package.
EOF
}

write_checksums() {
  local out_dir="$1"
  (
    cd "$out_dir"
    find . -type f ! -name checksums.txt -print0 |
      sort -z |
      xargs -0 shasum -a 256
  ) >"$out_dir/checksums.txt"
}

main() {
  need go
  need git
  need tar
  need python3

  if [[ -z "$VERSION" ]]; then
    VERSION="$(default_version)"
  fi
  assert_version
  assert_clean_tree
  prepare_runtime_archive

  local out_dir
  out_dir="$OUT_ROOT/$VERSION"
  rm -rf "$out_dir"
  mkdir -p "$out_dir"

  build_binary linux amd64 "$out_dir/cmesh-linux-amd64"
  build_binary linux arm64 "$out_dir/cmesh-linux-arm64"
  cp "$ROOT_DIR/scripts/install-manager-linux.sh" "$out_dir/install-manager-linux.sh"
  cp "$ROOT_DIR/scripts/install-worker.sh" "$out_dir/install-worker.sh"
  chmod +x "$out_dir/install-manager-linux.sh" "$out_dir/install-worker.sh" "$out_dir/cmesh-linux-amd64" "$out_dir/cmesh-linux-arm64"
  cp "$RUNTIME_ARCHIVE" "$out_dir/$RUNTIME_ARCHIVE_NAME"
  sha256_file "$out_dir/$RUNTIME_ARCHIVE_NAME" |
    awk -v name="$RUNTIME_ARCHIVE_NAME" '{print $1 "  " name}' >"$out_dir/$RUNTIME_ARCHIVE_NAME.sha256"
  mkdir -p "$out_dir/docs"
  cp "$ROOT_DIR/docs/LINUX_PRODUCTION.md" "$out_dir/docs/LINUX_PRODUCTION.md"
  cp "$ROOT_DIR/docs/PRODUCTION_INSTALL.md" "$out_dir/docs/PRODUCTION_INSTALL.md"
  cp "$ROOT_DIR/docs/LINUX_SLICED_RUNBOOK.md" "$out_dir/docs/LINUX_SLICED_RUNBOOK.md"
  cp "$ROOT_DIR/docs/LINUX_SECURITY_HARDENING.md" "$out_dir/docs/LINUX_SECURITY_HARDENING.md"
  cp "$ROOT_DIR/docs/LINUX_OBSERVABILITY.md" "$out_dir/docs/LINUX_OBSERVABILITY.md"
  cp "$ROOT_DIR/docs/LINUX_BACKUP_RESTORE.md" "$out_dir/docs/LINUX_BACKUP_RESTORE.md"
  cp "$ROOT_DIR/docs/LINUX_MODEL_MATRIX.md" "$out_dir/docs/LINUX_MODEL_MATRIX.md"
  cp "$ROOT_DIR/docs/LINUX_PRODUCTION_MILESTONES.md" "$out_dir/docs/LINUX_PRODUCTION_MILESTONES.md"
  cp "$ROOT_DIR/docs/RELEASE_SCOPE.md" "$out_dir/docs/RELEASE_SCOPE.md"
  cp "$ROOT_DIR/docs/RELEASE_SIGNING.md" "$out_dir/docs/RELEASE_SIGNING.md"
  cp "$ROOT_DIR/docs/RELEASE_MILESTONES.md" "$out_dir/docs/RELEASE_MILESTONES.md"
  cp "$ROOT_DIR/docs/RELEASE_BRANCH_AUDIT.md" "$out_dir/docs/RELEASE_BRANCH_AUDIT.md"
  cp "$ROOT_DIR/docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md" "$out_dir/docs/RELEASE_NOTES_v0.1.0-linux-rc.1.md"
  cp "$ROOT_DIR/docs/GITHUB_RELEASE_CHECKLIST.md" "$out_dir/docs/GITHUB_RELEASE_CHECKLIST.md"
  cp "$ROOT_DIR/docs/THIRD_PARTY_NOTICES.md" "$out_dir/docs/THIRD_PARTY_NOTICES.md"

  write_manifest "$out_dir"
  write_readme "$out_dir"
  write_checksums "$out_dir"

  echo "Prepared CMesh Linux production package:"
  echo "$out_dir"
  echo
  echo "Checksums:"
  cat "$out_dir/checksums.txt"
}

main "$@"
