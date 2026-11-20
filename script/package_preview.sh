#!/usr/bin/env bash
# Build a local, source-identifiable preview from a clean checkout.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
build_number="${1:?usage: package_preview.sh BUILD_NUMBER [OUTPUT_DIRECTORY]}"
output_dir="${2:-$HOME/Downloads}"
packaging_python="${MIMAC_PACKAGING_PYTHON:-python3}"
profile="${MIMAC_PREVIEW_PROFILE:-core-only}"
case "$profile" in
  core-only|siri-glass) ;;
  *) echo "Expected core-only or siri-glass for MIMAC_PREVIEW_PROFILE" >&2; exit 2 ;;
esac
case "$(uname -m)" in
  arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) echo "Unsupported macOS architecture" >&2; exit 2 ;;
esac
[[ "$profile" != siri-glass || "$arch" == arm64 ]] || { echo "Siri glass requires arm64" >&2; exit 2; }
[[ "$build_number" =~ ^[1-9][0-9]{0,3}$ ]] || { echo "Build number must be between 1 and 9999" >&2; exit 2; }
cd "$root"
[[ -z "$(git status --porcelain)" ]] || { echo "Commit the source changes before packaging" >&2; exit 2; }
revision="$(git rev-parse HEAD)"
version="${MIMAC_VERSION_DATE:-$(TZ=Asia/Shanghai date +%Y.%m.%d)}"
preview_revision="${MIMAC_PREVIEW_REVISION-}"
release_identity="$(python3 script/release_naming.py identity "$version" "$preview_revision")"
stem="$(python3 script/release_naming.py stem "$arch")"
output_dir="$output_dir/$release_identity"
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd -P)"
for suffix in dmg json dmg.sha256; do
  [[ ! -e "$output_dir/$stem.$suffix" && ! -L "$output_dir/$stem.$suffix" ]] || {
    echo "Already exists: $output_dir/$stem.$suffix" >&2
    exit 2
  }
done
"$packaging_python" -c 'import ds_store, mac_alias'

stage="$(mktemp -d "$output_dir/.mimac-preview.XXXXXX")"
cleanup() {
  if [[ -d "$stage/source" ]]; then
    git -C "$root" worktree remove --force "$stage/source"
  fi
  rm -rf "$stage"
}
trap cleanup EXIT
git worktree add --detach "$stage/source" "$revision"
cd "$stage/source"

export GOWORK=off
mygo_revision=""
if [[ -n "${MIMAC_PREVIEW_MYGO_SOURCE:-}" ]]; then
  dependency="$(cd "$MIMAC_PREVIEW_MYGO_SOURCE" && pwd -P)"
  [[ -z "$(git -C "$dependency" status --porcelain)" ]] || { echo "MyGo checkout must be clean" >&2; exit 2; }
  mygo_revision="$(git -C "$dependency" rev-parse HEAD)"
  export GOWORK="$stage/local.go.work"
  python3 - "$GOWORK" "$stage/source" "$dependency" <<'PYWORK'
import json, sys
from pathlib import Path

workspace, source, dependency = sys.argv[1:]
version = next(line.split()[1] for line in (Path(source) / "go.mod").read_text().splitlines() if line.startswith("go "))
paths = "\n".join("  " + json.dumps(path, ensure_ascii=False) for path in (source, dependency))
Path(workspace).write_text(f"go {version}\n\nuse (\n{paths}\n)\n")
PYWORK
fi
export CGO_ENABLED=0

python3 script/release_version.py --date "$version" --build-number "$build_number" --source-revision "$revision" --preview-revision "$preview_revision" --output "$stage/version.txt"
script/build_native_settings.sh
if [[ "$profile" == siri-glass ]]; then
  script/build_voice_overlay.sh siri-required
else
  script/build_voice_overlay.sh core-only
fi
python3 script/package_audio_component.py --arch "$arch"
go tool mygo build -platform "darwin/$arch" -skip-dmg -skip-notarize
[[ -z "$mygo_revision" || ( "$(git -C "$dependency" rev-parse HEAD)" == "$mygo_revision" && -z "$(git -C "$dependency" status --porcelain)" ) ]]
bundle="$stage/source/dist/darwin-$arch/MiMac.app"
python3 script/verify_release.py "$bundle" --arch "$arch" --revision "$revision" \
  --version "$version" --build-number "$build_number" --release-identity "$release_identity" --component-profile "$profile" --output "$stage/$stem.json"
if [[ -n "$mygo_revision" ]]; then
  python3 - "$stage/$stem.json" "$mygo_revision" <<'PY'
import json, sys
from pathlib import Path

receipt = Path(sys.argv[1])
manifest = json.loads(receipt.read_text())
manifest["mygo_source_revision"] = sys.argv[2]
receipt.write_text(json.dumps(manifest, indent=2) + "\n")
PY
fi
"$packaging_python" script/package_dmg.py "$bundle" --output "$stage/$stem.dmg"
hdiutil verify "$stage/$stem.dmg"
(
  cd "$stage"
  shasum -a 256 "$stem.dmg" > "$stem.dmg.sha256"
)
for suffix in json dmg.sha256 dmg; do
  # Hard-link publication keeps the final move atomic and refuses an existing file.
  ln "$stage/$stem.$suffix" "$output_dir/$stem.$suffix"
done
printf 'Local preview: %s\nRelease: %s (build %s)\nSource: %s\n' "$output_dir/$stem.dmg" "$release_identity" "$build_number" "$revision"
