package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PVjammer/foreman/internal/forge/fake"
	"github.com/PVjammer/foreman/internal/project"
)

func sh(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func flags(set ...string) func(string) bool {
	m := map[string]bool{}
	for _, s := range set {
		m[s] = true
	}
	return func(n string) bool { return m[n] }
}

// onboard runs the landing questions non-interactively, writes the result like init does,
// and loads it back.
func onboard(t *testing.T, root string, repos []string, fl landingFlags, set ...string) *project.Project {
	t.Helper()
	doc, forges, err := landingSetup(newAsker(true), flags(set...), root, repos, fl, project.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]any{"name": "t", "landing": doc}
	if len(forges["project"]) > 0 {
		full["forges"] = forges["project"]
	}
	_ = os.MkdirAll(filepath.Join(root, project.Dir), 0o755)
	if err := project.WriteFile(root, full); err != nil {
		t.Fatal(err)
	}
	if err := saveForges(nil, map[string]map[string]any{"global": forges["global"]}); err != nil {
		t.Fatal(err)
	}
	p, err := project.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOnboardingOffByDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	sh(t, root, "git", "init", "-q", "-b", "main")
	p := onboard(t, root, []string{"."}, landingFlags{})
	if p.Config.Landing.Enabled {
		t.Fatal("landing enabled without being asked for")
	}
	if _, ok := p.Config.LandingFor("."); ok {
		t.Fatal("repo policy without landing")
	}
}

func TestOnboardingGitHubForkDetected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := fake.New("tok", "nick")
	ts := httptest.NewServer(srv)
	defer ts.Close()
	root := t.TempDir()
	sh(t, root, "git", "init", "-q", "-b", "main")
	sh(t, root, "git", "remote", "add", "origin", "git@github.com:nick/proj.git")
	sh(t, root, "git", "remote", "add", "upstream", "https://github.com/upstream-org/proj.git")
	p := onboard(t, root, []string{"."}, landingFlags{enable: "on", tokenCmd: "echo tok", api: ts.URL, save: "global"},
		"landing", "forge-token-cmd", "forge-api", "forge-save")
	pol, ok := p.Config.LandingFor(".")
	if !ok || !pol.PR() || pol.Forge != "github.com" || pol.Project != "upstream-org/proj" || pol.HeadOwner != "nick" ||
		pol.Remote != "origin" || pol.Upstream != "upstream" || pol.Branch != "fm/{name}" || pol.Draft {
		t.Fatalf("policy = %+v ok %v", pol, ok)
	}
	// Saved globally (the user chose it), so it merges into this and every other project.
	f, ok := p.Config.Forges["github.com"]
	if !ok || f.Kind != "github" || f.API != ts.URL || len(f.TokenCmd) != 2 {
		t.Fatalf("forge = %+v", f)
	}
	if b, _ := os.ReadFile(filepath.Join(root, project.Dir, "config.toml")); strings.Contains(string(b), "token_cmd") {
		t.Fatal("global forge entry also written to the project")
	}
	if srv.Calls["GET /user"] != 1 {
		t.Fatalf("token not checked at setup: %v", srv.Calls)
	}
}

func TestOnboardingSelfHostedGitLabAndFF(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	for _, r := range []string{"svc", "site"} {
		sh(t, root, "git", "init", "-q", "-b", "main", r)
	}
	sh(t, filepath.Join(root, "svc"), "git", "remote", "add", "origin", "ssh://git@gitlab.corp.example:2222/team/sub/svc.git")
	os.WriteFile(filepath.Join(root, "site", "go.mod"), []byte("module x\n"), 0o644)
	t.Setenv("CORP_TOKEN", "x")
	checked := ""
	defer func(orig func(context.Context, project.Forge, string) (string, error)) { checkForge = orig }(checkForge)
	checkForge = func(_ context.Context, fe project.Forge, proj string) (string, error) {
		checked = fe.Kind + " " + fe.API + " " + proj
		return "nick", nil
	}
	p := onboard(t, root, []string{"svc", "site"}, landingFlags{enable: "on", tokenEnv: "CORP_TOKEN", branch: "{ticket}-{name}"},
		"landing", "forge-token-env", "landing-branch")
	svc, ok := p.Config.LandingFor("svc")
	if !ok || !svc.PR() || svc.Forge != "gitlab.corp.example" || svc.Project != "team/sub/svc" || svc.Branch != "{ticket}-{name}" || svc.HeadOwner != "" {
		t.Fatalf("svc = %+v", svc)
	}
	f := p.Config.Forges["gitlab.corp.example"]
	if f.Kind != "gitlab" || f.API != "https://gitlab.corp.example" || f.TokenEnv != "CORP_TOKEN" {
		t.Fatalf("forge = %+v", f)
	}
	if checked != "gitlab https://gitlab.corp.example team/sub/svc" {
		t.Fatalf("token check = %q", checked)
	}
	site, ok := p.Config.LandingFor("site")
	if !ok || site.PR() || site.Gate != "go test ./..." {
		t.Fatalf("site = %+v", site)
	}
}

func TestOnboardingPRWithoutForgeRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	sh(t, root, "git", "init", "-q", "-b", "main")
	sh(t, root, "git", "remote", "add", "origin", "/srv/git/x.git")
	_, _, err := landingSetup(newAsker(true), flags("landing", "landing-mode"), root, []string{"."}, landingFlags{enable: "on", mode: "pr"}, project.Defaults())
	if err == nil {
		t.Fatal("pr mode accepted for a local remote")
	}
}

func TestParseChoices(t *testing.T) {
	c, err := parseChoices("1,3", "2", "1=autonomous")
	if err != nil || c[0].Action != "start" || c[0].Mode != "autonomous" || c[1].Action != "drop" || c[2].Action != "start" {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range [][3]string{{"0", "", ""}, {"x", "", ""}, {"", "", "1=fast"}} {
		if _, err := parseChoices(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}
