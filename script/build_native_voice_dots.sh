#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_DIR="$ROOT_DIR/native/voice_dot_model"
SOURCE="$SOURCE_DIR/lib.rs"
LIBRARY_NAME="libMiMacVoiceDots.dylib"
DEPLOYMENT_TARGET="14.2"

[[ "$(uname -s)" == Darwin ]] || {
	echo "The voice dot model library requires macOS and the Apple linker." >&2
	exit 2
}
[[ -f "$SOURCE" ]] || {
	echo "missing Rust voice dot model source: $SOURCE" >&2
	exit 1
}

cd "$ROOT_DIR"
RUSTC_NAME="${MIMAC_RUSTC:-rustc}"
RUSTC="$(command -v "$RUSTC_NAME")" || {
	echo "the Rust compiler pinned in rust-toolchain.toml is required to build $LIBRARY_NAME offline." >&2
	exit 2
}
PINNED_RUST_VERSION="$(sed -n 's/^channel = "\([^"]*\)"$/\1/p' "$ROOT_DIR/rust-toolchain.toml")"
[[ -n "$PINNED_RUST_VERSION" ]] || {
	echo "rust-toolchain.toml does not pin a Rust channel." >&2
	exit 2
}
RUSTC_VERSION="$("$RUSTC" --version)"
[[ "$RUSTC_VERSION" == "rustc $PINNED_RUST_VERSION "* ]] || {
	echo "expected rustc $PINNED_RUST_VERSION from rust-toolchain.toml, found: $RUSTC_VERSION" >&2
	exit 2
}
SDK_PATH="$(xcrun --sdk macosx --show-sdk-path)"
DEVELOPER_DIR="$(xcode-select -p)"
LINKER="$(xcrun --sdk macosx --find clang)"
TEMP_ROOT="$ROOT_DIR/.build"
mkdir -p "$TEMP_ROOT"
BUILD_DIR="$(mktemp -d "$TEMP_ROOT/native-voice-dots.XXXXXX")"
STAGED_TMP=""
cleanup() {
	rm -rf "$BUILD_DIR"
	if [[ -n "$STAGED_TMP" ]]; then
		rm -f "$STAGED_TMP"
	fi
}
trap cleanup EXIT

verify_library() {
	local path="$1"
	local target_arch="$2"
	[[ -s "$path" ]] || return 1
	/usr/bin/lipo "$path" -verify_arch "$target_arch" >/dev/null 2>&1 || return 1
	local exports
	exports="$(/usr/bin/nm -gU "$path" 2>/dev/null)" || return 1
	local symbol
	for symbol in \
		mi_dot_model_abi_version \
		mi_dot_model_create \
		mi_dot_model_destroy \
		mi_dot_model_start \
		mi_dot_model_meter_rms \
		mi_dot_model_submit \
		mi_dot_model_done \
		mi_dot_model_fail \
		mi_dot_model_cancel \
		mi_dot_model_step; do
		grep -Eq " _$symbol$" <<<"$exports" || return 1
	done
	[[ "$(/usr/bin/otool -D "$path" 2>/dev/null | tail -n 1)" == "@rpath/$LIBRARY_NAME" ]] || return 1
}

for arch in arm64 amd64; do
	target_arch="$arch"
	target_triple="aarch64-apple-darwin"
	if [[ "$arch" == amd64 ]]; then
		target_arch=x86_64
		target_triple=x86_64-apple-darwin
	fi
	stdlib_dir="$("$RUSTC" --print target-libdir --target "$target_triple")"
	if [[ ! -d "$stdlib_dir" ]] || ! find "$stdlib_dir" -maxdepth 1 -name 'libcore-*.rlib' -print -quit | grep -q .; then
		echo "Rust standard libraries for $target_triple are missing from the pinned toolchain." >&2
		exit 2
	fi

	temp_library="$BUILD_DIR/$arch/$LIBRARY_NAME"
	resource_dir="$ROOT_DIR/resources/darwin-$arch"
	resource_library="$resource_dir/$LIBRARY_NAME"
	mkdir -p "$(dirname "$temp_library")" "$resource_dir"
	SDKROOT="$SDK_PATH" DEVELOPER_DIR="$DEVELOPER_DIR" "$RUSTC" \
		--edition=2021 \
		--crate-name mi_dot_model \
		--crate-type cdylib \
		--target "$target_triple" \
		-C linker="$LINKER" \
		-C opt-level=3 \
		-C debuginfo=0 \
		-C codegen-units=1 \
		-C panic=abort \
		-C link-arg=-isysroot \
		-C link-arg="$SDK_PATH" \
		-C link-arg=-mmacosx-version-min="$DEPLOYMENT_TARGET" \
		-C link-arg=-Wl,-install_name,@rpath/"$LIBRARY_NAME" \
		-o "$temp_library" "$SOURCE"
	verify_library "$temp_library" "$target_arch" || {
		echo "compiled voice dot model library failed architecture, ABI export, or install-name verification ($target_triple)." >&2
		exit 1
	}
	STAGED_TMP="$resource_library.tmp.$$"
	cp "$temp_library" "$STAGED_TMP"
	mv -f "$STAGED_TMP" "$resource_library"
	STAGED_TMP=""
	verify_library "$resource_library" "$target_arch" || {
		echo "staged voice dot model library verification failed ($target_triple)." >&2
		exit 1
	}
	printf 'Built %s for %s with %s.\n' "$LIBRARY_NAME" "$target_triple" "$RUSTC_VERSION"
done

echo "Built the offline Rust voice dot model library for darwin-arm64 and darwin-amd64."
