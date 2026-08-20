#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"

: "${API:?DevX must provide an API port}"

umask 077
mkdir -p .redline
chmod 700 .redline

exec go run ./cmd/redline \
  --config .devx/redline.dev.yaml \
  serve \
  --listen "127.0.0.1:${API}"
