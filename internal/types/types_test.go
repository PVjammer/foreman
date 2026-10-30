package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/project"
)

func TestBuiltinsParse(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := &project.Project{Root: t.TempDir(), Config: project.Defaults()}
	got := map[string]*Type{}
	for _, ty := range List(p) {
		got[ty.Name] = ty
	}
	for _, n := range []string{"feature", "bug", "research", "spike", "poc", "deploy"} {
		ty, ok := got[n]
		if !ok {
			t.Fatalf("missing built-in %s", n)
		}
		if ty.Description == "" || ty.Phase("work") == "" || ty.Phase("closeout") == "" {
			t.Errorf("%s: incomplete %+v", n, ty)
		}
	}
	if !got["feature"].Worktrees || got["research"].Worktrees || !got["research"].ReadOnly {
		t.Errorf("mechanics wrong: feature %+v research %+v", got["feature"], got["research"])
	}
	if ty, err := Get(p, "build"); err != nil || ty.Name != "feature" {
		t.Errorf("legacy alias build: %v %v", ty, err)
	}
}

func TestProjectTypeShadowsBuiltinAndCustomTypesAppear(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	p := &project.Project{Root: root, Config: project.Defaults()}
	os.MkdirAll(p.Path("types"), 0o755)
	os.WriteFile(filepath.Join(p.Path("types"), "feature.md"), []byte("---\ndescription: ours\nworktrees: false\nprofile: pi\n---\n## work\nDo it our way.\n"), 0o644)
	os.WriteFile(filepath.Join(p.Path("types"), "datadog.md"), []byte("---\ndescription: chase an alert\nread_only: true\n---\n## work\nQuery Datadog.\n## closeout\nUpdate the runbook.\n"), 0o644)
	ty, err := Get(p, "feature")
	if err != nil || ty.Source != "project" || ty.Description != "ours" || ty.Worktrees || ty.Profile != "pi" {
		t.Fatalf("shadowed feature = %+v, %v", ty, err)
	}
	names := []string{}
	for _, ty := range List(p) {
		names = append(names, ty.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "datadog") {
		t.Errorf("custom type missing from %v", names)
	}
}
