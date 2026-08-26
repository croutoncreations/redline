#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${repository_root}/scripts/lib/macos-signing.sh"

identities='  1) AAA "Apple Development: Example (TEAM)"
  2) BBB "Developer ID Application: Crouton Creations, LLC (TEAM)"
     2 valid identities found'

test "$(REDLINE_CODESIGN_IDENTITIES="${identities}" resolve_redline_sign_identity auto)" = \
  "Developer ID Application: Crouton Creations, LLC (TEAM)"
test "$(REDLINE_CODESIGN_IDENTITIES="${identities}" resolve_redline_sign_identity -)" = "-"
test "$(REDLINE_CODESIGN_IDENTITIES="${identities}" resolve_redline_sign_identity 'Apple Development: Example (TEAM)')" = \
  "Apple Development: Example (TEAM)"
test "$(REDLINE_CODESIGN_IDENTITIES='0 valid identities found' resolve_redline_sign_identity auto)" = "-"

printf 'macOS signing policy tests passed.\n'
