#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE="$ROOT_DIR/internal/platform/preferences/native/SettingsForm.swift"
[[ -f "$SOURCE" ]] || { echo "missing SwiftUI settings source: $SOURCE" >&2; exit 1; }
SWIFTC="$(xcrun --find swiftc)"
SDK_PATH="$(xcrun --sdk macosx --show-sdk-path)"

for arch in arm64 amd64; do
	target_arch="$arch"
	if [[ "$arch" == amd64 ]]; then
		target_arch=x86_64
	fi
	output_dir="$ROOT_DIR/resources/darwin-$arch"
	mkdir -p "$output_dir"
	"$SWIFTC" \
		-sdk "$SDK_PATH" \
		-swift-version 5 \
		-target "$target_arch-apple-macos14.2" \
		-module-name MiMacSettings \
		-O \
		-emit-library \
		-Xlinker -install_name -Xlinker @rpath/libMiMacSettings.dylib \
		-framework AppKit \
		-framework SwiftUI \
		"$SOURCE" \
		-o "$output_dir/libMiMacSettings.dylib"
	for symbol in MiMacPreferencesCreate MiMacPreferencesUpdate MiMacPreferencesClose; do
		/usr/bin/nm -gU "$output_dir/libMiMacSettings.dylib" | /usr/bin/grep -q " _$symbol$" || {
			echo "missing native settings export: $symbol" >&2
			exit 1
		}
	done
done

echo "Built native SwiftUI settings libraries for darwin-arm64 and darwin-amd64."
