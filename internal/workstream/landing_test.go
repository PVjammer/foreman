package workstream

import (
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/ledger"
	"github.com/PVjammer/foreman/internal/project"
	"github.com/PVjammer/foreman/internal/types"
)

func briefFor(t *testing.T, cfg project.Config, m *ledger.Meta) string {
	t.Helper()
	p := &project.Project{Root: t.TempDir(), Config: cfg}
	l := ledger.New(p)
	if err := l.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	ty, err := types.Get(p, "feature")
	if err != nil {
		t.Fatal(err)
	}
	b, err := renderBrief(p, l, m, ty)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBriefLandingSectionOnlyWhenEnabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	wts := []ledger.Worktree{{Repo: ".", Path: "/x/root", Branch: "fm/w"}, {Repo: "site", Path: "/x/site", Branch: "fm/w"}}

	off := project.Defaults()
	b := briefFor(t, off, &ledger.Meta{Name: "w", Kind: "feature", Worktrees: wts})
	if strings.Contains(b, "## Landing") || strings.Contains(b, "landing.md") {
		t.Fatalf("landing text in a non-landing brief:\n%s", b)
	}

	on := project.Defaults()
	on.Landing.Enabled = true
	on.Docs.Mode = "local"
	on.Landing.Repos = map[string]project.LandingRepo{
		".":    {Mode: "pr", Base: "main", Remote: "origin", Forge: "github.com", Project: "PVjammer/foreman"},
		"site": {Mode: "ff", Base: "trunk"},
	}
	// Opted out per workstream: no landing text either.
	if b := briefFor(t, on, &ledger.Meta{Name: "w", Kind: "feature", Worktrees: wts}); strings.Contains(b, "## Landing") {
		t.Fatal("landing text for a workstream created with --no-landing")
	}
	b = briefFor(t, on, &ledger.Meta{Name: "w", Kind: "feature", Worktrees: wts, Landing: true})
	for _, want := range []string{"## Landing", "pull request into `main` on github.com/PVjammer/foreman", "fast-forward of local `trunk`",
		"`origin/main`, `trunk`", "landing.md", "followups.jsonl", "docs.patch", "## Report"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
}

func TestLandingPrompt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := project.Defaults()
	cfg.Landing.Enabled = true
	cfg.Landing.Repos = map[string]project.LandingRepo{".": {Mode: "ff"}}
	p := &project.Project{Root: t.TempDir(), Config: cfg}
	l := ledger.New(p)
	m := &ledger.Meta{Name: "w", Landing: true, Worktrees: []ledger.Worktree{{Repo: ".", Branch: "fm/w"}}}
	s, err := landingPrompt(p, l, m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "[foreman → w] Time to land") || !strings.Contains(s, "on your branch") {
		t.Fatalf("prompt:\n%s", s)
	}
}
