#!/usr/bin/env bash
set -euo pipefail

CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-${1:-}}"
CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE="${CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE:-false}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

main() {
  need openssl
  need shasum
  need jq
  need tar

  [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required"
  CMESH_LINUX_PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  local public_key="$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem"
  local tarball="$(dirname "$CMESH_LINUX_PACKAGE_DIR")/$(basename "$CMESH_LINUX_PACKAGE_DIR").tar.gz"
  local tarball_public_key="$tarball.public-key.pem"

  [[ -f "$public_key" ]] || fail "missing release-signing-public-key.pem"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/manifest.json.sig" ]] || fail "missing manifest.json.sig"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/checksums.txt.sig" ]] || fail "missing checksums.txt.sig"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/SIGNING.md" ]] || fail "missing SIGNING.md"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/signature-manifest.json" ]] || fail "missing signature-manifest.json"
  [[ -f "$tarball" ]] || fail "missing tarball: $tarball"
  [[ -f "$tarball.sha256" ]] || fail "missing tarball checksum: $tarball.sha256"
  [[ -f "$tarball.sig" ]] || fail "missing tarball signature: $tarball.sig"
  [[ -f "$tarball_public_key" ]] || fail "missing tarball public key sidecar: $tarball_public_key"
  cmp "$public_key" "$tarball_public_key" >/dev/null || fail "package public key and tarball public key differ"

  openssl dgst -sha256 -verify "$public_key" -signature "$CMESH_LINUX_PACKAGE_DIR/manifest.json.sig" "$CMESH_LINUX_PACKAGE_DIR/manifest.json" >/dev/null
  openssl dgst -sha256 -verify "$public_key" -signature "$CMESH_LINUX_PACKAGE_DIR/checksums.txt.sig" "$CMESH_LINUX_PACKAGE_DIR/checksums.txt" >/dev/null
  openssl dgst -sha256 -verify "$public_key" -signature "$tarball.sig" "$tarball" >/dev/null
  (cd "$CMESH_LINUX_PACKAGE_DIR" && shasum -a 256 -c checksums.txt >/dev/null)
  (cd "$(dirname "$tarball")" && shasum -a 256 -c "$(basename "$tarball").sha256" >/dev/null)

  jq -e '.kind == "cmesh.linux.production.release.v1"' "$CMESH_LINUX_PACKAGE_DIR/manifest.json" >/dev/null
  jq -e '.kind == "cmesh.linux.production.signature.v1" and (.signed_files | index("manifest.json")) and (.signed_files | index("checksums.txt"))' "$CMESH_LINUX_PACKAGE_DIR/signature-manifest.json" >/dev/null
  if [[ "$CMESH_REQUIRE_PUBLIC_RELEASE_SIGNATURE" == "true" ]]; then
    jq -e '.public_release == true and .key_kind == "public-release" and (.key_id | length > 0) and .key_id != "local-test"' "$CMESH_LINUX_PACKAGE_DIR/signature-manifest.json" >/dev/null
    if grep -qi 'generated test key' "$CMESH_LINUX_PACKAGE_DIR/SIGNING.md"; then
      fail "public release package still contains generated test key warning"
    fi
  fi

  if find "$CMESH_LINUX_PACKAGE_DIR" -type f \( -name '*.key' -o -name '*private*' \) | grep -q .; then
    fail "private signing material found inside package"
  fi
  if tar -tzf "$tarball" | grep -E '(^|/)(.*private.*|.*\\.key)$' >/dev/null; then
    fail "private signing material found inside tarball"
  fi

  echo "PASS: Linux stable release signature smoke completed"
  echo "package_dir: $CMESH_LINUX_PACKAGE_DIR"
  echo "tarball: $tarball"
}

main "$@"
