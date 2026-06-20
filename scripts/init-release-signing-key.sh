#!/usr/bin/env bash
set -euo pipefail

CMESH_SIGNING_KEY_ID="${CMESH_SIGNING_KEY_ID:-cmesh-linux-release-2026q2}"
CMESH_SIGNING_KEY_DIR="${CMESH_SIGNING_KEY_DIR:-$HOME/.cmesh/release-signing}"
CMESH_SIGNING_KEY_BITS="${CMESH_SIGNING_KEY_BITS:-4096}"
CMESH_FORCE_ROTATE_SIGNING_KEY="${CMESH_FORCE_ROTATE_SIGNING_KEY:-false}"

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

  [[ -n "$CMESH_SIGNING_KEY_ID" ]] || fail "CMESH_SIGNING_KEY_ID is required"
  [[ "$CMESH_SIGNING_KEY_ID" =~ ^[a-zA-Z0-9._-]+$ ]] || fail "CMESH_SIGNING_KEY_ID may only contain letters, numbers, dots, underscores, and dashes"

  mkdir -p "$CMESH_SIGNING_KEY_DIR"
  chmod 700 "$CMESH_SIGNING_KEY_DIR"

  local private_key="$CMESH_SIGNING_KEY_DIR/$CMESH_SIGNING_KEY_ID.key"
  local public_key="$CMESH_SIGNING_KEY_DIR/$CMESH_SIGNING_KEY_ID.pub.pem"

  if [[ -e "$private_key" && "$CMESH_FORCE_ROTATE_SIGNING_KEY" != "true" ]]; then
    echo "release signing key already exists: $private_key" >&2
  else
    [[ "$CMESH_SIGNING_KEY_DIR" != *"/dist"* ]] || fail "signing key dir must not live under dist"
    openssl genrsa -out "$private_key" "$CMESH_SIGNING_KEY_BITS" >/dev/null 2>&1
    chmod 600 "$private_key"
  fi

  openssl rsa -in "$private_key" -pubout -out "$public_key" >/dev/null 2>&1
  chmod 644 "$public_key"

  local fingerprint
  fingerprint="$(openssl rsa -pubin -in "$public_key" -outform DER 2>/dev/null | shasum -a 256 | awk '{print $1}')"

  cat <<EOF
CMESH_SIGNING_KEY_ID=$CMESH_SIGNING_KEY_ID
CMESH_SIGNING_PRIVATE_KEY=$private_key
CMESH_SIGNING_PUBLIC_KEY=$public_key
CMESH_SIGNING_PUBLIC_KEY_SHA256=$fingerprint
EOF
}

main "$@"
