#!/usr/bin/env bash
set -euo pipefail

project_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"

: "${API:?DevX must provide an API port}"

for _ in {1..50}; do
  if [[ -s .devx/api-token ]]; then
    token="$(tr -d '\r\n' < .devx/api-token)"
    if curl --fail --silent \
      --header "Authorization: Bearer ${token}" \
      "http://127.0.0.1:${API}/v1/health/details?window=24h" >/dev/null; then
      open "http://127.0.0.1:${API}/?access_token=${token}"
      exit 0
    fi
  fi
  sleep 0.2
done

echo "Redline development service did not become ready on port ${API}" >&2
exit 1
