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
	// packageJSONSection matches a line that opens a JSON object and leaves
	// it open: "name": {   (not "name": {} or "name": { "a": "1.0.0" }).
	packageJSONSection = regexp.MustCompile(`^\s*"([^"]+)"\s*:\s*\{\s*$`)
	// goModBlock matches a go.mod line that opens a directive block.
	goModBlock = regexp.MustCompile(`^\s*(require|exclude|replace|retract|tool|godebug|ignore)\s*\(\s*$`)
)

// npmDependencySections are the package.json objects whose entries are
// dependencies. A semver-shaped value anywhere else ("version",
// "engines", a custom "schemaVersion") is not a dependency bump.
var npmDependencySections = map[string]bool{
	"dependencies": true, "devDependencies": true, "optionalDependencies": true, "peerDependencies": true,
}

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
	var unverifiedLockfiles []string
	for _, f := range pr.Files {
		// A rename to or from a manifest or lockfile removes or rewrites
		// that dependency file wholesale (package.json -> deps.md), so it is
		// never a version bump, even when both paths qualify for the lane.
		if f.PreviousFilename != "" && f.PreviousFilename != f.Filename &&
			(isDepFile(f.PreviousFilename) || isDepFile(f.Filename)) {
			result.Message = fmt.Sprintf("%s: renamed from %s; renaming a dependency file is not a version bump", f.Filename, f.PreviousFilename)
			return result
		}
		base := path.Base(f.Filename)
		var allowed *regexp.Regexp
		switch {
		case goManifests[base]:
			allowed = goModBumpLine
		case nodeManifests[base]:
			allowed = packageJSONBumpLine
		case depLockfiles[base] && isDepFile(f.Filename):
			if msg := checkLockfile(f); msg != "" {
				result.Message = fmt.Sprintf("%s: %s", f.Filename, msg)
				return result
			}
			unverifiedLockfiles = append(unverifiedLockfiles, f.Filename)
			continue
		default:
			continue
		}
		if f.Patch == "" {
			result.Message = fmt.Sprintf("%s: no diff available to confirm it is a version bump", f.Filename)
			return result
		}
		// Dependency entries removed and added, by name, per change block
		// (a run of -/+ lines with no context line or hunk header between).
		// A bump removes and re-adds the same names in place; anything else
		// adds, drops or renames a dependency, which can bring in new
		// install scripts, or moves it to another section (devDependencies
		// -> dependencies, or between go.mod require blocks), which needs
		// context lines or another hunk between the two halves. Versions
		// are tracked as name@version so a bump must change at least one.
		added, removed := map[string]int{}, map[string]int{}
		addedVer, removedVer := map[string]int{}, map[string]int{}
		endBlock := func() string {
			msg := unpairedEntry(added, removed)
			added, removed = map[string]int{}, map[string]int{}
			return msg
		}
		// section is the package.json object or go.mod directive block
		// enclosing the current line, as far as the hunk shows (its context
		// lines, or the enclosing line git puts after the @@ header);
		// "" when it is not visible.
		goMod := allowed == goModBumpLine
		section := ""
		track := func(text string) {
			t := strings.TrimSpace(text)
			if goMod {
				if m := goModBlock.FindStringSubmatch(text); m != nil {
					section = m[1]
				} else if t == ")" {
					section = ""
				}
				return
			}
			if m := packageJSONSection.FindStringSubmatch(text); m != nil {
				section = m[1]
			} else if strings.HasPrefix(t, "}") {
				section = ""
			}
		}
		for _, line := range strings.Split(f.Patch, "\n") {
			if strings.HasPrefix(line, "@@") {
				section = ""
				if parts := strings.SplitN(line, "@@", 3); len(parts) == 3 {
					track(parts[2])
				}
			} else if len(line) > 0 {
				track(line[1:])
			}
			if !strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "-") {
				if msg := endBlock(); msg != "" {
					result.Message = fmt.Sprintf("%s: %s, not version-bumped in place", f.Filename, msg)
					return result
				}
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
			// go.mod: a bare "module version" line is a dependency only
			// inside a require block; the same shape in an exclude or
			// retract block changes resolution, not a version. Fail closed
			// when the block is not visible. (A single-line require
			// directive carries its keyword and needs no block.)
			if goMod && !entry.directive && !strings.HasPrefix(strings.TrimSpace(changed), "require ") && section != "require" {
				where := "with no require block visible in the diff context"
				if section != "" {
					where = fmt.Sprintf("in a %q block, not require", section)
				}
				result.Message = fmt.Sprintf("%s: %q changed %s", f.Filename, entry.key, where)
				return result
			}
			// Fail closed: an entry whose section the diff context does not
			// show cannot be confirmed as a dependency.
			if !goMod && !npmDependencySections[section] {
				where := "outside a dependency section"
				if section != "" {
					where = fmt.Sprintf("in %q, not a dependency section", section)
				} else {
					where = "with no dependency section visible in the diff context"
				}
				result.Message = fmt.Sprintf("%s: %q changed %s", f.Filename, entry.key, where)
				return result
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
		if msg := endBlock(); msg != "" {
			result.Message = fmt.Sprintf("%s: %s, not version-bumped in place", f.Filename, msg)
			return result
		}
		if !versionsDiffer(addedVer, removedVer) {
			result.Message = fmt.Sprintf("%s: diff changes no version, so its lockfile change is not a dependency bump", f.Filename)
			return result
		}
	}
	// Syntax and registry checks do not prove that a lockfile was generated
	// from the accepted manifest. Do not admit even plausible lockfile edits
	// until an isolated, trusted resolver comparison supplies that evidence.
	if len(unverifiedLockfiles) > 0 {
		sort.Strings(unverifiedLockfiles)
		result.Inconclusive = true
		result.Message = fmt.Sprintf("Lockfile changes require trusted resolver validation (not implemented): %s; manual review required", strings.Join(unverifiedLockfiles, ", "))
		return result
	}
	result.Pass = true
	result.Message = "OK: only docs, changelog and dependency version bumps"
	return result
}

var (
	// goSumLine is one go.sum entry: module, version (optionally
	// "/go.mod"), and an h1: hash.
	goSumLine = regexp.MustCompile(`^[^\s]+ v[^\s]+ h1:[A-Za-z0-9+/]+=*$`)
	// lockPackageKey opens a package-lock.json v2/v3 "packages" entry:
	// "node_modules/x": { or "node_modules/a/node_modules/@s/b": {.
	lockPackageKey = regexp.MustCompile(`^\s*"((?:[^"]*/)?node_modules/[^"]+)"\s*:\s*\{`)
	// lockResolved is a package-lock.json "resolved" line.
	lockResolved = regexp.MustCompile(`^\s*"resolved"\s*:\s*"([^"]*)"`)
	// lockLink is a package-lock.json "link": true line (a local symlink).
	lockLink = regexp.MustCompile(`^\s*"link"\s*:\s*true`)
)

// npmRegistry is the only tarball host a docs/deps lockfile may resolve to.
const npmRegistry = "https://registry.npmjs.org/"

// checkLockfile screens what a docs/deps PR adds to a lockfile, since the
// manifest bump alone does not constrain it (#183 Codex review). It fails
// closed: a deleted lockfile, a missing patch, or a lockfile format it cannot
// read is not a confirmed version bump. A clean result is only a syntax and
// URL screen, not resolver provenance; checkDocsDepsFiles still blocks it.
//
//   - go.sum: every added or removed line must be a well-formed entry.
//     Checksum verification alone does not prove resolver consistency.
//   - package-lock.json: every added "resolved" URL must be the npm registry
//     tarball for the package whose entry encloses it (no other hosts, git or
//     file: URLs, or another package's tarball), and no "link": true entry
//     may be added. Even an allowed URL and matching integrity cannot prove
//     that the resolver selected this package version for the manifest.
//   - pnpm-lock.yaml, yarn.lock: not validated, so never admitted.
func checkLockfile(f PRFile) string {
	if f.Status == "removed" {
		return "deleting a lockfile is not a version bump"
	}
	base := path.Base(f.Filename)
	if base == "pnpm-lock.yaml" || base == "yarn.lock" {
		return "lockfile contents cannot be validated (only go.sum and package-lock.json are checked)"
	}
	if f.Patch == "" {
		return "no diff available to validate the lockfile change"
	}
	if base == "go.sum" {
		for _, line := range strings.Split(f.Patch, "\n") {
			if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) && !goSumLine.MatchString(line[1:]) {
				return fmt.Sprintf("not a go.sum entry: %q", strings.TrimSpace(line[1:]))
			}
		}
		return ""
	}
	// package-lock.json: track the enclosing "node_modules/..." entry as far
	// as the diff shows it.
	key := ""
	for _, line := range strings.Split(f.Patch, "\n") {
		if strings.HasPrefix(line, "@@") || line == "" {
			if strings.HasPrefix(line, "@@") {
				key = ""
			}
			continue
		}
		text := line[1:]
		if m := lockPackageKey.FindStringSubmatch(text); m != nil {
			key = m[1]
		}
		if line[0] != '+' {
			continue
		}
		if lockLink.MatchString(text) {
			return "adds a \"link\": true entry (a local path, not a registry package)"
		}
		m := lockResolved.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		if key == "" {
			return fmt.Sprintf("resolved URL %q with no enclosing package entry visible in the diff context", m[1])
		}
		name := key[strings.LastIndex(key, "node_modules/")+len("node_modules/"):]
		// Require a plain tarball filename after the exact package prefix.
		// npm's URL parser normalizes dot segments and backslashes; percent
		// escapes, queries and fragments must not alter the fetched path.
		tarball, ok := strings.CutPrefix(m[1], npmRegistry+name+"/-/")
		if !ok || tarball == "" ||
			strings.Contains(name, "..") || strings.ContainsAny(name, "%\\?#") ||
			strings.Contains(tarball, "..") || strings.ContainsAny(tarball, "/\\%?#") {
			return fmt.Sprintf("%s resolves to %q, not its %s tarball", key, m[1], npmRegistry)
		}
	}
	return ""
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
