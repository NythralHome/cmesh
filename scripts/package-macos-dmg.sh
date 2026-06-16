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

retry() {
  local attempts="$1"
  local delay_seconds="$2"
  shift 2

  local attempt=1
  while true; do
    if "$@"; then
      return 0
    fi

    if (( attempt >= attempts )); then
      return 1
    fi

    sleep "$delay_seconds"
    attempt=$((attempt + 1))
  done
}

staging="$tmp_dir/staging"
mkdir -p "$staging"
ditto "$app_bundle" "$staging/CMesh Worker.app"
ln -s /Applications "$staging/Applications"
mkdir -p "$staging/.background"
chflags hidden "$staging/.background" 2>/dev/null || true

app_icon="$app_bundle/Contents/Resources/AppIcon.icns"
if [[ -f "$app_icon" ]]; then
  cp "$app_icon" "$staging/.VolumeIcon.icns"
fi

mkdir -p "$(dirname "$output_dmg")"
rm -f "$output_dmg"

if [[ "${CMESH_PLAIN_DMG:-}" == "1" ]]; then
  hdiutil create \
    -volname "$volume_name" \
    -srcfolder "$staging" \
    -ov \
    -format UDZO \
    -fs HFS+ \
    "$output_dmg" >/dev/null
  exit 0
fi

background="$staging/.background/background.png"
background_script="$tmp_dir/make-dmg-background.swift"
cat > "$background_script" <<'SWIFT'
import AppKit

let outputPath = CommandLine.arguments[1]
let size = NSSize(width: 660, height: 420)
let image = NSImage(size: size)

image.lockFocus()

NSColor(calibratedRed: 0.94, green: 0.98, blue: 0.96, alpha: 1.0).setFill()
NSBezierPath(rect: NSRect(origin: .zero, size: size)).fill()

let accent = NSColor(calibratedRed: 0.04, green: 0.45, blue: 0.36, alpha: 1.0)
let muted = NSColor(calibratedRed: 0.23, green: 0.29, blue: 0.27, alpha: 1.0)

let title = "Install CMesh Worker"
let titleAttributes: [NSAttributedString.Key: Any] = [
  .font: NSFont.systemFont(ofSize: 28, weight: .bold),
  .foregroundColor: NSColor(calibratedRed: 0.08, green: 0.11, blue: 0.10, alpha: 1.0)
]
title.draw(at: NSPoint(x: 42, y: 352), withAttributes: titleAttributes)

let subtitle = "Drag CMesh Worker to Applications"
let subtitleAttributes: [NSAttributedString.Key: Any] = [
  .font: NSFont.systemFont(ofSize: 17, weight: .semibold),
  .foregroundColor: muted
]
subtitle.draw(at: NSPoint(x: 42, y: 323), withAttributes: subtitleAttributes)

let line = NSBezierPath()
line.move(to: NSPoint(x: 238, y: 195))
line.curve(to: NSPoint(x: 424, y: 195), controlPoint1: NSPoint(x: 295, y: 228), controlPoint2: NSPoint(x: 365, y: 228))
line.lineWidth = 5
accent.setStroke()
line.stroke()

let arrow = NSBezierPath()
arrow.move(to: NSPoint(x: 424, y: 195))
arrow.line(to: NSPoint(x: 396, y: 213))
arrow.move(to: NSPoint(x: 424, y: 195))
arrow.line(to: NSPoint(x: 396, y: 177))
arrow.lineWidth = 5
arrow.lineCapStyle = .round
accent.setStroke()
arrow.stroke()

let footer = "After installing, return to the cluster invite page and click Open Worker App."
let footerAttributes: [NSAttributedString.Key: Any] = [
  .font: NSFont.systemFont(ofSize: 13, weight: .medium),
  .foregroundColor: NSColor(calibratedRed: 0.32, green: 0.39, blue: 0.37, alpha: 1.0)
]
footer.draw(at: NSPoint(x: 42, y: 34), withAttributes: footerAttributes)

image.unlockFocus()

guard
  let tiff = image.tiffRepresentation,
  let bitmap = NSBitmapImageRep(data: tiff),
  let png = bitmap.representation(using: .png, properties: [:])
else {
  fputs("failed to render DMG background\n", stderr)
  exit(1)
}

try png.write(to: URL(fileURLWithPath: outputPath))
SWIFT
swift "$background_script" "$background"

rw_dmg="$tmp_dir/worker-rw.dmg"
hdiutil create \
  -volname "$volume_name" \
  -srcfolder "$staging" \
  -ov \
  -format UDRW \
  -fs HFS+ \
  -size 160m \
  "$rw_dmg"

mount_dir="$tmp_dir/mount"
mkdir -p "$mount_dir"
hdiutil attach "$rw_dmg" -mountpoint "$mount_dir" -nobrowse -readwrite >/dev/null

finish_mount() {
  if [[ -d "$mount_dir" ]]; then
    retry 5 2 hdiutil detach "$mount_dir" >/dev/null 2>&1 || hdiutil detach "$mount_dir" -force >/dev/null 2>&1 || true
  fi
}
trap 'finish_mount; cleanup' EXIT

if command -v SetFile >/dev/null 2>&1; then
  SetFile -a C "$mount_dir" 2>/dev/null || true
  SetFile -a V "$mount_dir/.background" 2>/dev/null || true
fi

if ! osascript <<APPLESCRIPT
tell application "Finder"
  set dmgFolder to (POSIX file "$mount_dir" as alias)
  open dmgFolder
  set current view of container window of dmgFolder to icon view
  set toolbar visible of container window of dmgFolder to false
  set statusbar visible of container window of dmgFolder to false
  set the bounds of container window of dmgFolder to {120, 120, 780, 540}
  set viewOptions to the icon view options of container window of dmgFolder
  set arrangement of viewOptions to not arranged
  set icon size of viewOptions to 112
  set background picture of viewOptions to (POSIX file "$mount_dir/.background/background.png" as alias)
  set position of item "CMesh Worker.app" of dmgFolder to {170, 210}
  set position of item "Applications" of dmgFolder to {490, 210}
  update dmgFolder without registering applications
  delay 1
  close container window of dmgFolder
end tell
APPLESCRIPT
then
  echo "warning: Finder DMG layout customization failed; continuing with a plain DMG" >&2
fi

sync
finish_mount
trap cleanup EXIT

retry 5 3 hdiutil convert "$rw_dmg" \
  -format UDZO \
  -imagekey zlib-level=9 \
  -ov \
  -o "$output_dmg" >/dev/null
