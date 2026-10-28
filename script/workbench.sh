#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR/tools/workbench"
case "${1:-serve}" in
  serve|--build) ;;
  *) echo "usage: $0 [serve|--build]" >&2; exit 2 ;;
esac
if [[ ! -f node_modules/pele/package.json ]]; then
  npm ci --ignore-scripts --no-audit --no-fund
fi
npm run build
if [[ "${1:-serve}" != --build ]]; then exec node serve.mjs; fi
