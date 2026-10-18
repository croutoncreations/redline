package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result files written by runRepro and read by analyze. Index i matches
// plan.Packages[i].
func resultFile(dir, side string, i int) string {
	return filepath.Join(dir, fmt.Sprintf("%s-%d.json", side, i))
}

// runRepro runs each planned package's tests twice, in fresh worktrees:
// once on the merge-base with the PR's test-side files overlaid, and once on
// the head commit. It never decides pass/fail; analyze does.
func runRepro(repo string, plan Plan, outDir string, log io.Writer) error {
	if plan.Error != "" || len(plan.Packages) == 0 {
		return nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "green-lane-repro-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	baseTree := filepath.Join(work, "base")
	headTree := filepath.Join(work, "head")
	if err := addWorktree(repo, baseTree, plan.MergeBase); err != nil {
		return err
	}
	defer removeWorktree(repo, baseTree)
	if err := addWorktree(repo, headTree, plan.Head); err != nil {
		return err
	}
	defer removeWorktree(repo, headTree)

	if err := applyOverlay(repo, baseTree, plan.Overlay); err != nil {
		return fmt.Errorf("overlay PR test files onto base: %w", err)
	}

	for i, pkg := range plan.Packages {
		for _, side := range []struct{ name, tree string }{{"base", baseTree}, {"head", headTree}} {
			if err := goTest(side.tree, pkg, resultFile(outDir, side.name, i), log); err != nil {
				return err
			}
		}
	}
	return nil
}

func addWorktree(repo, dir, commit string) error {
	_, err := git(repo, "worktree", "add", "--quiet", "--detach", dir, commit)
	return err
}

func removeWorktree(repo, dir string) {
	_, _ = git(repo, "worktree", "remove", "--force", dir)
}

// applyOverlay writes the PR's version of every test-side file (Go test
// files and testdata fixtures) into tree, and removes the ones it deletes.
func applyOverlay(repo, tree string, files []OverlayFile) error {
	for _, f := range files {
		dst := filepath.Join(tree, filepath.FromSlash(f.Path))
		if !strings.HasPrefix(dst, filepath.Clean(tree)+string(os.PathSeparator)) {
			return fmt.Errorf("overlay path %q escapes the worktree", f.Path)
		}
		if f.Delete {
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		content, err := git(repo, "cat-file", "blob", f.Blob)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := writeOverlayFile(dst, f.Mode, content); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return nil
}

// writeOverlayFile recreates a blob with its git mode, as git worktree add
// does on the head side: executable files keep their executable bit and
// symlinks stay symlinks. Without this, a fixture script would fail on base
// with "permission denied" and pass on head, which looks like a fails-before.
func writeOverlayFile(dst, mode, content string) error {
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	switch mode {
	case "120000":
		return os.Symlink(content, dst)
	case "100755":
		return os.WriteFile(dst, []byte(content), 0o755)
	case "100644", "":
		return os.WriteFile(dst, []byte(content), 0o644)
	default:
		return fmt.Errorf("unsupported git mode %s", mode)
	}
}

// goTestArgs is the exact go test invocation for one package.
func goTestArgs(pkg PackagePlan) []string {
	return []string{"test", "-count=1", "-json", "-timeout=5m",
		"-run", "^(" + strings.Join(pkg.run(), "|") + ")$",
		goPackageArg(pkg.Dir)}
}

func goTest(tree string, pkg PackagePlan, out string, log io.Writer) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	// stderr (module downloads, toolchain notices) is kept apart so the
	// JSON stream stays parseable.
	errf, err := os.Create(out + ".stderr")
	if err != nil {
		return err
	}
	defer errf.Close()
	args := goTestArgs(pkg)
	fmt.Fprintf(log, "[%s] go %s\n", filepath.Base(tree), strings.Join(args, " "))
	cmd := exec.Command("go", args...)
	cmd.Dir = tree
	cmd.Stdout = f
	cmd.Stderr = errf
	// A non-zero exit is the expected outcome on base; analyze reads the events.
	if err := cmd.Run(); err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return fmt.Errorf("go test %s: %w", pkg.Dir, err)
		}
	}
	return nil
}
