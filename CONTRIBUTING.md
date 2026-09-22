# Contributing to Redline

Thanks for your interest in Redline. This guide covers building and running Redline from source,
running the test suites, and packaging the macOS app. End-user installation is covered in
[Getting started](docs/getting-started.md).

## Prerequisites

- Go with automatic toolchain downloads enabled (the exact version is pinned in `go.mod`)
- Node.js 18 or later and npm (dashboard tests only)
- Xcode command-line tools with a recent Swift toolchain (macOS app only)
- Network access on the first run so Go, npm, Playwright, and SwiftPM can fetch pinned dependencies

A signed-in Codex or Claude CLI is only needed to see live subscription usage. Building the
service and running its tests do not require provider credentials or spend provider quota.

## Run the service from source

Copy the example config. It is safe to use unchanged for a local smoke test: automatic dispatch is
off. Edit its providers and policies only when you want to exercise your own accounts. Redline
reuses OpenUsage when it is running, but can collect Codex and Claude subscription windows natively
when it is unavailable:

```bash
cp config.example.yaml redline.yaml
go run ./cmd/redline --config redline.yaml serve
```

The copy creates `redline.yaml`; starting the service adds `redline.db` and `api-token`, and task
runs place their artifacts under `.redline/runs`. These local files are ignored by Git. Treat
`api-token` as a secret even though the API listens only on loopback.

If you already have the native app or another Redline instance running, port `7436` is taken and
`serve` exits with `bind: address already in use`. Point the source build at any unused loopback
port with `--listen`, then use the same port in the matching `--api` value for CLI commands (replace
`17436` below if it is also in use):

```bash
go run ./cmd/redline --config redline.yaml serve --listen 127.0.0.1:17436
go run ./cmd/redline --api http://127.0.0.1:17436 status --provider codex-main
```

The status command reports live usage only when that provider's CLI is installed and signed in.
Without provider credentials, the service and dashboard still start and explain the unavailable
account state.

Only `redline serve` reads configuration and opens SQLite. Every other CLI command is a client of
the local HTTP API. See [Architecture](docs/architecture.md) for the full picture and
[CLI reference](docs/cli.md) for the commands.

## Tests

```bash
go test -race ./...
go vet ./...
go test -cover ./...
```

CI also checks formatting with `test -z "$(gofmt -l .)"`.

### Dashboard (Playwright)

```bash
npm ci
npx playwright install chromium # first run only
npm run test:dashboard
```

The Playwright suite runs the embedded dashboard in an isolated browser with deterministic API and
server-sent-event fixtures. It covers task/profile workflows, dynamic harness and model selection,
run logs, live updates, responsive layout, and loading/error states without touching a live queue or
using provider quota.

## macOS app

The native menu-bar shell lives under `macos/`. It adopts an already-running Redline service or
starts the Go service embedded in its app bundle, so the install remains a single application
rather than separate UI and daemon packages.

```bash
swift test --package-path macos --disable-xctest
./scripts/build-macos-app.sh
open dist/Redline.app
```

`--disable-xctest` skips an empty legacy XCTest bundle that some SwiftPM toolchain versions fail to
load (all suites here use the Swift Testing framework); omit it if your toolchain doesn't need the
workaround.

Configured release builds use Sparkle 2 for automatic and manual signed update checks; local builds
remain update-disabled unless an HTTPS appcast and Ed25519 public key are explicitly supplied.

See [the native macOS guide](docs/native-macos.md) for service ownership, packaging, signing, and
release configuration.

## Screenshots and demo data

Use the isolated demo mode for screenshots, recordings, and release rehearsals so no personal
repositories, paths, credentials, or live allowance data appear in captured media. See
[Demo and screenshot staging](docs/demo-staging.md).

## Cutting a release

Tagged `v*` pushes build the CLI and publish the Homebrew formula automatically; the signed macOS
DMG and its cask are produced locally. See [Releasing](docs/releasing.md).

## Submitting changes

- Keep pull requests focused; one behavior change per PR is easiest to review.
- Add or update tests for behavior you change.
- Update `CHANGELOG.md` under `[Unreleased]` for user-visible changes.
- Run the Go and (if the dashboard changed) Playwright suites before opening a PR.
