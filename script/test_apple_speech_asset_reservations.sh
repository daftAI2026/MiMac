#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/mimac-apple-speech-contract.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT HUP INT TERM
BRIDGE_SOURCE=${APPLE_SPEECH_BRIDGE_SOURCE:-"$ROOT_DIR/internal/adapters/applespeech/native/AppleSpeechBridge.swift"}

for contract in AssetReservationContract AssetErrorClassificationContract; do
	xcrun swiftc -parse-as-library \
		-o "$TEST_DIR/$contract" \
		"$BRIDGE_SOURCE" \
		"$ROOT_DIR/internal/adapters/applespeech/native/$contract.swift"
	"$TEST_DIR/$contract"
done
