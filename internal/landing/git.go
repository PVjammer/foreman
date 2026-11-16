package landing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// git runs git in dir and returns trimmed stdout; errors carry stderr.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // never hang on a credential prompt
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return strings.TrimSpace(out.String()), fmt.Errorf("git %s: %s", args[0], tail(msg, 600))
	}
	return strings.TrimSpace(out.String()), nil
}

func gitOK(dir string, args ...string) bool {
	_, err := git(dir, args...)
	return err == nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// dirty lists tracked changes (staged or not) in a checkout; untracked files are ignored,
// since they don't block a merge or a push.
func dirty(dir string) []string {
	out, err := git(dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return []string{err.Error()}
	}
	if out == "" {
		return nil
	}
	var files []string
	for _, ln := range strings.Split(out, "\n") {
		if len(ln) > 3 {
			files = append(files, ln[3:])
		}
	}
	return files
}

// uncommittedWork lists changes in a worktree that landing would lose: tracked changes and
// untracked files that aren't ignored.
func uncommittedWork(dir string) []string {
	out, err := git(dir, "status", "--porcelain")
	if err != nil {
		return []string{err.Error()}
	}
	if out == "" {
		return nil
	}
	var files []string
	for _, ln := range strings.Split(out, "\n") {
		if len(ln) > 3 {
			files = append(files, ln[3:])
		}
	}
	return files
}

// runGate runs the configured gate command (from foreman config, never from the worker) in
// the worktree. It returns the tail of its output on failure.
func runGate(dir, gate string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", gate)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("gate %q timed out after %s", gate, timeout)
	}
	if err != nil {
		return fmt.Errorf("gate %q failed (%v):\n%s", gate, err, tail(strings.TrimSpace(string(out)), 1500))
	}
	return nil
}

// conflicts reports whether merging branch into base would conflict, without touching any
// checkout (git merge-tree --write-tree, git ≥ 2.38).
func conflicts(repo, base, branch string) (bool, string, error) {
	cmd := exec.Command("git", "-C", repo, "merge-tree", "--write-tree", "--name-only", "--no-messages", base, branch)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 1 {
			lines = lines[1:] // first line is the tree id
		}
		return true, strings.Join(lines, ", "), nil
	}
	if err != nil {
		return false, "", fmt.Errorf("git merge-tree: %v", err)
	}
	return false, "", nil
}

// patchPaths returns the file paths a unified diff touches.
func patchPaths(patch []byte) []string {
	var out []string
	for _, ln := range strings.Split(string(patch), "\n") {
		var p string
		switch {
		case strings.HasPrefix(ln, "--- "), strings.HasPrefix(ln, "+++ "):
			p = strings.TrimSpace(ln[4:])
			if i := strings.IndexByte(p, '\t'); i >= 0 {
				p = p[:i]
			}
		case strings.HasPrefix(ln, "rename from "), strings.HasPrefix(ln, "rename to "),
			strings.HasPrefix(ln, "copy from "), strings.HasPrefix(ln, "copy to "):
			_, p, _ = strings.Cut(ln, " ")
			_, p, _ = strings.Cut(p, " ")
			p = "b/" + p
		default:
			continue
		}
		if p == "/dev/null" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// checkPatchPaths refuses a docs patch that reaches outside the docs folder.
func checkPatchPaths(patch []byte) error {
	paths := patchPaths(patch)
	if len(paths) == 0 {
		return errors.New("docs.patch isn't a unified diff")
	}
	for _, p := range paths {
		rel := p
		if strings.HasPrefix(rel, "a/") || strings.HasPrefix(rel, "b/") {
			rel = rel[2:]
		} else {
			return fmt.Errorf("docs.patch path %q needs an a/ or b/ prefix", p)
		}
		if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") || strings.Contains(rel, "/../") || strings.HasPrefix(rel, ".git/") || rel == ".git" {
			return fmt.Errorf("docs.patch path %q is outside the docs folder", p)
		}
	}
	return nil
}

// applyDocsPatch applies a docs patch inside dir as a plain patch tool: git is told not to
// look for a repository above dir, so paths stay relative to the docs folder whatever mode
// it's in. Paths are checked first, and git apply itself refuses ../ without --unsafe-paths.
func applyDocsPatch(dir string, patch []byte) error {
	if err := checkPatchPaths(patch); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, args := range [][]string{{"apply", "--check", "-p1", "-"}, {"apply", "-p1", "-"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
		cmd.Stdin = bytes.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docs.patch doesn't apply: %s", tail(strings.TrimSpace(string(out)), 600))
		}
	}
	return nil
}
