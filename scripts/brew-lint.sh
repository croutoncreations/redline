#!/usr/bin/env bash
# Run `brew style` and `brew audit --strict` on Homebrew formula and cask files
# before they reach croutoncreations/homebrew-tap. Homebrew only audits files
# that live in a tap, so each file is copied into a throwaway local tap first.
#
# Usage:
#   scripts/brew-lint.sh dist/homebrew/Formula/redline.rb [path/to/Casks/redline.rb ...]
#
# A file's kind comes from its parent directory: Formula/ or Casks/.
# Audits run offline: snapshot formulae point at release URLs that do not exist yet.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  printf 'usage: %s path/to/Formula/name.rb|path/to/Casks/name.rb ...\n' "$0" >&2
  exit 2
fi
command -v brew >/dev/null || { printf 'brew-lint: brew is not on PATH\n' >&2; exit 2; }

tap="local/redline-lint-$$"
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ANALYTICS=1 HOMEBREW_NO_ENV_HINTS=1
brew tap-new --no-git "${tap}" >/dev/null 2>&1
tap_dir="$(brew --repository "${tap}")"
trap 'brew untap --force "${tap}" >/dev/null 2>&1 || rm -rf "${tap_dir}"' EXIT
mkdir -p "${tap_dir}/Formula" "${tap_dir}/Casks"

status=0
for file in "$@"; do
  # brew style exits 0 with "0 files inspected" for a missing path, so an
  # absent or empty file has to fail here rather than pass silently.
  if [[ ! -s "${file}" ]]; then
    printf 'brew-lint: %s is missing or empty\n' "${file}" >&2
    status=1
    continue
  fi
  name="$(basename "${file}" .rb)"
  case "$(basename "$(dirname "${file}")")" in
    Formula) kind=formula; dest="${tap_dir}/Formula/${name}.rb" ;;
    Casks) kind=cask; dest="${tap_dir}/Casks/${name}.rb" ;;
    *)
      printf 'brew-lint: %s must be under a Formula/ or Casks/ directory\n' "${file}" >&2
      status=1
      continue
      ;;
  esac
  cp "${file}" "${dest}"
  printf '==> brew style --%s %s\n' "${kind}" "${file}"
  brew style "--${kind}" "${dest}" || status=1
  printf '==> brew audit --strict --%s %s\n' "${kind}" "${file}"
  brew audit --strict "--${kind}" "${tap}/${name}" || status=1
done

if [[ ${status} -ne 0 ]]; then
  printf 'brew-lint: Homebrew checks FAILED; do not publish these files to the tap.\n' >&2
fi
exit "${status}"
