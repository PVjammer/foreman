package workstream

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
