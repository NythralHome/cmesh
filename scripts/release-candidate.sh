#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${CMESH_RC_VERSION:-${1:-}}"
OUT_ROOT="${CMESH_RC_DIR:-$ROOT_DIR/dist/release-candidate}"
DRY_RUN_DIR="${CMESH_RC_DRY_RUN_DIR:-$ROOT_DIR/dist/release-dry-run}"
READINESS_DIR="${CMESH_RC_READINESS_DIR:-$ROOT_DIR/dist/production-readiness}"
ALLOW_DIRTY="${CMESH_RC_ALLOW_DIRTY:-false}"
SKIP_READINESS="${CMESH_RC_SKIP_READINESS:-false}"

expected_assets=(
  "cmesh-linux-amd64"
  "cmesh-linux-arm64"
  "cmesh-darwin-amd64"
  "cmesh-darwin-arm64"
  "cmesh-windows-amd64.exe"
  "CMesh-Worker-Apple-Silicon.dmg"
  "CMesh-Worker-linux-amd64.tar.gz"
  "CMesh-Worker-windows-amd64.zip"
  "llama.cpp-b9704-linux-amd64-rpc.tar.gz"
  "llama.cpp-b9704-linux-amd64-rpc-stage.tar.gz"
  "llama-cpp-runtime-checksums.txt"
  "checksums.txt"
)

fail() {
  echo "error: $*" >&2
  exit 1
}

latest_alpha_tag() {
  git -C "$ROOT_DIR" tag --sort=-v:refname | grep -E '^v0\.1\.0-alpha\.[0-9]+$' | head -n 1
}

next_alpha_version() {
  local latest number
  latest="$(latest_alpha_tag || true)"
  if [[ -z "$latest" ]]; then
    echo "v0.1.0-alpha.1"
    return
  fi
  number="${latest##*.}"
  echo "v0.1.0-alpha.$((number + 1))"
}

release_range() {
  local latest
  latest="$(latest_alpha_tag || true)"
  if [[ -n "$latest" ]]; then
    echo "$latest..HEAD"
  else
    echo "HEAD"
  fi
}

assert_clean_tree() {
  if [[ "$ALLOW_DIRTY" == "true" ]]; then
    return
  fi
  if [[ -n "$(git -C "$ROOT_DIR" status --short)" ]]; then
    fail "working tree is dirty; commit or set CMESH_RC_ALLOW_DIRTY=true"
  fi
}

assert_dry_run_passed() {
  local report="$DRY_RUN_DIR/report.txt"
  [[ -f "$report" ]] || fail "missing dry-run report: $report"
  grep -F "PASS: release dry-run completed" "$report" >/dev/null || fail "dry-run report does not contain PASS"
}

assert_readiness_passed() {
  if [[ "$SKIP_READINESS" == "true" ]]; then
    return
  fi
  local report="$READINESS_DIR/report.txt"
  [[ -f "$report" ]] || fail "missing production readiness report: $report"
  grep -F "PASS: CMesh production readiness gate completed" "$report" >/dev/null ||
    fail "production readiness report does not contain PASS"
}

write_expected_assets() {
  local path="$1"
  {
    echo "# Expected release assets for $VERSION"
    echo
    for asset in "${expected_assets[@]}"; do
      echo "- $asset"
    done
  } >"$path"
}

write_local_checksums() {
  local path="$1"
  : >"$path"
  if [[ ! -d "$DRY_RUN_DIR" ]]; then
    return
  fi
  (
    cd "$DRY_RUN_DIR"
    find . -maxdepth 1 -type f ! -name checksums.txt -print0 |
      sort -z |
      xargs -0 shasum -a 256
  ) >"$path"
}

write_release_notes() {
  local path="$1"
  local range="$2"
  local commit
  commit="$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
  cat >"$path" <<EOF
# CMesh $VERSION

Release candidate prepared from commit \`$commit\`.

## Highlights

- Worker runtime repair flow is now explicit in the Worker app and local control API.
- Manager separates Installed Models, Model Catalog, Model Activity, Chat, and Readiness surfaces.
- Installed model inventory now includes worker, path, size, and runtime readiness metadata.
- Model install jobs report local download progress in the Worker app.
- Worker heartbeats now include CMesh storage accounting for models, runtimes, and cache usage.
- Model install scheduling now rejects workers that have insufficient physical free disk.
- Alpha validation docs now include a release checklist and model smoke pack.
- Linux distributed RPC workers can use a pinned CMesh-built llama.cpp runtime
  artifact instead of a manually installed host runtime.

## Validation Before Publishing

- Run \`make release-dry-run VERSION=$VERSION\`.
- Confirm \`dist/release-dry-run/report.txt\` contains \`PASS: release dry-run completed\`.
- Run \`make production-readiness\` for local readiness evidence, and
  \`CMESH_RUN_AWS_E2E=true make production-readiness\` before a public RC.
- Confirm \`dist/production-readiness/report.txt\` contains
  \`PASS: CMesh production readiness gate completed\`.
- Confirm macOS notarization secrets are configured before publishing public DMG builds.
- After GitHub release assets are published, run:

\`\`\`sh
CMESH_VERSION=$VERSION CMESH_DRY_RUN=true scripts/deploy-alpha.sh
\`\`\`

Only deploy alpha after the asset guard passes.

## Expected Assets

See \`expected-assets.md\`.

## Commit Summary

\`\`\`
$(git -C "$ROOT_DIR" log --oneline "$range")
\`\`\`
EOF
}

write_manifest() {
  local path="$1"
  local range="$2"
  {
    echo "version=$VERSION"
    echo "commit=$(git -C "$ROOT_DIR" rev-parse --short HEAD)"
    echo "date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "range=$range"
    echo "dry_run_dir=$DRY_RUN_DIR"
    echo "production_readiness_dir=$READINESS_DIR"
    echo "production_readiness_required=$([[ "$SKIP_READINESS" == "true" ]] && echo false || echo true)"
    echo "release_notes=RELEASE_NOTES.md"
    echo "expected_assets=expected-assets.md"
    echo "local_checksums=local-checksums.txt"
  } >"$path"
}

main() {
  if [[ -z "$VERSION" ]]; then
    VERSION="$(next_alpha_version)"
  fi
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+ ]] || fail "VERSION must look like a release tag, got $VERSION"

  assert_clean_tree
  assert_dry_run_passed
  assert_readiness_passed

  local out_dir range
  out_dir="$OUT_ROOT/$VERSION"
  range="$(release_range)"
  rm -rf "$out_dir"
  mkdir -p "$out_dir"

  write_release_notes "$out_dir/RELEASE_NOTES.md" "$range"
  write_expected_assets "$out_dir/expected-assets.md"
  write_local_checksums "$out_dir/local-checksums.txt"
  cp "$DRY_RUN_DIR/report.txt" "$out_dir/dry-run-report.txt"
  if [[ "$SKIP_READINESS" != "true" ]]; then
    cp "$READINESS_DIR/report.txt" "$out_dir/production-readiness-report.txt"
  fi
  write_manifest "$out_dir/manifest.env" "$range"

  echo "Prepared CMesh release candidate metadata:"
  echo "$out_dir"
  echo
  echo "Next manual steps:"
  echo "  1. Review $out_dir/RELEASE_NOTES.md"
  echo "  2. Tag and push only when ready: git tag $VERSION && git push origin $VERSION"
  echo "  3. After GitHub assets publish: CMESH_VERSION=$VERSION CMESH_DRY_RUN=true scripts/deploy-alpha.sh"
}

main "$@"
