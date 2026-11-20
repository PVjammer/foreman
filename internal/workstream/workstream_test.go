package workstream

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestUncommittedIgnoresCachesButNotWork(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("x = 1\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	os.MkdirAll(filepath.Join(dir, "__pycache__"), 0o755)
	os.WriteFile(filepath.Join(dir, "__pycache__", "app.cpython-312.pyc"), []byte{0}, 0o644)
	os.MkdirAll(filepath.Join(dir, ".pytest_cache"), 0o755)
	os.WriteFile(filepath.Join(dir, ".pytest_cache", "README.md"), []byte("x"), 0o644)
	if got := uncommitted(dir); len(got) != 0 {
		t.Fatalf("caches only: got %v, want none", got)
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("draft"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("x = 2\n"), 0o644)
	got := uncommitted(dir)
	if len(got) != 2 {
		t.Fatalf("got %v, want app.py and notes.txt", got)
	}
}

func TestPlanResolvesWithoutCreatingAnything(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	git(t, filepath.Join(root), "init", "-q")
	os.MkdirAll(filepath.Join(root, "api"), 0o755)
	git(t, filepath.Join(root, "api"), "init", "-q")
	p := &project.Project{Root: root, Config: project.Defaults()}
	l := ledger.New(p)

	m, ty, brief, err := Plan(p, l, Spec{Name: "auth-timeout", Type: "bug", Goal: "Fix the login timeout.", Repos: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if ty.Name != "bug" || m.Cwd != p.WorkstreamDir("auth-timeout") || len(m.Worktrees) != 1 ||
		m.Worktrees[0].Branch != "fm/auth-timeout" || m.Worktrees[0].Path != filepath.Join(m.Cwd, "api") {
		t.Errorf("plan = %+v (type %s)", m, ty.Name)
	}
	if !strings.Contains(brief, "Fix the login timeout.") || !strings.Contains(brief, "on branch `fm/auth-timeout`") {
		t.Errorf("brief:\n%s", brief)
	}
	if _, err := os.Stat(p.Path("ws")); err == nil {
		t.Errorf("Plan created %s", p.Path("ws"))
	}
	if _, _, _, err := Plan(p, l, Spec{Name: "x", Type: "feature", Goal: "g", Repos: []string{"nope"}}); err == nil {
		t.Error("want an error for a repo that isn't a git repo")
	}
	if _, _, _, err := Plan(p, l, Spec{Name: "Bad Name", Goal: "g"}); err == nil {
		t.Error("want an error for a bad name")
	}

	p.Config.Landing.Enabled = true
	if m, _, _, _ := Plan(p, l, Spec{Name: "lands", Goal: "g"}); !m.Landing {
		t.Error("landing project: plan should land")
	}
	if m, _, _, _ := Plan(p, l, Spec{Name: "nolands", Goal: "g", NoLanding: true}); m.Landing {
		t.Error("--no-landing: plan shouldn't land")
	}
}
