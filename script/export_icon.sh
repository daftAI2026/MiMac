#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ICON_TOOL="${MIMAC_ICON_TOOL:-/Applications/Icon Composer.app/Contents/Executables/ictool}"
[[ -x "$ICON_TOOL" ]] || { echo "Install Icon Composer or set MIMAC_ICON_TOOL" >&2; exit 2; }
EXPORT_DIR="$(mktemp -d -t mimac-icon)"
trap 'rm -rf "$EXPORT_DIR"' EXIT

# Use Icon Composer's documented command-line export, without reimplementing
# its material renderer. The layered source remains the canonical artwork.
for size in 1024 160; do
  "$ICON_TOOL" "$ROOT_DIR/Assets/Brand/MiMac.icon" --export-image \
    --output-file "$EXPORT_DIR/icon-$size.png" --platform macOS --rendition Default \
    --width "$size" --height "$size" --scale 1 --design-generation 27
  [[ -s "$EXPORT_DIR/icon-$size.png" ]] || { echo "Icon export produced no image" >&2; exit 2; }
done
cp "$EXPORT_DIR/icon-1024.png" "$ROOT_DIR/Assets/Brand/app-icon.png"
cp "$EXPORT_DIR/icon-160.png" "$ROOT_DIR/Assets/Brand/about-icon.png"
