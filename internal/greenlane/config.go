package greenlane

// DefaultConfig is the shadow policy. It is compiled into the evaluator,
// which the workflow builds from the base commit, so a PR cannot loosen the
// rules that score it.
func DefaultConfig() Config {
	return Config{
		MaxChangedLines:    150,
		MaxLockfileChanges: 300,
		MaxFiles:           6,
		DocsDeps: SizeLimits{
			MaxChangedLines:    100,
			MaxLockfileChanges: 150,
			MaxFiles:           5,
		},
		RequiredChecks: []string{
			"go",
			"dashboard",
			"linux-smoke",
			"windows-smoke",
			"macos",
			"macos-intel",
		},
		DeniedPaths: DefaultDeniedPaths(),
	}
}

// DefaultDeniedPaths lists paths no green-lane PR may touch, in any lane.
func DefaultDeniedPaths() []string {
	return []string{
		// Common denied paths
		".github/workflows/**",
		".github/actions/**",
		"**/.env*",
		"**/*secret*",
		"**/*credential*",
		"**/*.pem",
		"**/*.p12",
		"**/*.keystore",
		"**/*.jks",
		"CODEOWNERS",

		// Green-lane controls: policy, prompts, and the code that scores PRs.
		".github/green-lane.yml",
		".github/green-lane/**",
		".github/prompts/**",
		"cmd/green-lane-check/**",
		"cmd/green-lane-repro-analyze/**",
		"internal/greenlane/**",

		// Test runner config
		"playwright.config.*",

		// Redline-specific
		"internal/apiauth/**",
		"internal/nativeusage/credentials*",
		"macos/Sources/RedlineKit/APICredentialStore.swift",
		".goreleaser.yaml",
		".github/release-notes-*",
		"scripts/package-macos-release.sh",
		"scripts/generate-sparkle-appcast.sh",
		"scripts/rehearse-macos-release-vm.sh",
		"scripts/lib/macos-signing.sh",
		"**/*.entitlements",
		"macos/**/Info.plist",
		"internal/store/sqlite.go",
	}
}
