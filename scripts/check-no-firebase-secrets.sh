#!/usr/bin/env bash
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

while IFS= read -r -d '' path; do
  case "$path" in
    google-services.json|*/google-services.json)
      echo "Firebase check failed: google-services.json must not be tracked" >&2
      exit 1
      ;;
  esac
done < <(git ls-files -z)

# Report only a fixed error, never the matching credential or its contents.
status=0
git grep -q -E '"private_key"[[:space:]]*:[[:space:]]*"-----BEGIN' -- . || status=$?
if [[ "$status" == 0 ]]; then
  echo "Firebase check failed: tracked service-account private key" >&2
  exit 1
elif [[ "$status" != 1 ]]; then
  echo "Firebase check failed: could not scan tracked files" >&2
  exit 1
fi
