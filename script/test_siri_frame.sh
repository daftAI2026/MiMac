#!/bin/sh
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
NATIVE_DIR="$ROOT_DIR/internal/platform/siriglass/native"
xcrun swift build --package-path "$NATIVE_DIR" -c release --product SiriGlassBridge --build-system native
LIB_DIR="$NATIVE_DIR/.build/arm64-apple-macosx/release"
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/mimac-frame-contract.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM
xcrun swiftc -parse-as-library -I "$LIB_DIR/Modules" -I "$LIB_DIR/CGlassPrivateABI.build" -I "$LIB_DIR/CGlassCompatibility.build" -L "$LIB_DIR" -lSiriGlassBridge \
    -Xlinker -rpath -Xlinker "$LIB_DIR" \
    "$NATIVE_DIR/Contracts/FrameContract.swift" -o "$TEST_DIR/frame-contract"
"$TEST_DIR/frame-contract"
