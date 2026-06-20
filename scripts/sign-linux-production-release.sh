#!/usr/bin/env bash
set -euo pipefail

CMESH_LINUX_PACKAGE_DIR="${CMESH_LINUX_PACKAGE_DIR:-${1:-}}"
CMESH_SIGNING_PRIVATE_KEY="${CMESH_SIGNING_PRIVATE_KEY:-}"
CMESH_SIGNING_PUBLIC_KEY="${CMESH_SIGNING_PUBLIC_KEY:-}"
CMESH_SIGNING_GENERATE_TEST_KEY="${CMESH_SIGNING_GENERATE_TEST_KEY:-false}"
CMESH_PUBLIC_RELEASE="${CMESH_PUBLIC_RELEASE:-false}"
CMESH_SIGNING_KEY_ID="${CMESH_SIGNING_KEY_ID:-}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

file_sha256() {
  local path="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$path" | awk '{print $1}'
  else
    shasum -a 256 "$path" | awk '{print $1}'
  fi
}

sign_file() {
  local file="$1"
  openssl dgst -sha256 -sign "$CMESH_SIGNING_PRIVATE_KEY" -out "$file.sig" "$file"
}

verify_file() {
  local file="$1"
  openssl dgst -sha256 -verify "$CMESH_SIGNING_PUBLIC_KEY" -signature "$file.sig" "$file" >/dev/null
}

write_checksums() {
  local package_dir="$1"
  (
    cd "$package_dir"
    find . -type f \
      ! -name checksums.txt \
      ! -name '*.sig' \
      ! -name signature-manifest.json \
      -print0 |
      sort -z |
      xargs -0 shasum -a 256
  ) >"$package_dir/checksums.txt"
}

main() {
  need openssl
  need tar
  need shasum
  need jq

  [[ -n "$CMESH_LINUX_PACKAGE_DIR" ]] || fail "CMESH_LINUX_PACKAGE_DIR is required"
  CMESH_LINUX_PACKAGE_DIR="$(cd "$CMESH_LINUX_PACKAGE_DIR" && pwd -P)"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/manifest.json" ]] || fail "missing manifest.json"
  [[ -f "$CMESH_LINUX_PACKAGE_DIR/checksums.txt" ]] || fail "missing checksums.txt"
  jq -e '.kind == "cmesh.linux.production.release.v1"' "$CMESH_LINUX_PACKAGE_DIR/manifest.json" >/dev/null

  if [[ "$CMESH_PUBLIC_RELEASE" == "true" && "$CMESH_SIGNING_GENERATE_TEST_KEY" == "true" ]]; then
    fail "public releases must use an explicit CMESH_SIGNING_PRIVATE_KEY; test keys are not allowed"
  fi

  if [[ -z "$CMESH_SIGNING_PRIVATE_KEY" ]]; then
    if [[ "$CMESH_SIGNING_GENERATE_TEST_KEY" != "true" ]]; then
      fail "CMESH_SIGNING_PRIVATE_KEY is required unless CMESH_SIGNING_GENERATE_TEST_KEY=true"
    fi
    mkdir -p "$(dirname "$CMESH_LINUX_PACKAGE_DIR")/signing"
    CMESH_SIGNING_PRIVATE_KEY="$(dirname "$CMESH_LINUX_PACKAGE_DIR")/signing/cmesh-linux-production-test-rsa.key"
    openssl genrsa -out "$CMESH_SIGNING_PRIVATE_KEY" 3072 >/dev/null 2>&1
    chmod 600 "$CMESH_SIGNING_PRIVATE_KEY"
  fi

  [[ -f "$CMESH_SIGNING_PRIVATE_KEY" ]] || fail "signing private key not found: $CMESH_SIGNING_PRIVATE_KEY"
  if [[ "$CMESH_PUBLIC_RELEASE" == "true" ]]; then
    [[ -n "$CMESH_SIGNING_KEY_ID" ]] || fail "CMESH_SIGNING_KEY_ID is required for public releases"
    case "$CMESH_SIGNING_PRIVATE_KEY" in
      "$CMESH_LINUX_PACKAGE_DIR"/*|*/dist/*)
        fail "public release private key must not live inside package or dist paths"
        ;;
    esac
  fi
  if [[ -z "$CMESH_SIGNING_PUBLIC_KEY" ]]; then
    CMESH_SIGNING_PUBLIC_KEY="$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem"
    openssl rsa -in "$CMESH_SIGNING_PRIVATE_KEY" -pubout -out "$CMESH_SIGNING_PUBLIC_KEY" >/dev/null 2>&1
  fi
  [[ -f "$CMESH_SIGNING_PUBLIC_KEY" ]] || fail "signing public key not found: $CMESH_SIGNING_PUBLIC_KEY"
  if [[ "$(cd "$(dirname "$CMESH_SIGNING_PUBLIC_KEY")" && pwd -P)/$(basename "$CMESH_SIGNING_PUBLIC_KEY")" != "$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem" ]]; then
    cp "$CMESH_SIGNING_PUBLIC_KEY" "$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem"
  fi

  local key_fingerprint version tarball tarball_sha key_kind release_warning
  key_fingerprint="$(openssl rsa -pubin -in "$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem" -outform DER 2>/dev/null | shasum -a 256 | awk '{print $1}')"
  version="$(jq -r '.version' "$CMESH_LINUX_PACKAGE_DIR/manifest.json")"
  key_kind="public-release"
  release_warning=""
  if [[ "$CMESH_SIGNING_GENERATE_TEST_KEY" == "true" ]]; then
    key_kind="generated-test"
    release_warning=$'\n> WARNING: this package was signed with a generated test key and must not be published as a public release.\n'
  fi

  cat >"$CMESH_LINUX_PACKAGE_DIR/SIGNING.md" <<EOF
# CMesh Linux Release Signatures

This package uses detached RSA/SHA-256 signatures.
$release_warning
## Key Metadata

- Key kind: \`$key_kind\`
- Key id: \`${CMESH_SIGNING_KEY_ID:-local-test}\`

## Files

- \`manifest.json.sig\` signs \`manifest.json\`
- \`checksums.txt.sig\` signs \`checksums.txt\`
- \`release-signing-public-key.pem\` is the public verification key

## Public Key Fingerprint

\`\`\`text
$key_fingerprint
\`\`\`

## Verify

\`\`\`sh
openssl dgst -sha256 -verify release-signing-public-key.pem -signature manifest.json.sig manifest.json
openssl dgst -sha256 -verify release-signing-public-key.pem -signature checksums.txt.sig checksums.txt
shasum -a 256 -c checksums.txt
\`\`\`
EOF

  write_checksums "$CMESH_LINUX_PACKAGE_DIR"
  (cd "$CMESH_LINUX_PACKAGE_DIR" && shasum -a 256 -c checksums.txt >/dev/null)

  rm -f "$CMESH_LINUX_PACKAGE_DIR/manifest.json.sig" "$CMESH_LINUX_PACKAGE_DIR/checksums.txt.sig"
  sign_file "$CMESH_LINUX_PACKAGE_DIR/manifest.json"
  sign_file "$CMESH_LINUX_PACKAGE_DIR/checksums.txt"
  verify_file "$CMESH_LINUX_PACKAGE_DIR/manifest.json"
  verify_file "$CMESH_LINUX_PACKAGE_DIR/checksums.txt"

  tarball="$(dirname "$CMESH_LINUX_PACKAGE_DIR")/$(basename "$CMESH_LINUX_PACKAGE_DIR").tar.gz"
  rm -f "$tarball" "$tarball.sha256" "$tarball.sig" "$tarball.public-key.pem"
  COPYFILE_DISABLE=1 tar --no-xattrs -C "$(dirname "$CMESH_LINUX_PACKAGE_DIR")" -czf "$tarball" "$(basename "$CMESH_LINUX_PACKAGE_DIR")"
  tarball_sha="$(file_sha256 "$tarball")"
  printf '%s  %s\n' "$tarball_sha" "$(basename "$tarball")" > "$tarball.sha256"
  cp "$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem" "$tarball.public-key.pem"
  sign_file "$tarball"
  verify_file "$tarball"

  jq -n \
    --arg kind "cmesh.linux.production.signature.v1" \
    --arg version "$version" \
    --arg package_dir "$CMESH_LINUX_PACKAGE_DIR" \
    --arg public_key "$CMESH_LINUX_PACKAGE_DIR/release-signing-public-key.pem" \
    --arg fingerprint "$key_fingerprint" \
    --arg key_kind "$key_kind" \
    --arg key_id "${CMESH_SIGNING_KEY_ID:-local-test}" \
    --arg public_release "$CMESH_PUBLIC_RELEASE" \
    --arg tarball "$tarball" \
    --arg tarball_sha256 "$tarball_sha" \
    --arg tarball_public_key "$tarball.public-key.pem" \
    '{
      kind: $kind,
      version: $version,
      package_dir: $package_dir,
      public_key: $public_key,
      public_key_sha256: $fingerprint,
      key_kind: $key_kind,
      key_id: $key_id,
      public_release: ($public_release == "true"),
      signed_files: ["manifest.json", "checksums.txt"],
      tarball: $tarball,
      tarball_public_key: $tarball_public_key,
      tarball_sha256: $tarball_sha256
    }' > "$CMESH_LINUX_PACKAGE_DIR/signature-manifest.json"

  echo "PASS: CMesh Linux production release signed"
  echo "package_dir: $CMESH_LINUX_PACKAGE_DIR"
  echo "tarball: $tarball"
  echo "public_key_sha256: $key_fingerprint"
}

main "$@"
