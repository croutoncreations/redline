package greenlane

import (
	"fmt"
	"path"
	"regexp"
	"sort"
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
	return strings.EqualFold(path.Ext(file), ".md") && !inGoSourceOrTestdata(file)
}

// isDepFile reports whether file is a Go/npm manifest or lockfile that is a
// real dependency file. The same Go-source/testdata exclusion as docs
// applies: a package.json fixture under testdata/, or one under internal/
// that go:embed ships, is test or program input, not a dependency bump.
func isDepFile(file string) bool {
	base := path.Base(file)
	return (goManifests[base] || nodeManifests[base] || depLockfiles[base]) && !inGoSourceOrTestdata(file)
}

// inGoSourceOrTestdata reports whether file lives in a Go source tree (cmd/,
// internal/) or under any testdata/ directory, where tests and go:embed can
// consume it.
func inGoSourceOrTestdata(file string) bool {
	if strings.HasPrefix(file, "cmd/") || strings.HasPrefix(file, "internal/") {
		return true
	}
	for _, part := range strings.Split(path.Dir(file), "/") {
		if part == "testdata" {
			return true
		}
	}
	return false
}

// selectLane picks LaneDocsDeps only when every file in the PR is docs or a
// dependency manifest/lockfile; anything else is LaneCode.
func selectLane(pr PRInfo) Lane {
	if len(pr.Files) == 0 {
		return LaneCode
	}
	for _, f := range pr.Files {
		// A rename qualifies only if its source path does too: renaming
		// code or a test input to docs/x.md removes code-side content.
		for _, p := range f.Paths() {
			if !isDocsFile(p) && !isDepFile(p) {
				return LaneCode
			}
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
	// packageJSONEntry extracts the dependency name and version from a bump line.
	packageJSONEntry = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*"([^"]+)"`)
)

// manifestEntry is what one matched manifest line says: a dependency (or a
// go/toolchain directive) and its version.
type manifestEntry struct {
	key, version string
	directive    bool // go or toolchain: may appear or vanish in a Go bump
}

// parseManifestEntry reads a matched manifest line. ok is false for lines
// that carry no version: blank lines and the require block delimiters.
// Formatting and "// indirect" markers are not part of the entry, so
// re-indenting a line or toggling indirect is not a version change.
func parseManifestEntry(goMod bool, line string) (e manifestEntry, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || t == "(" || t == ")" {
		return e, false
	}
	if goMod {
		fields := strings.Fields(strings.TrimPrefix(t, "require "))
		if len(fields) < 2 || fields[0] == "(" {
			return e, false
		}
		return manifestEntry{key: fields[0], version: fields[1], directive: fields[0] == "go" || fields[0] == "toolchain"}, true
	}
	m := packageJSONEntry.FindStringSubmatch(t)
	if m == nil {
		return e, false
	}
	return manifestEntry{key: m[1], version: m[2]}, true
}

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
		// Dependency entries removed and added, by name. A bump removes and
		// re-adds the same names; anything else adds, drops or renames a
		// dependency, which can bring in new install scripts. Versions are
		// tracked as name@version so a bump must change at least one.
		added, removed := map[string]int{}, map[string]int{}
		addedVer, removedVer := map[string]int{}, map[string]int{}
		for _, line := range strings.Split(f.Patch, "\n") {
			if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
				continue
			}
			changed := line[1:]
			if !allowed.MatchString(changed) {
				result.Message = fmt.Sprintf("%s: change is not a version bump: %q", f.Filename, strings.TrimSpace(changed))
				return result
			}
			entry, ok := parseManifestEntry(allowed == goModBumpLine, changed)
			if !ok {
				continue
			}
			names, versions := added, addedVer
			if line[0] == '-' {
				names, versions = removed, removedVer
			}
			versions[entry.key+"@"+entry.version]++
			// go/toolchain directives are not paired: a Go bump may drop
			// or add the toolchain line (#179).
			if !entry.directive {
				names[entry.key]++
			}
		}
		// A manifest edit that changes no version (whitespace, re-indenting,
		// toggling "// indirect") would satisfy the Size rule's
		// lockfile-needs-manifest check without bumping anything, which is a
		// lockfile-only change in disguise. There is no lockfile exception.
		if !versionsDiffer(addedVer, removedVer) {
			result.Message = fmt.Sprintf("%s: diff changes no version, so its lockfile change is not a dependency bump", f.Filename)
			return result
		}
		if msg := unpairedEntry(added, removed); msg != "" {
			result.Message = fmt.Sprintf("%s: %s, not version-bumped", f.Filename, msg)
			return result
		}
	}
	result.Pass = true
	result.Message = "OK: only docs, changelog and dependency version bumps"
	return result
}

// versionsDiffer reports whether the added and removed name@version
// multisets differ, i.e. the diff changes at least one version.
func versionsDiffer(added, removed map[string]int) bool {
	if len(added) != len(removed) {
		return true
	}
	for k, n := range added {
		if removed[k] != n {
			return true
		}
	}
	return false
}

// unpairedEntry describes the first dependency that was added or removed
// without a matching entry on the other side, in sorted order so the message
// is stable. It returns "" when every name is removed and re-added equally.
func unpairedEntry(added, removed map[string]int) string {
	names := make([]string, 0, len(added)+len(removed))
	for k := range added {
		names = append(names, k)
	}
	for k := range removed {
		if _, ok := added[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		switch {
		case added[k] > removed[k]:
			return fmt.Sprintf("dependency %q is added", k)
		case removed[k] > added[k]:
			return fmt.Sprintf("dependency %q is removed", k)
		}
	}
	return ""
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
