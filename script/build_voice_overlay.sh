#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NATIVE_DIR="$ROOT_DIR/internal/platform/siriglass/native"
RESOURCE_DIR="$ROOT_DIR/resources/darwin-arm64"
LIBRARY_NAME="libSiriGlassBridge.dylib"
LIBRARY_DEST="$RESOURCE_DIR/$LIBRARY_NAME"
MODE="${1:-auto}"

if (( $# > 1 )); then
	echo "usage: $0 [auto|core-only|siri-required]" >&2
	exit 2
fi
case "$MODE" in
	auto|core-only|siri-required) ;;
	*)
		echo "usage: $0 [auto|core-only|siri-required]" >&2
		exit 2
		;;
esac

bridge_unavailable() {
	if [[ "$MODE" == siri-required ]]; then
		echo "Cannot build required native display bridge: $1" >&2
		exit 1
	fi
	echo "Skipping optional native display bridge: $1"
	exit 0
}

# This is an owned generated resource. Remove only it, including when another
# host must not reuse a stale ABI image from this checkout.
rm -f "$LIBRARY_DEST"

if [[ "$MODE" == core-only ]]; then
	echo "Selected core-only release profile; Siri glass bridge excluded."
	exit 0
fi

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
	if [[ "$MODE" == siri-required ]]; then
		echo "Cannot build required Siri glass bridge: requires macOS arm64." >&2
		exit 1
	fi
	echo "Skipping Siri glass bridge: requires macOS arm64."
	exit 0
fi

product_version="$(/usr/bin/sw_vers -productVersion)"
product_major="${product_version%%.*}"
if [[ ! "$product_major" =~ ^[0-9]+$ ]] || (( product_major < 26 )); then
	if [[ "$MODE" == siri-required ]]; then
		echo "Cannot build required native display bridge: public API requires macOS 26 or later; found $product_version." >&2
		exit 1
	fi
	echo "Skipping native display bridge: public API requires macOS 26 or later; found $product_version."
	exit 0
fi

[[ -f "$NATIVE_DIR/Package.swift" ]] || {
	bridge_unavailable "native package is missing: $NATIVE_DIR/Package.swift"
}

if ! /usr/bin/xcrun swift build \
	--package-path "$NATIVE_DIR" \
	--configuration release \
	--product SiriGlassBridge \
	--build-system native; then
	bridge_unavailable "SwiftPM build failed."
fi

library_source="$NATIVE_DIR/.build/arm64-apple-macosx/release/$LIBRARY_NAME"
[[ -s "$library_source" ]] || bridge_unavailable "SwiftPM output is missing: $library_source"
/usr/bin/lipo "$library_source" -verify_arch arm64 || bridge_unavailable "compiled library is not arm64."

for symbol in \
	mimac_siri_glass_abi_version \
	mimac_siri_glass_check_compatibility \
	mimac_siri_glass_create \
	mimac_siri_glass_attach_rect \
	mimac_siri_glass_attach_rect_with_clip \
	mimac_siri_glass_update \
	mimac_siri_glass_update_frame \
	mimac_siri_glass_destroy \
	mimac_siri_glass_last_error; do
	if ! /usr/bin/nm -gU "$library_source" | /usr/bin/grep -Eq " _$symbol$"; then
		bridge_unavailable "compiled library export is missing: $symbol"
	fi
done

if ! python3 "$ROOT_DIR/script/check_native_display.py" "$library_source"; then
	bridge_unavailable "installed capabilities are unavailable."
fi

mkdir -p "$RESOURCE_DIR"
cp "$library_source" "$LIBRARY_DEST"
echo "Built optional Siri glass bridge resource: $LIBRARY_DEST"
