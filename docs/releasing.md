# Releasing Redline

A release has two halves. The CLI half is fully automated by GoReleaser in GitHub Actions. The
macOS app half runs on a Mac with the signing identity and is uploaded to the same GitHub release.
Both publish to the Homebrew tap `croutoncreations/homebrew-tap`.

```text
git tag vX.Y.Z && git push --tags
        |
        v
.github/workflows/release.yml  ->  GitHub release vX.Y.Z
  (GoReleaser)                       redline_X.Y.Z_<os>_<arch>.tar.gz / .zip
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
2. Confirm CI is green on `main`.

## 2. Tag

```bash
git tag -a vX.Y.Z -m "Redline X.Y.Z"
git push origin vX.Y.Z
```

The **Release** workflow runs GoReleaser, which:

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
```

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
  never a same-version or newer build; the script refuses if it finds one.
- Because the feed is served from `releases/latest/download/`, uploading `appcast.xml` to a
  GitHub **pre-release** does not affect existing users: `latest` skips pre-releases.

## 4. Update the Homebrew cask

```bash
REDLINE_VERSION=X.Y.Z ./scripts/update-homebrew-cask.sh dist/releases/Redline-X.Y.Z-universal.dmg
```

The script computes the DMG's SHA-256, renders `Casks/redline.rb`, and pushes it to the tap. It
uses your existing git credentials, or `HOMEBREW_TAP_TOKEN` if set. Preview without pushing:

```bash
REDLINE_VERSION=X.Y.Z REDLINE_CASK_DRY_RUN=1 ./scripts/update-homebrew-cask.sh dist/releases/Redline-X.Y.Z-universal.dmg
```

Skip this step for pre-releases; the cask should only ever point at a final version.

## 5. Verify

```bash
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

To test a cask before pushing it, Homebrew requires it to live in a tap:

```bash
brew tap-new --no-git local/scratch
cp /tmp/redline.rb "$(brew --repository local/scratch)/Casks/redline.rb"
brew style --cask "$(brew --repository local/scratch)/Casks/redline.rb"
brew install --cask local/scratch/redline
brew uninstall --cask local/scratch/redline && brew untap local/scratch
```

## Notes

- The CLI archives are not code-signed or notarized. Homebrew and `go install` do not require it;
  only a manual tarball download opened via Finder on macOS would trigger Gatekeeper. Revisit if
  that becomes a support burden.
- `prerelease: auto` marks tags such as `v1.2.0-rc.1` as GitHub pre-releases; Homebrew's
  `github_latest` livecheck ignores those.
