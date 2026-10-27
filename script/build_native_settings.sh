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
	"$SWIFTC" \
		-sdk "$SDK_PATH" \
		-swift-version 5 \
		-target "$target_arch-apple-macos14.2" \
		-module-name MiMacATVV \
		-O -emit-library \
		-Xlinker -install_name -Xlinker @rpath/libMiMacATVV.dylib \
		-framework CoreBluetooth -framework Foundation \
		"$ROOT_DIR/internal/adapters/xiaomi/atvv/native/Transport.swift" \
		-o "$output_dir/libMiMacATVV.dylib"
	for symbol in MiMacATVVTransportCreate MiMacATVVTransportRequestStop MiMacATVVTransportClose; do
		/usr/bin/nm -gU "$output_dir/libMiMacATVV.dylib" | /usr/bin/grep -q " _$symbol$" || {
			echo "missing ATVV transport export: $symbol" >&2
			exit 1
		}
	done
	xcrun --sdk macosx clang -fobjc-arc -O2 -mmacosx-version-min=14.2 \
		-arch "$target_arch" -dynamiclib \
		-install_name @rpath/libMiMacProcessTap.dylib \
		"$ROOT_DIR/internal/adapters/processtap/native/Endpoint.m" \
		-framework Foundation -framework CoreAudio \
		-o "$output_dir/libMiMacProcessTap.dylib"
	for symbol in MiMacProcessTapCreate MiMacProcessTapCopyUID MiMacProcessTapClose; do
		/usr/bin/nm -gU "$output_dir/libMiMacProcessTap.dylib" | /usr/bin/grep -q " _$symbol$" || {
			echo "missing process tap export: $symbol" >&2
			exit 1
		}
	done
	xcrun --sdk macosx clang -fobjc-arc -O2 -mmacosx-version-min=14.2 \
		-arch "$target_arch" -dynamiclib \
		-install_name @rpath/libMiMacReadyIndicator.dylib \
		"$ROOT_DIR/internal/platform/readyindicator/native/ReadyIndicator.m" \
		-framework AppKit -framework QuartzCore -framework CoreGraphics \
		-o "$output_dir/libMiMacReadyIndicator.dylib"
	for symbol in MiMacReadyIndicatorABIVersion MiMacReadyIndicatorInstall MiMacReadyIndicatorUpdate MiMacReadyIndicatorClose; do
		/usr/bin/nm -gU "$output_dir/libMiMacReadyIndicator.dylib" | /usr/bin/grep -q " _$symbol$" || {
			echo "missing ready indicator export: $symbol" >&2
			exit 1
		}
	done
	"$SWIFTC" \
		-sdk "$SDK_PATH" \
		-swift-version 5 \
		-target "$target_arch-apple-macos14.2" \
		-module-name MiMacSpeech \
		-O -emit-library \
		-Xlinker -install_name -Xlinker @rpath/libMiMacSpeech.dylib \
		-framework Speech -framework AVFoundation \
		"$ROOT_DIR/internal/adapters/applespeech/native/AppleSpeechBridge.swift" \
		-o "$output_dir/libMiMacSpeech.dylib"
	for symbol in MiMacAppleSpeechIsAvailable MiMacAppleSpeechCreate MiMacAppleSpeechWaitReady MiMacAppleSpeechTryWrite MiMacAppleSpeechBeginDrain MiMacAppleSpeechWaitDone MiMacAppleSpeechRequestCancel MiMacAppleSpeechWaitTerminated MiMacAppleSpeechCopyFinalText MiMacAppleSpeechCopyErrorMessage MiMacAppleSpeechRelease; do
		/usr/bin/nm -gU "$output_dir/libMiMacSpeech.dylib" | /usr/bin/grep -q " _$symbol$" || {
			echo "missing Apple Speech export: $symbol" >&2
			exit 1
		}
	done
done

echo "Built native settings, ATVV, process tap, ready indicator and Apple Speech libraries for darwin-arm64 and darwin-amd64."
