#!/usr/bin/env bash

resolve_redline_sign_identity() {
  local requested="${1:-auto}"
  if [[ "${requested}" != "auto" ]]; then
    printf '%s\n' "${requested}"
    return
  fi

  local identities="${REDLINE_CODESIGN_IDENTITIES:-}"
  if [[ -z "${identities}" ]]; then
    identities="$(security find-identity -v -p codesigning 2>/dev/null || true)"
  fi

  local developer_id
  developer_id="$(printf '%s\n' "${identities}" | sed -n 's/^.*"\(Developer ID Application:.*\)"$/\1/p' | head -n 1)"
  if [[ -n "${developer_id}" ]]; then
    printf '%s\n' "${developer_id}"
  else
    printf '%s\n' '-'
  fi
}
