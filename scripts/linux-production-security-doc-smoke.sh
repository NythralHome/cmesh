#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DOC="$ROOT_DIR/docs/LINUX_SECURITY_HARDENING.md"
SECURITY="$ROOT_DIR/SECURITY.md"

fail() {
  echo "error: $*" >&2
  exit 1
}

[[ -f "$DOC" ]] || fail "missing security hardening doc: $DOC"
[[ -f "$SECURITY" ]] || fail "missing SECURITY.md: $SECURITY"

required_markers=(
  '`CMESH_JOIN_TOKEN` is for workers only'
  '`CMESH_OPERATOR_TOKEN` is for admin/operator APIs only'
  "Never reuse the join token as the operator token"
  "/etc/cmesh/manager.env"
  "/etc/cmesh/worker.env"
  "NoNewPrivileges=true"
  "ProtectSystem=strict"
  "ReadWritePaths=/var/lib/cmesh"
  'Authorization: Bearer $CMESH_OPERATOR_TOKEN'
  "Runtime artifacts must be checksum-verified"
  "Model artifacts in the Linux production matrix must be checksum-verified"
  "scripts/production-security-smoke.sh"
  "worker auth isolation"
  "ufw deny 19781/tcp"
)

for marker in "${required_markers[@]}"; do
  grep -F "$marker" "$DOC" >/dev/null || fail "security doc is missing marker: $marker"
done

security_markers=(
  "v0.1.0-linux-rc.1"
  "private invited Linux clusters"
  "GitHub Security"
  'Authorization: Bearer $CMESH_OPERATOR_TOKEN'
  "docs/LINUX_SECURITY_HARDENING.md"
  "Do not open a public issue"
  "signing private keys"
)

for marker in "${security_markers[@]}"; do
  grep -F "$marker" "$SECURITY" >/dev/null || fail "SECURITY.md is missing marker: $marker"
done

echo "PASS: Linux production security doc smoke completed"
echo "doc: $DOC"
