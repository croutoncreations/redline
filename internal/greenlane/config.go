package greenlane

// DefaultConfig is the shadow policy. It is compiled into the evaluator,
// which the workflow builds from the base commit, so a PR cannot loosen the
// rules that score it.
func DefaultConfig() Config {
	return Config{
		AllowedBaseBranches: []string{"main"},
		AllowedAuthors:      []string{"jfox85"},
		CheckSources: map[string]CheckSource{
			"actionlint":    {AppID: 15368, WorkflowID: 321038296},
			"go":            {AppID: 15368, WorkflowID: 321038296},
			"homebrew":      {AppID: 15368, WorkflowID: 321038296},
			"dashboard":     {AppID: 15368, WorkflowID: 321038296},
			"linux-smoke":   {AppID: 15368, WorkflowID: 321038296},
			"windows-smoke": {AppID: 15368, WorkflowID: 321038296},
			"macos":         {AppID: 15368, WorkflowID: 321038296},
			"macos-intel":   {AppID: 15368, WorkflowID: 321038296},
		},
		// Retained for legacy receipt tooling; not used by the simple policy.
		ProvenanceIssuers:  map[string]ReceiptIssuer{},
		MaxChangedLines:    150,
		MaxLockfileChanges: 300,
		MaxFiles:           6,
		DocsDeps: SizeLimits{
			MaxChangedLines:    100,
			MaxLockfileChanges: 150,
			MaxFiles:           5,
		},
		RequiredChecks: []string{
			"actionlint",
			"go",
			"homebrew",
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
		// GitHub reads CODEOWNERS from the root, .github/ and docs/.
		"CODEOWNERS",
		".github/CODEOWNERS",
		"docs/CODEOWNERS",

		// Green-lane controls: policy, prompts, and the code that scores PRs.
		".github/green-lane.yml",
		".github/green-lane/**",
		".github/prompts/**",
		"cmd/green-lane-check/**",
		"cmd/green-lane-provenance/**",
		"cmd/green-lane-repro-analyze/**",
		"internal/greenlane/**",

		// Dependency lockfiles stay manual in every lane, including mixed code PRs.
		"**/go.sum",
		"**/package-lock.json",
		"**/pnpm-lock.yaml",
		"**/yarn.lock",
		"**/Package.resolved",
		"**/gradle.lockfile",
		"**/*.lock",

		// Test runner config
		"playwright.config.*",

		// Redline-specific
		"internal/apiauth/**",
		"internal/nativeusage/credentials*",
		"macos/Sources/RedlineKit/APICredentialStore.swift",
		".goreleaser.yaml",
		".github/release-notes-*",
		"scripts/brew-lint.sh",
		"scripts/check-homebrew-versions.sh",
		"scripts/update-homebrew-cask.sh",
		"scripts/package-macos-release.sh",
		"scripts/generate-sparkle-appcast.sh",
		"scripts/rehearse-macos-release-vm.sh",
		"scripts/lib/macos-signing.sh",
		"**/*.entitlements",
		"macos/**/Info.plist",
		"internal/store/sqlite.go",
	}
}
