package greenlane

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Lane names the rule set a PR is evaluated under. Both lanes are shadow
// only: they report what the gate would do and never merge anything.
type Lane string

const (
	// LaneCode is the default lane: every rule applies, including the
	// fails-before/passes-after repro.
	LaneCode Lane = "code"
	// LaneDocsDeps covers PRs that only touch docs, the changelog and
	// dependency manifests/lockfiles. It skips the repro rule, applies
	// tighter size caps, limits manifest edits to version bumps, and keeps
	// the same denied-path, CI, reviewer and duplicate rules.
	LaneDocsDeps Lane = "docs-deps"
)

// Manifests and lockfiles a docs/deps PR may touch, by base name.
var (
	goManifests   = map[string]bool{"go.mod": true}
	nodeManifests = map[string]bool{"package.json": true}
	depLockfiles  = map[string]bool{"go.sum": true, "package-lock.json": true, "pnpm-lock.yaml": true, "yarn.lock": true}
)

// isDocsFile reports whether file is Markdown documentation (README, docs/,
// CHANGELOG.md, CONTRIBUTING.md, ...). Only the .md extension qualifies, so
// scripts or source files under docs/ stay in the code lane. Markdown inside
// Go source trees (cmd/, internal/) or any testdata/ directory is not
// documentation either: tests can read it and go:embed can ship it, so a
// change there needs the repro rule. Denied paths such as
// .github/release-notes-*.md are still rejected by the Denied Paths rule.
func isDocsFile(file string) bool {
	if !strings.EqualFold(path.Ext(file), ".md") {
		return false
	}
	if strings.HasPrefix(file, "cmd/") || strings.HasPrefix(file, "internal/") {
		return false
	}
	for _, part := range strings.Split(path.Dir(file), "/") {
		if part == "testdata" {
			return false
		}
	}
	return true
}

func isDepFile(file string) bool {
	base := path.Base(file)
	return goManifests[base] || nodeManifests[base] || depLockfiles[base]
}

// selectLane picks LaneDocsDeps only when every file in the PR is docs or a
// dependency manifest/lockfile; anything else is LaneCode.
func selectLane(pr PRInfo) Lane {
	if len(pr.Files) == 0 {
		return LaneCode
	}
	for _, f := range pr.Files {
		if !isDocsFile(f.Filename) && !isDepFile(f.Filename) {
			return LaneCode
		}
	}
	return LaneDocsDeps
}

var (
	// go.mod lines a version bump may add or remove: require entries,
	// the require block delimiters, and the go/toolchain directives.
	goModBumpLine = regexp.MustCompile(`^\s*(require\s+)?(\(|\)|[A-Za-z0-9._~/-]+\s+v[0-9][^\s]*(\s*//\s*indirect)?)\s*$|^\s*(go\s+[0-9][0-9.]*|toolchain\s+go[0-9][0-9.]*)\s*$|^\s*$`)
	// package.json lines a version bump may add or remove: a "name":
	// "version" pair whose value looks like a semver range. Script entries
	// ("test": "rm -rf /") never look like a version, so they fail.
	packageJSONBumpLine = regexp.MustCompile(`^\s*"[@A-Za-z0-9._/-]+"\s*:\s*"[~^]?[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*"\s*,?\s*$`)
)

// checkDocsDepsFiles enforces the docs/deps lane's file-type rule: manifest
// edits must be version bumps only. A replace directive, a new script or an
// unreadable patch means the PR is not a plain dependency bump.
func (e *Evaluator) checkDocsDepsFiles(pr PRInfo) RuleResult {
	result := RuleResult{Name: "File Types"}
	for _, f := range pr.Files {
		base := path.Base(f.Filename)
		var allowed *regexp.Regexp
		switch {
		case goManifests[base]:
			allowed = goModBumpLine
		case nodeManifests[base]:
			allowed = packageJSONBumpLine
		default:
			continue
		}
		if f.Patch == "" {
			result.Message = fmt.Sprintf("%s: no diff available to confirm it is a version bump", f.Filename)
			return result
		}
		bumped := false
		for _, line := range strings.Split(f.Patch, "\n") {
			if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
				continue
			}
			changed := line[1:]
			if !allowed.MatchString(changed) {
				result.Message = fmt.Sprintf("%s: change is not a version bump: %q", f.Filename, strings.TrimSpace(changed))
				return result
			}
			if t := strings.TrimSpace(changed); t != "" && t != "(" && t != ")" {
				bumped = true
			}
		}
		// A whitespace-only manifest edit would satisfy the Size rule's
		// lockfile-needs-manifest check without bumping anything, which is a
		// lockfile-only change in disguise. There is no lockfile exception.
		if !bumped {
			result.Message = fmt.Sprintf("%s: diff changes no version, so its lockfile change is not a dependency bump", f.Filename)
			return result
		}
	}
	result.Pass = true
	result.Message = "OK: only docs, changelog and dependency version bumps"
	return result
}

func (e *Evaluator) laneLimits(lane Lane) SizeLimits {
	if lane == LaneDocsDeps {
		return e.config.DocsDeps
	}
	return SizeLimits{
		MaxChangedLines:    e.config.MaxChangedLines,
		MaxLockfileChanges: e.config.MaxLockfileChanges,
		MaxFiles:           e.config.MaxFiles,
	}
}
