#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NATIVE_DIR="$ROOT_DIR/internal/platform/siriglass/native"
RESOURCE_DIR="$ROOT_DIR/resources/darwin-arm64"
LIBRARY_NAME="libSiriGlassBridge.dylib"
LIBRARY_DEST="$RESOURCE_DIR/$LIBRARY_NAME"

# This is an owned generated resource. Remove only it, including when another
# host must not reuse a stale ABI image from this checkout.
rm -f "$LIBRARY_DEST"

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
	echo "Skipping Siri glass bridge: requires macOS arm64."
	exit 0
fi

product_version="$(/usr/bin/sw_vers -productVersion)"
build_version="$(/usr/bin/sw_vers -buildVersion)"
if [[ "$product_version" != 27.0.1 || "$build_version" != 26A434 ]]; then
	echo "Skipping Siri glass bridge: requires macOS 27.0.1 build 26A434; found $product_version build $build_version."
	exit 0
fi

[[ -f "$NATIVE_DIR/Package.swift" ]] || {
	echo "Missing native Swift package: $NATIVE_DIR/Package.swift" >&2
	exit 1
}

/usr/bin/xcrun swift build \
	--package-path "$NATIVE_DIR" \
	--configuration release \
	--product SiriGlassBridge \
	--build-system native

library_source="$NATIVE_DIR/.build/arm64-apple-macosx/release/$LIBRARY_NAME"
[[ -s "$library_source" ]] || {
	echo "SwiftPM bridge library missing: $library_source" >&2
	exit 1
}
/usr/bin/lipo -verify_arch arm64 "$library_source"

for symbol in \
	mimac_siri_glass_abi_version \
	mimac_siri_glass_create \
	mimac_siri_glass_attach_rect \
	mimac_siri_glass_update \
	mimac_siri_glass_destroy \
	mimac_siri_glass_last_error; do
	if ! /usr/bin/nm -gU "$library_source" | /usr/bin/grep -Eq " _$symbol$"; then
		echo "Missing Siri glass bridge export: $symbol" >&2
		exit 1
	fi
done

mkdir -p "$RESOURCE_DIR"
cp "$library_source" "$LIBRARY_DEST"
echo "Built optional Siri glass bridge resource: $LIBRARY_DEST"
