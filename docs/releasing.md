# Releasing Redline

A release has two halves. The CLI half is fully automated by GoReleaser in GitHub Actions. The
macOS app half runs on a Mac with the signing identity and is uploaded to the same GitHub release.
Both publish to the Homebrew tap `croutoncreations/homebrew-tap`.

```text
git tag vX.Y.Z && git push --tags
        |
        v
.github/workflows/release.yml
  ci-gate         tag on main + CI green for that commit (waits up to 45 min)
  homebrew-check  snapshot build; brew style + brew audit --strict on the formula
  goreleaser   ->  GitHub release vX.Y.Z
                                     redline_X.Y.Z_<os>_<arch>.tar.gz / .zip
                                     checksums.txt
                                     homebrew-tap/Formula/redline.rb   (pushed)

scripts/package-macos-release.sh  ->  Redline-X.Y.Z-universal.dmg (+ .sha256, appcast.xml)
  (local Mac, Developer ID)               |
                                          v
gh release upload vX.Y.Z Redline-X.Y.Z-universal.dmg ...
scripts/update-homebrew-cask.sh   ->  homebrew-tap/Casks/redline.rb  (pushed)
```

## 1. Prepare

1. Move the `[Unreleased]` entries in `CHANGELOG.md` under a new `## [X.Y.Z] - YYYY-MM-DD`
   heading and commit to `main`.
2. Confirm CI is green on `main`. The release workflow enforces this, but checking first saves a
   failed run.
3. Confirm the tap is not already behind: `scripts/check-homebrew-versions.sh` (needs `gh` and
   `jq`). It prints the latest release and the formula and cask versions in the tap, and fails
   if either lags.

## 2. Tag

```bash
git tag -a vX.Y.Z -m "Redline X.Y.Z"
git push origin vX.Y.Z
```

Tag a commit on `main` that CI ran on. That is normally the merge commit at the tip of `main`:
CI only runs for the head of each push, so an older commit inside a multi-commit push has no run
and is rejected. The **Release** workflow runs three jobs in order and
publishes nothing unless the first two pass:

1. **ci-gate** fails if the tagged commit is not on `main`. Otherwise it waits for the **CI**
   workflow's push run for that commit and fails unless the run concludes `success`. It polls
   for up to 45 minutes, so tagging immediately after merging is fine. If CI failed, fix `main`
   and tag the new commit, or re-run CI if the failure was a flake, then re-run the Release
   workflow from the Actions tab.
2. **homebrew-check** runs `goreleaser release --snapshot --skip=publish` and then
   `scripts/brew-lint.sh` on the generated `Formula/redline.rb`, which runs `brew style` and
   `brew audit --strict` on it. PR CI runs the same check (the `homebrew` job), together with a
   cask rendered by `scripts/update-homebrew-cask.sh`, so a `.goreleaser.yaml` change that
   breaks the formula fails on its PR rather than at release time.
3. **goreleaser** runs GoReleaser, which:

- builds `redline` for darwin, linux, and windows on amd64 and arm64 with `CGO_ENABLED=0` and the
  version baked in (`redline version` prints it);
- creates the GitHub release with the archives and `checksums.txt`, using the commit log for
  notes;
- renders `Formula/redline.rb` and pushes it to `croutoncreations/homebrew-tap` using the
  `HOMEBREW_TAP_TOKEN` repository secret (a fine-grained token with *Contents: read/write* on the
  tap only).

GoReleaser uses `mode: keep-existing`, so it never deletes assets that are already attached to the
release. Order does not matter: the DMG can be uploaded before or after the workflow runs.

Rehearse locally without publishing anything:

```bash
goreleaser release --snapshot --clean --skip=publish
ls dist/
cat dist/homebrew/Formula/redline.rb
scripts/brew-lint.sh dist/homebrew/Formula/redline.rb
```

If Homebrew on your Mac cannot audit (for example, Command Line Tools older than the OS), run
the lint in the `homebrew/brew` container instead:
`docker run --rm -v "$PWD:/w" -w /w homebrew/brew scripts/brew-lint.sh dist/homebrew/Formula/redline.rb`.

## 3. Build and upload the macOS app

On a Mac with the Developer ID identity and a `notarytool` keychain profile (see
[Release DMG and notarization](native-macos.md#release-dmg-and-notarization) for creating the
profile). The Sparkle values below are stable across releases: the feed URL is baked into every
shipped app and the key is the *public* half of the Ed25519 pair whose private half lives in the
login keychain under account `redline-release`.

```bash
REDLINE_VERSION=X.Y.Z \
REDLINE_BUILD_NUMBER=N \
REDLINE_SIGN_IDENTITY="Developer ID Application: Crouton Creations, LLC (VALSDN267N)" \
REDLINE_NOTARY_PROFILE=redline-notary \
REDLINE_SPARKLE_FEED_URL="https://github.com/croutoncreations/redline/releases/latest/download/appcast.xml" \
REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX="https://github.com/croutoncreations/redline/releases/download/vX.Y.Z/" \
REDLINE_SPARKLE_PUBLIC_KEY="i4t0Xawz/eYSWAyl+Bp2EEP+oN3EhEtrYeyB7wAD0fU=" \
REDLINE_SPARKLE_KEY_ACCOUNT="redline-release" \
./scripts/package-macos-release.sh

gh release upload vX.Y.Z \
  dist/releases/Redline-X.Y.Z-universal.dmg \
  dist/releases/Redline-X.Y.Z-universal.dmg.sha256 \
  dist/releases/appcast.xml
```

- `REDLINE_VERSION` must equal the tag without the leading `v`; the cask URL and the Sparkle
  download URL are both derived from it. The script only accepts `X.Y` or `X.Y.Z`, so for a
  pre-release tag such as `v0.2.0-rc.1` build with `REDLINE_VERSION=0.2.0` and point
  `REDLINE_SPARKLE_DOWNLOAD_URL_PREFIX` at the rc tag.
- `REDLINE_BUILD_NUMBER` is `CFBundleVersion` and must increase monotonically across every
  build Sparkle might see. Read the previous one from the last release's `appcast.xml`
  (`<sparkle:version>`) and add one.
- Output lands in `dist/releases/`. Sparkle's `generate_appcast` indexes every `Redline-*.dmg` in
  that directory and builds deltas from the older ones, so leave previous releases in place but
  never a same-version or newer build; the script refuses if it finds one. `generate_appcast`
  gives every DMG this release's download prefix, so the script repoints each older
  `Redline-A.B.C-*.dmg` enclosure at its own `vA.B.C` release; only the new DMG, its `.sha256`,
  the `.delta` files, and `appcast.xml` need uploading. The DMG being released keeps the
  configured prefix, so a pre-release's DMG stays on its `-rc` tag. An older DMG that only ever
  shipped as a pre-release has no final release to point at, so remove it before packaging.
- Because the feed is served from `releases/latest/download/`, uploading `appcast.xml` to a
  GitHub **pre-release** does not affect existing users: `latest` skips pre-releases.

## 4. Update the Homebrew cask

```bash
REDLINE_VERSION=X.Y.Z ./scripts/update-homebrew-cask.sh dist/releases/Redline-X.Y.Z-universal.dmg
```

The script computes the DMG's SHA-256, renders `Casks/redline.rb`, runs `scripts/brew-lint.sh`
on it (`brew style` and `brew audit --strict`; it refuses to push on failure), and pushes it to
the tap. It uses your existing git credentials, or `HOMEBREW_TAP_TOKEN` if set. Preview without pushing:

```bash
REDLINE_VERSION=X.Y.Z REDLINE_CASK_DRY_RUN=1 ./scripts/update-homebrew-cask.sh dist/releases/Redline-X.Y.Z-universal.dmg
```

Skip this step for pre-releases; the cask should only ever point at a final version.

This step is manual and has been missed before, so the scheduled **Homebrew drift** workflow
(`.github/workflows/homebrew-drift.yml`, daily, also runnable with *Run workflow*) runs
`scripts/check-homebrew-versions.sh`. It fails as soon as the formula lags the latest final
release, and fails once the cask is still behind 48 hours after the release was published. A
failed run notifies you by email.

## 5. Verify

```bash
scripts/check-homebrew-versions.sh                  # formula and cask both at X.Y.Z
brew update
brew install --cask croutoncreations/tap/redline     # app
brew install croutoncreations/tap/redline            # CLI (conflicts with the cask's bundled CLI; pick one)
redline version
go install github.com/croutoncreations/redline/cmd/redline@vX.Y.Z
```

The cask links the app's bundled CLI as `redline`, and the formula installs its own, and both
target `$(brew --prefix)/bin/redline`. Homebrew will not link the second one: whichever is
installed first owns the path, and the other reports a link conflict or is skipped with a warning.
Install one or the other: the app for a Mac you sit at, the formula for a headless box. The cask's
caveats say so.

To lint formula or cask files yourself, pass them to `scripts/brew-lint.sh`. Each file must sit
in a `Formula/` or `Casks/` directory, because the script uses that to tell the two apart:

```bash
scripts/brew-lint.sh dist/homebrew/Formula/redline.rb
```

To test-install a cask before pushing it, Homebrew requires it to live in a tap:

```bash
brew tap-new --no-git local/scratch
cp /tmp/redline.rb "$(brew --repository local/scratch)/Casks/redline.rb"
brew install --cask local/scratch/redline
brew uninstall --cask local/scratch/redline && brew untap local/scratch
```

## Notes

- The CLI archives are not code-signed or notarized. Homebrew and `go install` do not require it;
  only a manual tarball download opened via Finder on macOS would trigger Gatekeeper. Revisit if
  that becomes a support burden.
- `prerelease: auto` marks tags such as `v1.2.0-rc.1` as GitHub pre-releases; Homebrew's
  `github_latest` livecheck ignores those.
