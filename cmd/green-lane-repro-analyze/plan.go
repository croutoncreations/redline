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
	for i := 0; i+1 < len(fields); i += 2 {
		status, file := fields[i], fields[i+1]
		if !isTestSideFile(file) {
			continue
		}
		if strings.HasPrefix(status, "D") {
			plan.Overlay = append(plan.Overlay, OverlayFile{Path: file, Delete: true})
			continue
		}
		blob, err := git(repo, "rev-parse", head+":"+file)
		if err != nil {
			return plan, fmt.Errorf("resolve %s: %w", file, err)
		}
		plan.Overlay = append(plan.Overlay, OverlayFile{Path: file, Blob: strings.TrimSpace(blob)})

		if !strings.HasSuffix(file, "_test.go") || inTestdata(file) {
			continue
		}
		headSrc, err := git(repo, "cat-file", "blob", head+":"+file)
		if err != nil {
			return plan, fmt.Errorf("read %s: %w", file, err)
		}
		baseSrc, _ := git(repo, "cat-file", "blob", plan.MergeBase+":"+file) // absent for new files
		names, err := changedTests(file, baseSrc, headSrc)
		if err != nil {
			return plan, err
		}
		dir := path.Dir(file)
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
		names := byDir[dir]
		sort.Strings(names)
		plan.Packages = append(plan.Packages, PackagePlan{Dir: dir, Tests: names})
	}
	return plan, nil
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

// changedTests returns top-level Go test functions in headSrc that are new or
// whose source differs from baseSrc. Unchanged tests in the same file are
// excluded: they already pass on base and could never "fail before".
func changedTests(file, baseSrc, headSrc string) ([]string, error) {
	headTests, err := testFuncs(file, headSrc)
	if err != nil {
		return nil, fmt.Errorf("parse %s at head: %w", file, err)
	}
	baseTests := map[string]string{}
	if baseSrc != "" {
		if parsed, err := testFuncs(file, baseSrc); err == nil {
			baseTests = parsed
		}
	}
	var names []string
	for name, src := range headTests {
		if baseTests[name] != src {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

var testNamePattern = regexp.MustCompile(`^Test($|[^a-z])`)

func testFuncs(file, src string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" || !testNamePattern.MatchString(fn.Name.Name) {
			continue
		}
		params := fn.Type.Params.List
		if len(params) != 1 || len(params[0].Names) > 1 {
			continue
		}
		if _, ok := params[0].Type.(*ast.StarExpr); !ok {
			continue
		}
		start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
		out[fn.Name.Name] = src[start:end]
	}
	return out, nil
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
