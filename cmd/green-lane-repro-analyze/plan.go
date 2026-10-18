package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Plan says which tests the repro check must see fail on the merge-base and
// pass on the head, and which test-side files from the PR are overlaid onto
// the merge-base. It is computed from git objects only, so building it never
// executes PR code.
type Plan struct {
	Base      string        `json:"base"`
	Head      string        `json:"head"`
	MergeBase string        `json:"merge_base"`
	Packages  []PackagePlan `json:"packages"`
	Overlay   []OverlayFile `json:"overlay"`
	Error     string        `json:"error,omitempty"`
}

// PackagePlan lists the new or changed top-level tests in one package.
type PackagePlan struct {
	Dir   string   `json:"dir"` // slash-separated, relative to the repo root
	Tests []string `json:"tests"`
}

// OverlayFile is a test-side file the PR adds, changes or deletes.
type OverlayFile struct {
	Path   string `json:"path"`
	Blob   string `json:"blob,omitempty"` // head blob; empty when deleted
	Mode   string `json:"mode,omitempty"` // git mode at head: 100644, 100755 or 120000
	Delete bool   `json:"delete,omitempty"`
}

// Tests is the flat list of test IDs the plan requires.
func (p Plan) Tests() []string {
	var ids []string
	for _, pkg := range p.Packages {
		for _, name := range pkg.Tests {
			ids = append(ids, testID(pkg.Dir, name))
		}
	}
	return ids
}

func testID(dir, name string) string { return dir + "." + name }

// goPackageArg turns a repo-relative directory into a go test package
// argument. Without the leading "./", go resolves "internal/foo" as an import
// path in GOROOT ("package internal/foo is not in std"), which is why every
// shadow run reported "Compile failed on base commit".
func goPackageArg(dir string) string {
	if dir == "" || dir == "." {
		return "."
	}
	return "./" + strings.TrimPrefix(dir, "./")
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func buildPlan(repo, base, head string) (Plan, error) {
	plan := Plan{Base: base, Head: head}
	if !shaPattern.MatchString(base) || !shaPattern.MatchString(head) {
		return plan, fmt.Errorf("base and head must be full commit SHAs (got %q, %q)", base, head)
	}
	mergeBase, err := git(repo, "merge-base", base, head)
	if err != nil {
		return plan, fmt.Errorf("merge-base: %w", err)
	}
	plan.MergeBase = strings.TrimSpace(mergeBase)

	// Diff from the merge-base, not the current base tip, so commits that
	// landed on main after the PR branched are not attributed to the PR.
	diff, err := git(repo, "diff", "--no-renames", "--name-status", "-z", plan.MergeBase, head)
	if err != nil {
		return plan, fmt.Errorf("diff: %w", err)
	}
	fields := strings.Split(strings.TrimSuffix(diff, "\x00"), "\x00")
	byDir := map[string][]string{}
	fixtureDirs := map[string]bool{} // packages whose testdata the PR changes
	testDirs := map[string]bool{}    // packages with an added, changed or deleted _test.go
	for i := 0; i+1 < len(fields); i += 2 {
		status, file := fields[i], fields[i+1]
		if !isTestSideFile(file) {
			continue
		}
		if inTestdata(file) {
			fixtureDirs[testdataOwner(file)] = true
		} else {
			testDirs[path.Dir(file)] = true
		}
		if strings.HasPrefix(status, "D") {
			plan.Overlay = append(plan.Overlay, OverlayFile{Path: file, Delete: true})
			continue
		}
		// ls-tree gives the mode with the blob. The overlay must keep it:
		// a fixture script that loses its executable bit on base fails
		// there with "permission denied" and looks like a fails-before.
		mode, blob, err := treeEntry(repo, head, file)
		if err != nil {
			return plan, fmt.Errorf("resolve %s: %w", file, err)
		}
		plan.Overlay = append(plan.Overlay, OverlayFile{Path: file, Blob: blob, Mode: mode})
	}

	// Compare each affected package's test files as a whole: a table or
	// helper in one _test.go file is often used by a test in another.
	for dir := range testDirs {
		baseSrcs, err := packageTestSources(repo, plan.MergeBase, dir)
		if err != nil {
			return plan, err
		}
		headSrcs, err := packageTestSources(repo, head, dir)
		if err != nil {
			return plan, err
		}
		names, err := changedPackageTests(baseSrcs, headSrcs)
		if err != nil {
			return plan, err
		}
		byDir[dir] = append(byDir[dir], names...)
	}

	// A PR can add a regression case to a golden or table test only by
	// adding a fixture: no test function changes, but the test that walks
	// testdata/ now runs the new case. For such fixture-only packages,
	// schedule the tests that read testdata (or all of them if none
	// visibly do). Planning more tests only adds fails-before requirements,
	// so it cannot fake a repro. Packages that already have changed tests
	// keep exactly those: adding unchanged golden tests there would make a
	// genuine repro fail because those tests pass on base.
	for dir := range fixtureDirs {
		if len(byDir[dir]) > 0 {
			continue
		}
		names, err := fixtureTests(repo, head, dir)
		if err != nil {
			return plan, err
		}
		byDir[dir] = append(byDir[dir], names...)
	}

	dirs := make([]string, 0, len(byDir))
	for dir, names := range byDir {
		if len(names) > 0 {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		plan.Packages = append(plan.Packages, PackagePlan{Dir: dir, Tests: uniqueSorted(byDir[dir])})
	}
	return plan, nil
}

// testdataOwner returns the package directory a testdata path belongs to:
// everything before its first testdata component ("." at the repo root).
func testdataOwner(file string) string {
	parts := strings.Split(file, "/")
	for i, part := range parts {
		if part == "testdata" {
			if i == 0 {
				return "."
			}
			return strings.Join(parts[:i], "/")
		}
	}
	return path.Dir(file)
}

// fixtureTests lists the tests in dir (at commit) that read testdata: tests
// whose body, or a declaration in the package's test files that they use,
// has a string literal mentioning "testdata". If no test visibly does (the
// path may be built in non-test code), every test in the package is
// returned.
func fixtureTests(repo, commit, dir string) ([]string, error) {
	srcs, err := packageTestSources(repo, commit, dir)
	if err != nil {
		return nil, err
	}
	// Merge the package's test files: a test often reads a path declared
	// in another _test.go file of the same package.
	pkg, err := parsePackage(srcs)
	if err != nil {
		return nil, err
	}
	reads := closure(pkg, pkg.readsTestdata)
	var reading []string
	for _, name := range pkg.tests {
		if reads[name] {
			reading = append(reading, name)
		}
	}
	if len(reading) > 0 {
		return reading, nil
	}
	return pkg.tests, nil
}

func uniqueSorted(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// treeEntry returns the git mode and blob of file at commit.
func treeEntry(repo, commit, file string) (mode, blob string, err error) {
	out, err := git(repo, "ls-tree", "-z", commit, "--", file)
	if err != nil {
		return "", "", err
	}
	meta, name, ok := strings.Cut(strings.TrimSuffix(out, "\x00"), "\t")
	fields := strings.Fields(meta) // "<mode> <type> <object>"
	if !ok || name != file || len(fields) != 3 || fields[1] != "blob" {
		return "", "", fmt.Errorf("unexpected ls-tree output %q", out)
	}
	return fields[0], fields[2], nil
}

// isTestSideFile reports whether a PR change belongs on the base commit for
// the fails-before run: Go test files and anything under a testdata
// directory. Copying only *_test.go files left new fixtures missing on base,
// so a test could fail there for the wrong reason and look like a repro.
func isTestSideFile(file string) bool {
	return strings.HasSuffix(file, "_test.go") || inTestdata(file)
}

func inTestdata(file string) bool {
	for _, part := range strings.Split(path.Dir(file), "/") {
		if part == "testdata" {
			return true
		}
	}
	return false
}

// packageTestSources reads the _test.go files directly in dir at commit,
// keyed by path. A directory missing at commit yields an empty map.
func packageTestSources(repo, commit, dir string) (map[string]string, error) {
	args := []string{"ls-tree", "-z", "--name-only", commit}
	if dir != "." {
		args = append(args, "--", dir+"/")
	}
	out, err := git(repo, args...)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	srcs := map[string]string{}
	for _, file := range strings.Split(strings.TrimSuffix(out, "\x00"), "\x00") {
		if !strings.HasSuffix(file, "_test.go") || path.Dir(file) != path.Clean(dir) {
			continue
		}
		src, err := git(repo, "cat-file", "blob", commit+":"+file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		srcs[file] = src
	}
	return srcs, nil
}

// changedTests is changedPackageTests for a package with one test file.
func changedTests(file, baseSrc, headSrc string) ([]string, error) {
	base := map[string]string{}
	if baseSrc != "" {
		base[file] = baseSrc
	}
	return changedPackageTests(base, map[string]string{file: headSrc})
}

// changedPackageTests returns the package's top-level Go test functions at
// head that are new, whose source changed, or that use (across all of the
// package's test files, transitively) a top-level declaration that is new,
// changed or removed. That covers table-driven tests: adding a case to
// `var cases = ...` in helpers_test.go leaves TestX in x_test.go unchanged
// but changes what it runs. Import changes count too: a file whose imports
// changed has all its tests planned, and a changed blank/dot import, init
// function or TestMain plans every test in the package, since those change
// the whole test binary. Tests that use nothing changed are excluded: they
// already pass on base and could never "fail before". Planning more tests
// only adds fails-before requirements, so this cannot fake a repro.
func changedPackageTests(baseSrcs, headSrcs map[string]string) ([]string, error) {
	head, err := parsePackage(headSrcs)
	if err != nil {
		return nil, err
	}
	base, err := parsePackage(baseSrcs)
	if err != nil {
		// An unparsable base only makes everything look changed.
		base = newTestFile()
	}

	seed := map[string]bool{}
	for name, src := range head.decls {
		if b, ok := base.decls[name]; !ok || b != src {
			seed[name] = true
		}
	}
	for name := range base.decls {
		if _, ok := head.decls[name]; !ok {
			seed[name] = true // removed: anything still using the name changed
		}
	}
	everything := seed["init"] || seed["TestMain"] || head.sideEffectImports != base.sideEffectImports
	for file, imports := range head.imports {
		if b, ok := base.imports[file]; !ok || b != imports {
			for _, name := range head.declaredIn[file] {
				seed[name] = true
			}
		}
	}
	dirty := closure(head, seed)

	var names []string
	for _, name := range head.tests {
		if everything || dirty[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

// parsePackage merges the package's test files into one declaration graph.
// Files are visited in sorted order so equal sources give equal graphs.
func parsePackage(srcs map[string]string) (testFile, error) {
	files := make([]string, 0, len(srcs))
	for f := range srcs {
		files = append(files, f)
	}
	sort.Strings(files)
	pkg := newTestFile()
	var effects []string
	for _, file := range files {
		tf, err := parseTestFile(file, srcs[file])
		if err != nil {
			return testFile{}, fmt.Errorf("parse %s: %w", file, err)
		}
		pkg.tests = append(pkg.tests, tf.tests...)
		for name, src := range tf.decls {
			pkg.decls[name] += src
		}
		for name, uses := range tf.uses {
			pkg.uses[name] = append(pkg.uses[name], uses...)
		}
		for name := range tf.readsTestdata {
			pkg.readsTestdata[name] = true
		}
		pkg.imports[file] = tf.imports[file]
		pkg.declaredIn[file] = tf.declaredIn[file]
		if tf.sideEffectImports != "" {
			effects = append(effects, tf.sideEffectImports)
		}
	}
	sort.Strings(effects)
	pkg.sideEffectImports = strings.Join(effects, "\n")
	return pkg, nil
}

// closure returns seed plus every top-level declaration in tf that uses a
// member of the result, computed to a fixed point.
func closure(tf testFile, seed map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range seed {
		out[k] = v
	}
	for changed := true; changed; {
		changed = false
		for name, uses := range tf.uses {
			if out[name] {
				continue
			}
			for _, u := range uses {
				if out[u] {
					out[name], changed = true, true
					break
				}
			}
		}
	}
	return out
}

var testNamePattern = regexp.MustCompile(`^Test($|[^a-z])`)

// testFile is the top-level structure of one Go test file, or of a whole
// package's test files after parsePackage.
type testFile struct {
	tests         []string            // runnable TestXxx(t *T) functions
	decls         map[string]string   // top-level name -> its declaration's source
	uses          map[string][]string // top-level name -> identifiers it references
	readsTestdata map[string]bool     // top-level name -> has a "testdata" string literal
	imports       map[string]string   // file -> its import declarations' source
	declaredIn    map[string][]string // file -> top-level names it declares
	// sideEffectImports lists blank and dot imports, which change the
	// whole test binary rather than one file's identifiers.
	sideEffectImports string
}

func newTestFile() testFile {
	return testFile{
		decls: map[string]string{}, uses: map[string][]string{}, readsTestdata: map[string]bool{},
		imports: map[string]string{}, declaredIn: map[string][]string{},
	}
}

func parseTestFile(file, src string) (testFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return testFile{}, err
	}
	out := newTestFile()
	// The file header (build constraints and other leading directives, and
	// the package clause) is compared with the imports: changing
	// //go:build windows to linux makes an unchanged test runnable on the
	// workflow's runner, and package p vs p_test changes what it can see.
	var imports strings.Builder
	imports.WriteString(src[:fset.Position(f.Name.End()).Offset] + "\n")
	var effects []string
	text := func(n ast.Node) string {
		return src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset]
	}
	idents := func(n ast.Node) []string {
		var ids []string
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				ids = append(ids, id.Name)
			}
			return true
		})
		return ids
	}
	mentionsTestdata := func(n ast.Node) bool {
		found := false
		ast.Inspect(n, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, "testdata") {
				found = true
			}
			return !found
		})
		return found
	}
	add := func(name, decl string, n ast.Node) {
		if name == "_" || name == "" {
			return
		}
		// Several specs can share a name only in invalid code (or init);
		// keep both.
		out.decls[name] += decl
		out.declaredIn[file] = append(out.declaredIn[file], name)
		out.uses[name] = append(out.uses[name], idents(n)...)
		if mentionsTestdata(n) {
			out.readsTestdata[name] = true
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) == 1 {
				// Methods are keyed by receiver type and by method name: a
				// test may name only the type (checker{}.ok) or only the
				// method (NewCalc().helper(), with the type declared in
				// non-test code). A collision with another name only plans
				// more tests, which is the safe direction.
				add(receiverType(d.Recv.List[0].Type), text(d), d)
				add(d.Name.Name, text(d), d)
			} else {
				add(d.Name.Name, text(d), d)
			}
			if d.Recv == nil && isTestFunc(d) {
				out.tests = append(out.tests, d.Name.Name)
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				imports.WriteString(text(d) + "\n")
				for _, spec := range d.Specs {
					if s, ok := spec.(*ast.ImportSpec); ok && s.Name != nil && (s.Name.Name == "_" || s.Name.Name == ".") {
						effects = append(effects, s.Name.Name+" "+s.Path.Value)
					}
				}
				continue
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n.Name, text(s), s)
					}
				case *ast.TypeSpec:
					add(s.Name.Name, text(s), s)
				}
			}
		}
	}
	out.imports[file] = imports.String()
	sort.Strings(effects)
	out.sideEffectImports = strings.Join(effects, "\n")
	return out, nil
}

func isTestFunc(fn *ast.FuncDecl) bool {
	if fn.Name.Name == "TestMain" || !testNamePattern.MatchString(fn.Name.Name) {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	_, ok := params[0].Type.(*ast.StarExpr)
	return ok
}

func receiverType(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func writePlan(file string, plan Plan) error { return writeJSON(file, plan) }

func readPlan(file string) (Plan, error) {
	var plan Plan
	data, err := os.ReadFile(file)
	if err != nil {
		return plan, err
	}
	return plan, json.Unmarshal(data, &plan)
}

func writeJSON(file string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}
