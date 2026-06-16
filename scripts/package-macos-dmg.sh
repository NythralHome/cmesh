#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <app-bundle> <output-dmg> <volume-name>" >&2
  exit 2
fi

app_bundle="$1"
output_dmg="$2"
volume_name="$3"

if [[ ! -d "$app_bundle" ]]; then
  echo "app bundle not found: $app_bundle" >&2
  exit 1
fi

if ! command -v hdiutil >/dev/null 2>&1; then
  echo "hdiutil is required to build macOS DMG installers" >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

staging="$tmp_dir/staging"
mkdir -p "$staging"
ditto "$app_bundle" "$staging/CMesh Worker.app"
ln -s /Applications "$staging/Applications"

mkdir -p "$(dirname "$output_dmg")"
rm -f "$output_dmg"

hdiutil create \
  -volname "$volume_name" \
  -srcfolder "$staging" \
  -ov \
  -format UDZO \
  "$output_dmg"
